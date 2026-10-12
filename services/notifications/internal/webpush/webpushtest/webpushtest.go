// Package webpushtest is a fake push service for tests. Like a real one, it
// accepts a push only with a valid VAPID token for its origin, and like the
// browser it decrypts the payload with the subscription's private key, so a
// test sees exactly what the phone would show.
package webpushtest

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/notifications/internal/webpush"
)

// Push is one push the service accepted or refused, decrypted.
type Push struct {
	Device  string
	Payload []byte
	TTL     string
	Urgency string
	Topic   string
	// Status is what the service answered.
	Status int
	// Audience, Subject, and Expires are the VAPID token's claims.
	Audience string
	Subject  string
	Expires  time.Time
}

// Device is a fake browser subscription: its keys and endpoint.
type Device struct {
	Name         string
	Subscription webpush.Subscription
	private      *ecdh.PrivateKey
	auth         []byte
}

// Server is the fake push service. Endpoints are /push/<device name>.
type Server struct {
	*httptest.Server
	t         testing.TB
	vapid     *ecdsa.PublicKey
	vapidText string

	mu        sync.Mutex
	devices   map[string]*Device
	responses map[string][]int
	pushes    []Push
}

// NewServer starts a TLS push service that accepts pushes signed with the
// VAPID public key vapidPublicKey. Use its Client() to reach it.
func NewServer(t testing.TB, vapidPublicKey string) *Server {
	t.Helper()
	raw, err := webpush.DecodeBase64URL(vapidPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{t: t, vapid: key, vapidText: vapidPublicKey, devices: map[string]*Device{}, responses: map[string][]int{}}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// NewDevice subscribes a new fake browser named name.
func (s *Server) NewDevice(name string) *Device {
	s.t.Helper()
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		s.t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	device := &Device{
		Name: name,
		Subscription: webpush.Subscription{
			Endpoint: s.URL + "/push/" + name,
			P256dh:   webpush.EncodeBase64URL(private.PublicKey().Bytes()),
			Auth:     webpush.EncodeBase64URL(auth),
		},
		private: private,
		auth:    auth,
	}
	s.mu.Lock()
	s.devices[name] = device
	s.mu.Unlock()
	return device
}

// Respond makes the next pushes to the device get these statuses, in
// order; after them, 201 Created.
func (s *Server) Respond(device string, statuses ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses[device] = append(s.responses[device], statuses...)
}

// Pushes lists every push received so far, in order.
func (s *Server) Pushes() []Push {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Push(nil), s.pushes...)
}

// PushesTo lists the pushes received for one device.
func (s *Server) PushesTo(device string) []Push {
	var result []Push
	for _, push := range s.Pushes() {
		if push.Device == device {
			result = append(result, push)
		}
	}
	return result
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutPrefix(r.URL.Path, "/push/")
	s.mu.Lock()
	device := s.devices[name]
	s.mu.Unlock()
	if r.Method != http.MethodPost || !ok || device == nil {
		http.NotFound(w, r)
		return
	}
	push, err := s.read(r, device)
	if err != nil {
		s.t.Errorf("push to %s refused: %v", name, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	push.Status = http.StatusCreated
	if queued := s.responses[name]; len(queued) > 0 {
		push.Status, s.responses[name] = queued[0], queued[1:]
	}
	s.pushes = append(s.pushes, push)
	s.mu.Unlock()
	if push.Status >= 400 {
		http.Error(w, fmt.Sprintf("scripted %d", push.Status), push.Status)
		return
	}
	w.WriteHeader(push.Status)
}

// read checks a push as a push service and decrypts it as the browser.
func (s *Server) read(r *http.Request, device *Device) (Push, error) {
	push := Push{Device: device.Name, TTL: r.Header.Get("TTL"), Urgency: r.Header.Get("Urgency"), Topic: r.Header.Get("Topic")}
	if got := r.Header.Get("Content-Encoding"); got != "aes128gcm" {
		return push, fmt.Errorf("Content-Encoding %q, want aes128gcm", got)
	}
	if push.TTL == "" {
		return push, errors.New("no TTL header")
	}
	claims, err := s.verifyVAPID(r.Header.Get("Authorization"))
	if err != nil {
		return push, err
	}
	push.Audience, push.Subject, push.Expires = claims.Audience, claims.Subject, time.Unix(claims.Expires, 0)
	if want := s.URL; claims.Audience != want {
		return push, fmt.Errorf("VAPID audience %q, want %q", claims.Audience, want)
	}
	if until := time.Until(push.Expires); until <= 0 || until > 24*time.Hour {
		return push, fmt.Errorf("VAPID token expires in %s, want within 24 h", until)
	}
	if !webpush.ValidSubject(claims.Subject) {
		return push, fmt.Errorf("VAPID subject %q is not a mailto: or https URL", claims.Subject)
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
	if err != nil {
		return push, err
	}
	if len(body) > 4096 {
		return push, fmt.Errorf("body of %d bytes, more than 4096", len(body))
	}
	push.Payload, err = Decrypt(body, device.private, device.auth)
	return push, err
}

type claims struct {
	Audience string `json:"aud"`
	Expires  int64  `json:"exp"`
	Subject  string `json:"sub"`
}

// verifyVAPID checks "vapid t=<JWT>, k=<public key>" (RFC 8292): the key is
// the server's and the ES256 signature is valid.
func (s *Server) verifyVAPID(header string) (claims, error) {
	var token, key string
	rest, ok := strings.CutPrefix(header, "vapid ")
	if !ok {
		return claims{}, fmt.Errorf("Authorization %q is not vapid", header)
	}
	for _, part := range strings.Split(rest, ",") {
		name, value, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch name {
		case "t":
			token = value
		case "k":
			key = value
		}
	}
	if key != s.vapidText {
		return claims{}, fmt.Errorf("VAPID key %q is not the server's", key)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims{}, errors.New("the VAPID token is not a JWT")
	}
	rawHeader, err := webpush.DecodeBase64URL(parts[0])
	if err != nil {
		return claims{}, err
	}
	var jose struct{ Typ, Alg string }
	if err := json.Unmarshal(rawHeader, &jose); err != nil || jose.Alg != "ES256" || jose.Typ != "JWT" {
		return claims{}, fmt.Errorf("JWT header %s, want ES256 JWT", rawHeader)
	}
	signature, err := webpush.DecodeBase64URL(parts[2])
	if err != nil || len(signature) != 64 {
		return claims{}, errors.New("the JWT signature is not 64 raw bytes")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, sv := new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])
	if !ecdsa.Verify(s.vapid, digest[:], r, sv) {
		return claims{}, errors.New("the VAPID signature does not verify")
	}
	rawClaims, err := webpush.DecodeBase64URL(parts[1])
	if err != nil {
		return claims{}, err
	}
	var result claims
	if err := json.Unmarshal(rawClaims, &result); err != nil {
		return claims{}, err
	}
	return result, nil
}

// Decrypt reverses webpush.Encrypt as the browser does (RFC 8291 and the
// aes128gcm content coding of RFC 8188), with the subscription's private
// key and auth secret.
func Decrypt(body []byte, private *ecdh.PrivateKey, auth []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("the body is shorter than the aes128gcm header")
	}
	salt, recordSize, idLength := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if len(body) < 21+idLength {
		return nil, errors.New("the body is shorter than its key id")
	}
	serverPublic, ciphertext := body[21:21+idLength], body[21+idLength:]
	if uint32(len(ciphertext)) > recordSize {
		return nil, fmt.Errorf("a %d-byte ciphertext does not fit one %d-byte record", len(ciphertext), recordSize)
	}
	server, err := ecdh.P256().NewPublicKey(serverPublic)
	if err != nil {
		return nil, fmt.Errorf("the key id is not a P-256 public key: %w", err)
	}
	shared, err := private.ECDH(server)
	if err != nil {
		return nil, err
	}
	keyInfo := "WebPush: info\x00" + string(private.PublicKey().Bytes()) + string(serverPublic)
	ikm, err := hkdf.Key(sha256.New, shared, auth, keyInfo, 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	record, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	// The last record ends with the delimiter 2, then zero padding.
	end := len(record) - 1
	for end >= 0 && record[end] == 0 {
		end--
	}
	if end < 0 || record[end] != 0x02 {
		return nil, errors.New("the record does not end with the last-record delimiter")
	}
	return record[:end], nil
}
