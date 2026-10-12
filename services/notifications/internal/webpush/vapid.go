// Package webpush sends Web Push messages (RFC 8030) the way browsers' push
// services accept them: the payload encrypted for the subscribed browser
// (RFC 8291, with the aes128gcm content coding of RFC 8188) and each request
// signed with the server's VAPID key (RFC 8292). It uses the standard
// library only.
package webpush

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

// Keys is the server's VAPID key pair: a P-256 key whose public half the
// browser is given when it subscribes, so its push service accepts only
// pushes signed with the private half.
type Keys struct {
	private *ecdsa.PrivateKey
	public  string
}

// tokenLifetime is how long a VAPID token is valid; RFC 8292 allows at most
// 24 hours.
const tokenLifetime = 12 * time.Hour

// ParseKeys reads a VAPID key pair in the usual form: the public key as an
// uncompressed P-256 point (65 bytes) and the private key as a 32-byte
// scalar, both base64url without padding. It refuses a public key that does
// not belong to the private key, so a mistyped pair stops the service
// instead of every push being refused.
func ParseKeys(publicKey, privateKey string) (*Keys, error) {
	rawPrivate, err := DecodeBase64URL(privateKey)
	if err != nil || len(rawPrivate) != 32 {
		return nil, errors.New("the VAPID private key must be 32 bytes, base64url")
	}
	private, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), rawPrivate)
	if err != nil {
		return nil, errors.New("the VAPID private key is not a P-256 private key")
	}
	rawPublic, err := DecodeBase64URL(publicKey)
	if err != nil || len(rawPublic) != 65 {
		return nil, errors.New("the VAPID public key must be an uncompressed P-256 point (65 bytes), base64url")
	}
	derived, err := private.PublicKey.Bytes()
	if err != nil {
		return nil, fmt.Errorf("derive the VAPID public key: %w", err)
	}
	if !bytes.Equal(derived, rawPublic) {
		return nil, errors.New("the VAPID public key does not belong to the private key")
	}
	return &Keys{private: private, public: EncodeBase64URL(rawPublic)}, nil
}

// GenerateKeys makes a new VAPID key pair in the form ParseKeys reads.
func GenerateKeys() (publicKey, privateKey string, err error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	rawPrivate, err := private.Bytes()
	if err != nil {
		return "", "", err
	}
	rawPublic, err := private.PublicKey.Bytes()
	if err != nil {
		return "", "", err
	}
	return EncodeBase64URL(rawPublic), EncodeBase64URL(rawPrivate), nil
}

// PublicKey is the public key as browsers take it (applicationServerKey).
func (k *Keys) PublicKey() string { return k.public }

// ValidSubject reports whether subject may be a VAPID subject: a mailto:
// address or an https URL where the push service's operator can reach the
// sender.
func ValidSubject(subject string) bool {
	if address, ok := strings.CutPrefix(subject, "mailto:"); ok {
		parsed, err := mail.ParseAddress(address)
		return err == nil && parsed.Address == address
	}
	parsed, err := url.Parse(subject)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

// authorization is the VAPID Authorization header for a push to endpoint:
// a JWT signed with ES256 for the push service's origin, valid until
// expires, and the public key.
func (k *Keys) authorization(endpoint, subject string, now time.Time) (string, error) {
	target, err := url.Parse(endpoint)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return "", errors.New("the subscription endpoint is not an absolute URL")
	}
	claims, err := json.Marshal(struct {
		Audience string `json:"aud"`
		Expires  int64  `json:"exp"`
		Subject  string `json:"sub"`
	}{target.Scheme + "://" + target.Host, now.Add(tokenLifetime).Unix(), subject})
	if err != nil {
		return "", err
	}
	signingInput := EncodeBase64URL([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + EncodeBase64URL(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, k.private, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign the VAPID token: %w", err)
	}
	// JWS wants the raw signature: r and s, 32 bytes each.
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return "vapid t=" + signingInput + "." + EncodeBase64URL(signature) + ", k=" + k.public, nil
}

// EncodeBase64URL is base64url without padding, as Web Push uses
// everywhere.
func EncodeBase64URL(data []byte) string { return base64.RawURLEncoding.EncodeToString(data) }

// DecodeBase64URL reads base64url with or without padding, and tolerates
// the standard alphabet (+ and /) that some tools print.
func DecodeBase64URL(value string) ([]byte, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "=")
	value = strings.NewReplacer("+", "-", "/", "_").Replace(value)
	return base64.RawURLEncoding.DecodeString(value)
}
