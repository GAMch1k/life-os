package activity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/identity"
)

// The actor of a request with an identity token: the person who signed in;
// under legacy access (no credential) the header, as before accounts.
func TestActorFromIdentity(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]string{
		"kty": "OKP", "crv": "Ed25519", "kid": "k", "x": base64.RawURLEncoding.EncodeToString(public),
	}}})
	keys, err := identity.ParseKeySet(string(jwks))
	if err != nil {
		t.Fatal(err)
	}
	encode := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	token := func(via string) string {
		input := encode(map[string]string{"alg": "EdDSA", "typ": "lifeos-id+jwt", "kid": "k"}) + "." + encode(map[string]any{
			"iss": "lifeos-auth", "aud": "lifeos-services", "sub": "00000000-0000-4000-8000-000000000001",
			"sid": via, "via": via, "role": "owner", "exp": time.Now().Add(time.Minute).Unix(),
		})
		return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(input)))
	}

	app := httpserver.New("test", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)), httpserver.WithTrustedKeys(keys))
	app.Use(RequireActor())
	var seen Actor
	app.Post("/v1/things", func(c fiber.Ctx) error {
		seen = FromRequest(c).Actor
		return c.SendStatus(http.StatusNoContent)
	})
	post := func(identityToken, actor string) int {
		t.Helper()
		seen = Actor{}
		req := httptest.NewRequest(http.MethodPost, "/v1/things", nil)
		if identityToken != "" {
			req.Header.Set(identity.Header, identityToken)
		}
		if actor != "" {
			req.Header.Set(ActorHeader, actor)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}

	// A session: the user, with or without the header, whatever it claims.
	if status := post(token("session"), ""); status != http.StatusNoContent || seen != User() {
		t.Fatalf("session without a header: %d as %+v", status, seen)
	}
	if status := post(token("session"), "dev:claude-code"); status != http.StatusNoContent || seen != User() {
		t.Fatalf("session claiming dev: %d as %+v", status, seen)
	}
	// Legacy access: as before accounts, the header says who, and is needed.
	if status := post(token("legacy"), ""); status != http.StatusBadRequest {
		t.Fatalf("legacy without a header: %d", status)
	}
	if status := post(token("legacy"), "dev:claude-code"); status != http.StatusNoContent || seen != Dev("claude-code") {
		t.Fatalf("legacy with dev: %d as %+v", status, seen)
	}
	if status := post(token("legacy"), "user"); status != http.StatusNoContent || seen != User() {
		t.Fatalf("legacy with user: %d as %+v", status, seen)
	}
	// No token at all (an internal caller): the header, as before.
	if status := post("", "system:voice-worker"); status != http.StatusNoContent || seen != System("voice-worker") {
		t.Fatalf("internal: %d as %+v", status, seen)
	}
}
