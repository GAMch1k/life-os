package email

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a small SMTP server for the tests: plain, implicit TLS, or
// STARTTLS; AUTH PLAIN over TLS only; MAIL, RCPT, and DATA that can be told
// to refuse. It keeps what it received.
type fakeSMTP struct {
	t        *testing.T
	listener net.Listener
	tls      *tls.Config
	roots    *x509.CertPool

	// What it does: offer STARTTLS, wrap every connection in TLS, require
	// a sign-in with this user and password, and refuse with these replies
	// ("" accepts).
	startTLS     bool
	implicitTLS  bool
	user, secret string
	mailReply    string
	rcptReply    string
	dataReply    string
	// silent never greets: a client waits for the banner (tls on a
	// starttls port does that).
	silent bool

	mu       sync.Mutex
	messages []received
	authUser string
}

type received struct {
	from, to string
	data     string
	tls      bool
}

func newFakeSMTP(t *testing.T, configure func(*fakeSMTP)) *fakeSMTP {
	t.Helper()
	s := &fakeSMTP{t: t}
	if configure != nil {
		configure(s)
	}
	s.tls, s.roots = selfSigned(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if s.implicitTLS {
		listener = tls.NewListener(listener, s.tls)
	}
	s.listener = listener
	go s.serve()
	t.Cleanup(func() { _ = listener.Close() })
	return s
}

func (s *fakeSMTP) port() int { return s.listener.Addr().(*net.TCPAddr).Port }

func (s *fakeSMTP) received() []received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]received(nil), s.messages...)
}

func (s *fakeSMTP) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeSMTP) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if s.silent {
		time.Sleep(3 * time.Second)
		return
	}
	encrypted := s.implicitTLS
	reader := bufio.NewReader(conn)
	write := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	write("220 fake.test ESMTP")
	signedIn := false
	var current received
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch verb {
		case "EHLO", "HELO":
			write("250-fake.test")
			if s.startTLS && !encrypted {
				write("250-STARTTLS")
			}
			if encrypted {
				write("250-AUTH PLAIN LOGIN")
			}
			write("250 8BITMIME")
		case "STARTTLS":
			if !s.startTLS || encrypted {
				write("502 no")
				continue
			}
			write("220 go ahead")
			tlsConn := tls.Server(conn, s.tls)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			conn = tlsConn
			reader = bufio.NewReader(conn)
			encrypted = true
		case "AUTH":
			fields := strings.Fields(line)
			if len(fields) < 3 || strings.ToUpper(fields[1]) != "PLAIN" {
				write("504 PLAIN only")
				continue
			}
			raw, _ := base64.StdEncoding.DecodeString(fields[2])
			parts := strings.Split(string(raw), "\x00")
			if len(parts) == 3 && parts[1] == s.user && parts[2] == s.secret {
				signedIn = true
				s.mu.Lock()
				s.authUser = parts[1]
				s.mu.Unlock()
				write("235 2.7.0 Authentication successful")
			} else {
				write("535 5.7.8 Authentication credentials invalid")
			}
		case "MAIL":
			if s.user != "" && !signedIn {
				write("530 5.7.0 Authentication required")
				continue
			}
			if s.mailReply != "" {
				write(s.mailReply)
				continue
			}
			current = received{from: between(line, "<", ">"), tls: encrypted}
			write("250 2.1.0 Ok")
		case "RCPT":
			if s.rcptReply != "" {
				write(s.rcptReply)
				continue
			}
			current.to = between(line, "<", ">")
			write("250 2.1.5 Ok")
		case "DATA":
			write("354 End data with <CR><LF>.<CR><LF>")
			var data strings.Builder
			for {
				part, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if part == ".\r\n" {
					break
				}
				data.WriteString(part)
			}
			if s.dataReply != "" {
				write(s.dataReply)
				continue
			}
			current.data = data.String()
			s.mu.Lock()
			s.messages = append(s.messages, current)
			s.mu.Unlock()
			write("250 2.0.0 Ok: queued")
		case "QUIT":
			write("221 2.0.0 Bye")
			return
		default:
			write("250 Ok")
		}
	}
}

func between(line, open, close string) string {
	start := strings.Index(line, open)
	end := strings.LastIndex(line, close)
	if start < 0 || end <= start {
		return ""
	}
	return line[start+1 : end]
}

// selfSigned is a certificate for 127.0.0.1 and the pool that trusts it.
func selfSigned(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fake.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}, roots
}
