package google

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

var testKey = bytes.Repeat([]byte{7}, TokenKeySize)

func TestConfigIsUnsetPartialOrInvalidWithoutCrashing(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(testKey)

	unset, err := NewConfig("", "", "", "")
	if err != nil || unset.Configured() {
		t.Fatalf("unset = %+v, %v; want not configured, no error", unset, err)
	}

	partial, err := NewConfig("client.apps.googleusercontent.com", "", key, "")
	if err == nil || partial.Configured() || !strings.Contains(err.Error(), "LIFE_OS_GOOGLE_CLIENT_SECRET is not set") {
		t.Fatalf("partial = %+v, %v", partial, err)
	}
	if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "client.apps") {
		t.Fatalf("error names a value: %v", err)
	}

	for value, want := range map[string]string{
		"not base64!!": "must be base64",
		base64.StdEncoding.EncodeToString([]byte("too short")): "not 9 bytes",
	} {
		config, err := NewConfig("id", "super-secret", value, "")
		if err == nil || config.Configured() || !strings.Contains(err.Error(), want) {
			t.Errorf("key %q: %+v, %v; want error with %q", value, config, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "super-secret") {
			t.Errorf("error names the secret: %v", err)
		}
	}

	for _, redirect := range []string{"lifeos.gamch1k.com/callback", "http://lifeos.gamch1k.com/calendar/google/callback", "https://user@host/cb"} {
		if config, err := NewConfig("id", "secret", key, redirect); err == nil || config.Configured() {
			t.Errorf("redirect %q accepted", redirect)
		}
	}

	config, err := NewConfig(" id ", "secret", key, "")
	if err != nil || !config.Configured() || config.RedirectURI != DefaultRedirectURI || config.ClientID != "id" || !bytes.Equal(config.TokenKey, testKey) {
		t.Fatalf("configured = %+v, %v", config, err)
	}
	local, err := NewConfig("id", "secret", key, "http://localhost:3000/calendar/google/callback")
	if err != nil || local.RedirectURI != "http://localhost:3000/calendar/google/callback" {
		t.Fatalf("localhost redirect = %+v, %v", local, err)
	}
}

func TestParseTokenKeyAcceptsEveryBase64Form(t *testing.T) {
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		key, err := ParseTokenKey("  " + encoding.EncodeToString(testKey) + "\n")
		if err != nil || !bytes.Equal(key, testKey) {
			t.Errorf("%v: %x, %v", encoding, key, err)
		}
	}
	if _, err := ParseTokenKey(base64.StdEncoding.EncodeToString(make([]byte, 16))); err == nil {
		t.Fatal("a 16-byte key was accepted")
	}
}

