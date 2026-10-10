package banking

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// The approach follows Calendar's Google connection (sealed refresh token,
// hashed single-use OAuth state), copied rather than imported: services
// share no internals.

// TokenKeySize is the size of LIFE_OS_BANKING_TOKEN_KEY: an AES-256 key.
const TokenKeySize = 32

// sealContext is the AES-GCM additional data: a ciphertext opens only as a
// Banking provider session id of this format version.
var sealContext = []byte("life-os/banking/provider-session/v1")

// ErrUnreadableSession means a stored session id does not open with the
// configured key: the key changed, or the ciphertext was altered.
var ErrUnreadableSession = errors.New("the stored bank session cannot be decrypted with LIFE_OS_BANKING_TOKEN_KEY")

// Sealer encrypts and authenticates session ids with AES-256-GCM.
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
// ErrUnreadableSession.
func (s *Sealer) Open(ciphertext, nonce []byte) ([]byte, error) {
	if len(nonce) != s.aead.NonceSize() {
		return nil, ErrUnreadableSession
	}
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, sealContext)
	if err != nil {
		return nil, ErrUnreadableSession
	}
	return plaintext, nil
}

// StateLifetime bounds how long the owner has to give consent at the bank;
// the state is single use within it.
const StateLifetime = 10 * time.Minute

// NewState is a fresh authorization state: 256 random bits, base64url.
func NewState() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// HashState is what Banking stores of a state, so a read of the table
// cannot complete an authorization.
func HashState(state string) []byte {
	sum := sha256.Sum256([]byte(state))
	return sum[:]
}
