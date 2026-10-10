package google

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"
)

// StateLifetime bounds how long the owner has to answer Google's consent
// page; the state is single use within it.
const StateLifetime = 10 * time.Minute

// randomToken is n random bytes in unpadded base64url.
func randomToken(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// NewState is a fresh OAuth state: 256 random bits.
func NewState() (string, error) {
	return randomToken(32)
}

// HashState is what Calendar stores of a state, so a read of the table
// cannot complete an authorization.
func HashState(state string) []byte {
	sum := sha256.Sum256([]byte(state))
	return sum[:]
}

// NewVerifier is a fresh PKCE code verifier (RFC 7636): 43 characters from
// 256 random bits.
func NewVerifier() (string, error) {
	return randomToken(32)
}

// Challenge is the S256 code challenge of verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
