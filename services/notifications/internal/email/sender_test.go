package email

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func configFor(t *testing.T, server *fakeSMTP, settings Settings) Config {
	t.Helper()
	settings.Host = "127.0.0.1"
	settings.Port = strconv.Itoa(server.port())
	if settings.From == "" {
		settings.From = "lifeos@example.com"
	}
	config, ok, problem := Parse(settings)
	if !ok {
		t.Fatal(problem)
	}
	return config
}

func message() Message {
	return Message{
		To:      "owner@example.com",
		Subject: "Verify your email address",
		Text:    "Open this link: https://lifeos.example.com/verify-email/abc\n",
		HTML:    `<p>Open <a href="https://lifeos.example.com/verify-email/abc">this link</a>.</p>`,
		Kind:    "verify_email",
	}
}

func failureOf(t *testing.T, err error) *Failure {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("not a Failure: %v", err)
	}
	return failure
}

func TestSendOverSTARTTLSWithASignIn(t *testing.T) {
	server := newFakeSMTP(t, func(s *fakeSMTP) {
		s.startTLS, s.user, s.secret = true, "apikey", "s3cret-key-value"
	})
	config := configFor(t, server, Settings{TLS: "starttls", Username: "apikey", Password: "s3cret-key-value", FromName: "Life OS at home"})
	if err := NewSender(config, WithRootCAs(server.roots)).Send(context.Background(), message()); err != nil {
		t.Fatal(err)
	}
	got := server.received()
	if len(got) != 1 || !got[0].tls || got[0].from != "lifeos@example.com" || got[0].to != "owner@example.com" {
		t.Fatalf("received %+v", got)
	}
	data := got[0].data
	for _, want := range []string{
		"Subject: Verify your email address",
		`From: "Life OS at home" <lifeos@example.com>`,
		"To: <owner@example.com>",
		"Auto-Submitted: auto-generated",
		"Content-Type: multipart/alternative",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Type: text/html; charset=UTF-8",
		"Message-ID: <",
	} {
		if !strings.Contains(data, want) {
			t.Errorf("the message has no %q:\n%s", want, data)
		}
	}
	if strings.Contains(data, "go-mail") {
		t.Error("the message names the library (User-Agent)")
	}
}

func TestSendOverImplicitTLS(t *testing.T) {
	server := newFakeSMTP(t, func(s *fakeSMTP) {
		s.implicitTLS, s.user, s.secret = true, "me@example.com", "app-password"
	})
	config := configFor(t, server, Settings{TLS: "tls", Username: "me@example.com", Password: "app-password"})
	if err := NewSender(config, WithRootCAs(server.roots)).Send(context.Background(), message()); err != nil {
		t.Fatal(err)
	}
	if got := server.received(); len(got) != 1 || !got[0].tls {
		t.Fatalf("received %+v", got)
	}
}

func TestSendToALocalRelayWithoutTLS(t *testing.T) {
	server := newFakeSMTP(t, nil)
	config := configFor(t, server, Settings{TLS: "none"})
	msg := message()
	msg.HTML = ""
	if err := NewSender(config).Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	got := server.received()
	if len(got) != 1 || got[0].tls || strings.Contains(got[0].data, "text/html") {
		t.Fatalf("received %+v", got)
	}
}

func TestSendFailures(t *testing.T) {
	const secret = "the-wrong-secret-value"
	for _, tc := range []struct {
		name      string
		server    func(*fakeSMTP)
		settings  Settings
		code      string
		recipient bool
		mentions  string
	}{
		{
			name:     "a wrong password",
			server:   func(s *fakeSMTP) { s.startTLS, s.user, s.secret = true, "apikey", "right" },
			settings: Settings{Username: "apikey", Password: secret},
			code:     CodeAuth, mentions: "SMTP 535",
		},
		{
			name:     "no STARTTLS on the port",
			server:   nil,
			settings: Settings{TLS: "starttls"},
			code:     CodeTLS, mentions: "does not offer STARTTLS",
		},
		{
			name:     "TLS on a plain port",
			server:   nil,
			settings: Settings{TLS: "tls"},
			code:     CodeTLS,
		},
		{
			name:     "a certificate nobody vouches for",
			server:   func(s *fakeSMTP) { s.startTLS = true },
			settings: Settings{TLS: "starttls"},
			code:     CodeTLS, mentions: "known authority",
		},
		{
			name:     "a refused From",
			server:   func(s *fakeSMTP) { s.mailReply = "553 5.7.1 <lifeos@example.com>: Sender address rejected" },
			settings: Settings{TLS: "none"},
			code:     CodeSender, mentions: "SMTP 553",
		},
		{
			name:     "a refused recipient",
			server:   func(s *fakeSMTP) { s.rcptReply = "550 5.1.1 <owner@example.com>: Recipient address rejected" },
			settings: Settings{TLS: "none"},
			code:     CodeRecipient, recipient: true, mentions: "SMTP 550",
		},
		{
			name:     "a refused message",
			server:   func(s *fakeSMTP) { s.dataReply = "554 5.7.1 Message rejected as spam" },
			settings: Settings{TLS: "none"},
			code:     CodeMessage, mentions: "SMTP 554",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newFakeSMTP(t, tc.server)
			config := configFor(t, server, tc.settings)
			options := []Option{WithTimeout(5 * time.Second)}
			if tc.name != "a certificate nobody vouches for" {
				options = append(options, WithRootCAs(server.roots))
			}
			err := NewSender(config, options...).Send(context.Background(), message())
			failure := failureOf(t, err)
			if failure.Code != tc.code || failure.Recipient != tc.recipient {
				t.Fatalf("got %s (recipient %v): %s", failure.Code, failure.Recipient, failure.Message)
			}
			if tc.mentions != "" && !strings.Contains(failure.Message, tc.mentions) {
				t.Errorf("the message does not say %q: %s", tc.mentions, failure.Message)
			}
			for _, secretText := range []string{secret, "owner@example.com", "Recipient address rejected", "spam"} {
				if strings.Contains(failure.Error(), secretText) {
					t.Errorf("the failure quotes %q: %s", secretText, failure.Error())
				}
			}
		})
	}
}

func TestSendToNobodyListening(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	config, ok, problem := Parse(Settings{Host: "127.0.0.1", Port: strconv.Itoa(port), TLS: "none", From: "lifeos@example.com"})
	if !ok {
		t.Fatal(problem)
	}
	failure := failureOf(t, NewSender(config, WithTimeout(3*time.Second)).Send(context.Background(), message()))
	if failure.Code != CodeConnect || !strings.Contains(failure.Message, "connection refused") {
		t.Fatalf("%s: %s", failure.Code, failure.Message)
	}
}

func TestSendToASilentServerTimesOut(t *testing.T) {
	server := newFakeSMTP(t, func(s *fakeSMTP) { s.silent = true })
	config := configFor(t, server, Settings{TLS: "none"})
	failure := failureOf(t, NewSender(config, WithTimeout(time.Second)).Send(context.Background(), message()))
	if failure.Code != CodeTimeout {
		t.Fatalf("%s: %s", failure.Code, failure.Message)
	}
}

func TestSendRefusesABadRecipientBeforeConnecting(t *testing.T) {
	config, ok, problem := Parse(Settings{Host: "smtp.invalid", From: "lifeos@example.com"})
	if !ok {
		t.Fatal(problem)
	}
	msg := message()
	msg.To = "owner@example.com\r\nBcc: someone@example.com"
	failure := failureOf(t, NewSender(config).Send(context.Background(), msg))
	if failure.Code != CodeRecipient || !failure.Recipient {
		t.Fatalf("%s", failure.Code)
	}
}