func TestAuthURLAsksForOfflineConsentWithStateAndPKCE(t *testing.T) {
	config := Config{ClientID: "id", ClientSecret: "secret", RedirectURI: DefaultRedirectURI, TokenKey: testKey}
	raw := config.AuthURL(GoogleEndpoints.AuthURL, "the-state", "the-challenge", "owner@example.com")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host != "accounts.google.com" {
		t.Fatalf("url = %s, %v", raw, err)
	}
	query := parsed.Query()
	for name, want := range map[string]string{
		"client_id": "id", "redirect_uri": DefaultRedirectURI, "response_type": "code", "access_type": "offline", "prompt": "consent",
		"state": "the-state", "code_challenge": "the-challenge", "code_challenge_method": "S256", "login_hint": "owner@example.com",
		"scope": ScopeCalendarList + " " + ScopeEvents + " " + ScopeAppCreated,
	} {
		if got := query.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if strings.Contains(raw, "secret") {
		t.Fatal("the consent URL carries the client secret")
	}
	if query := mustQuery(t, config.AuthURL("https://example.test/auth?x=1", "s", "c", "")); query.Get("x") != "1" || query.Has("login_hint") {
		t.Fatalf("existing query or empty hint: %v", query)
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query()
}

func TestMissingScopes(t *testing.T) {
	if got := MissingScopes(Scopes); len(got) != 0 {
		t.Fatalf("all granted: %v", got)
	}
	got := MissingScopes([]string{ScopeCalendarList, "openid"})
	if strings.Join(got, " ") != ScopeEvents+" "+ScopeAppCreated {
		t.Fatalf("missing = %v", got)
	}
	if got := MissingScopes(nil); len(got) != 3 {
		t.Fatalf("none granted: %v", got)
	}
}

func TestSealRoundTripsAndDetectsTampering(t *testing.T) {
	sealer, err := NewSealer(testKey)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("1//0refresh-token-value")
	ciphertext, nonce, err := sealer.Seal(secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, secret) || len(nonce) != 12 {
		t.Fatalf("ciphertext %x nonce %x", ciphertext, nonce)
	}
	opened, err := sealer.Open(ciphertext, nonce)
	if err != nil || !bytes.Equal(opened, secret) {
		t.Fatalf("open = %q, %v", opened, err)
	}
	again, nonceAgain, _ := sealer.Seal(secret)
	if bytes.Equal(again, ciphertext) || bytes.Equal(nonceAgain, nonce) {
		t.Fatal("sealing twice gave the same ciphertext or nonce")
	}

	flip := func(in []byte, index int) []byte {
		out := bytes.Clone(in)
		out[index] ^= 0x01
		return out
	}
	other, _ := NewSealer(bytes.Repeat([]byte{8}, TokenKeySize))
	for name, open := range map[string]func() ([]byte, error){
		"ciphertext": func() ([]byte, error) { return sealer.Open(flip(ciphertext, 0), nonce) },
		"tag":        func() ([]byte, error) { return sealer.Open(flip(ciphertext, len(ciphertext)-1), nonce) },
		"nonce":      func() ([]byte, error) { return sealer.Open(ciphertext, flip(nonce, 3)) },
		"short":      func() ([]byte, error) { return sealer.Open(ciphertext, nonce[:8]) },
		"truncated":  func() ([]byte, error) { return sealer.Open(ciphertext[:len(ciphertext)-2], nonce) },
		"key":        func() ([]byte, error) { return other.Open(ciphertext, nonce) },
	} {
		if plaintext, err := open(); !errors.Is(err, ErrUnreadableToken) || plaintext != nil {
			t.Errorf("tampered %s: %q, %v", name, plaintext, err)
		}
	}
	if _, err := NewSealer(testKey[:16]); err == nil {
		t.Fatal("a 16-byte key was accepted")
	}
}

func TestStateAndPKCE(t *testing.T) {
	// RFC 7636, appendix B.
	if got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("challenge = %s", got)
	}
	seen := map[string]bool{}
	for range 50 {
		state, err := NewState()
		if err != nil || len(state) != 43 || seen[state] {
			t.Fatalf("state %q, %v", state, err)
		}
		seen[state] = true
		verifier, err := NewVerifier()
		if err != nil || len(verifier) < 43 || len(verifier) > 128 || strings.ContainsAny(verifier, "+/=") {
			t.Fatalf("verifier %q, %v", verifier, err)
		}
	}
	if bytes.Equal(HashState("a"), HashState("b")) || len(HashState("a")) != 32 || string(HashState("a")) == "a" {
		t.Fatal("state hash")
	}
}

func TestAccessTokensAreRefreshedAMarginBeforeTheyExpire(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		left  time.Duration
		fresh bool
	}{
		{time.Hour, true},
		{RefreshMargin + time.Second, true},
		{RefreshMargin, false},
		{time.Minute, false},
		{-time.Minute, false},
	} {
		if got := Fresh(now.Add(tc.left), now); got != tc.fresh {
			t.Errorf("%v left: fresh = %v, want %v", tc.left, got, tc.fresh)
		}
	}
	token := accessToken{value: "x", expiresAt: now.Add(time.Hour)}
	if token.usable(token.connection, now) != true || (accessToken{}).usable(token.connection, now) {
		t.Fatal("usable")
	}
}
