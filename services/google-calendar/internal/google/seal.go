package google

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// sealContext is the AES-GCM additional data: a ciphertext opens only as a
// Calendar Google refresh token of this format version.
var sealContext = []byte("life-os/calendar/google-refresh-token/v1")

// ErrUnreadableToken means a stored token does not open with the configured
// key: the key changed, or the ciphertext was altered.
var ErrUnreadableToken = errors.New("the stored Google token cannot be decrypted with LIFE_OS_GOOGLE_TOKEN_KEY")

// Sealer encrypts and authenticates refresh tokens with AES-256-GCM.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer uses key, which must be TokenKeySize bytes.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != TokenKeySize {
		return nil, fmt.Errorf("token key must be %d bytes", TokenKeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plaintext under a fresh random nonce and returns both.
func (s *Sealer) Seal(plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return s.aead.Seal(nil, nonce, plaintext, sealContext), nonce, nil
}

// Open decrypts what Seal returned; any change to either fails with
// ErrUnreadableToken.
func (s *Sealer) Open(ciphertext, nonce []byte) ([]byte, error) {
	if len(nonce) != s.aead.NonceSize() {
		return nil, ErrUnreadableToken
	}
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, sealContext)
	if err != nil {
		return nil, ErrUnreadableToken
	}
	return plaintext, nil
}
