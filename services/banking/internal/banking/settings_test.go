package banking

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/services/banking/internal/enablebanking/fakebank"
)

func keyFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "enablebanking.pem")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func pemKey(t *testing.T) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(fakebank.Key())
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

var tokenKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))

func TestLoadSettings(t *testing.T) {
	key := keyFile(t, pemKey(t))
	redirect := ExampleRedirectURI
	settings, err := LoadSettings(fakebank.AppID, key, tokenKey, redirect, "")
	if err != nil || !settings.Configured() || settings.RedirectURI != redirect {
		t.Fatalf("LoadSettings = %+v, %v", settings, err)
	}
	local, err := LoadSettings(fakebank.AppID, key, tokenKey, "http://localhost:3000/finance/bank/callback", "")
	if err != nil || !local.Configured() {
		t.Fatalf("localhost redirect = %+v, %v", local, err)
	}

	// Nothing set, or only the verification stack's empty key: not
	// configured, no error.
	for _, file := range []string{"", keyFile(t, nil), filepath.Join(t.TempDir(), "missing.pem")} {
		if settings, err := LoadSettings("", file, "", "", ""); err != nil || settings.Configured() {
			t.Errorf("key file %q: %+v, %v", file, settings, err)
		}
	}

	for name, tc := range map[string]struct {
		appID, file, token, redirect string
		want                         string
	}{
		"no app id":         {"", key, tokenKey, redirect, EnvAppID},
		"empty key":         {fakebank.AppID, keyFile(t, nil), tokenKey, redirect, EnvKeyFile},
		"no token key":      {fakebank.AppID, key, "", redirect, EnvTokenKey},
		"short token key":   {fakebank.AppID, key, base64.StdEncoding.EncodeToString([]byte("short")), redirect, EnvTokenKey},
		"not a key":         {fakebank.AppID, keyFile(t, []byte("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n")), tokenKey, redirect, EnvKeyFile},
		"no redirect":       {fakebank.AppID, key, tokenKey, "", EnvRedirect},
		"relative redirect": {fakebank.AppID, key, tokenKey, "/finance/bank/callback", EnvRedirect},
		"http redirect":     {fakebank.AppID, key, tokenKey, "http://lifeos.example/finance/bank/callback", EnvRedirect},
	} {
		settings, err := LoadSettings(tc.appID, tc.file, tc.token, tc.redirect, "")
		if err == nil || settings.Configured() || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %+v, %v", name, settings, err)
			continue
		}
		// Messages name settings, never values.
		if strings.Contains(err.Error(), tokenKey) || strings.Contains(err.Error(), "BEGIN") || strings.Contains(err.Error(), tc.file) && tc.file != "" {
			t.Errorf("%s: the error repeats a value: %v", name, err)
		}
	}
}

func TestSealOpensOnlyWithTheSameKey(t *testing.T) {
	sealer, _ := NewSealer(bytes.Repeat([]byte{1}, 32))
	ciphertext, nonce, err := sealer.Seal([]byte("session-id"))
	if err != nil || bytes.Contains(ciphertext, []byte("session-id")) {
		t.Fatalf("Seal = %x, %v", ciphertext, err)
	}
	if plain, err := sealer.Open(ciphertext, nonce); err != nil || string(plain) != "session-id" {
		t.Fatalf("Open = %q, %v", plain, err)
	}
	other, _ := NewSealer(bytes.Repeat([]byte{2}, 32))
	if _, err := other.Open(ciphertext, nonce); err != ErrUnreadableSession {
		t.Errorf("another key opened it: %v", err)
	}
	ciphertext[0] ^= 1
	if _, err := sealer.Open(ciphertext, nonce); err != ErrUnreadableSession {
		t.Errorf("an altered ciphertext opened: %v", err)
	}
	if _, err := NewSealer([]byte("short")); err == nil {
		t.Error("a short key was accepted")
	}
	state, _ := NewState()
	if len(state) < 43 || string(HashState(state)) == state || len(HashState(state)) != 32 {
		t.Errorf("state %q hash %x", state, HashState(state))
	}
}
