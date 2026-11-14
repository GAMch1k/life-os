// Package identitytest makes identity tokens for services' tests, as the
// Dashboard's auth check mints them (apps/dashboard/src/lib/auth/identity.ts),
// with a made-up key the test's verifier trusts.
package identitytest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/identity"
)

// FirstUser is the first account's fixed id.
const FirstUser = "00000000-0000-4000-8000-000000000001"

// Signer signs identity tokens with a key only its KeySet trusts.
type Signer struct {
	kid     string
	private ed25519.PrivateKey
	// Keys are the trusted keys to give the service under test
	// (httpserver.WithTrustedKeys).
	Keys *identity.KeySet
}

// New makes a signer with a fresh key.
func New(t testing.TB) Signer {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const kid = "auth-test"
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]string{
		"kty": "OKP", "crv": "Ed25519", "kid": kid, "use": "sig", "alg": "EdDSA",
		"x": base64.RawURLEncoding.EncodeToString(public),
	}}})
	keys, err := identity.ParseKeySet(string(jwks))
	if err != nil {
		t.Fatal(err)
	}
	return Signer{kid: kid, private: private, Keys: keys}
}

// Token is a five-minute token for user (FirstUser when empty) with a server
// role (owner, admin, member) and a way in (session, legacy, device, token,
// mcp). A session made with Token signed in without a second factor; see
// TwoFactor.
func (s Signer) Token(user, role, via string) string {
	return s.TokenWith(user, role, via, nil)
}

// TwoFactor is Token for a browser session that signed in with a second
// factor (the mfa claim, which admin routes require).
func (s Signer) TwoFactor(user, role string) string {
	return s.TokenWith(user, role, identity.ViaSession, map[string]any{"mfa": true})
}

// TokenWith is Token with more claims, such as scp, mfa, or dev.
func (s Signer) TokenWith(user, role, via string, extra map[string]any) string {
	if user == "" {
		user = FirstUser
	}
	sid := uuid.NewString()
	if via == identity.ViaLegacy {
		sid = "legacy"
	}
	now := time.Now()
	claims := map[string]any{
		"iss": "lifeos-auth", "aud": "lifeos-services", "sub": user, "sid": sid,
		"via": via, "role": role, "iat": now.Unix(), "nbf": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(), "jti": uuid.NewString(),
	}
	for key, value := range extra {
		claims[key] = value
	}
	return s.Sign(claims)
}

// Sign signs any claims.
func (s Signer) Sign(claims map[string]any) string {
	encode := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	input := encode(map[string]string{"alg": "EdDSA", "typ": "lifeos-id+jwt", "kid": s.kid}) + "." + encode(claims)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.private, []byte(input)))
}
