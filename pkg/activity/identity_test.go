package activity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

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
	tokenWith := func(via string, extra map[string]any) string {
		claims := map[string]any{
			"iss": "lifeos-auth", "aud": "lifeos-services", "sub": "00000000-0000-4000-8000-000000000001",
			"sid": via, "via": via, "role": "owner", "exp": time.Now().Add(time.Minute).Unix(),
		}
		for key, value := range extra {
			claims[key] = value
		}
		input := encode(map[string]string{"alg": "EdDSA", "typ": "lifeos-id+jwt", "kid": "k"}) + "." + encode(claims)
		return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(input)))
	}
	token := func(via string) string { return tokenWith(via, nil) }

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
	// A paired device: the user (P21-06).
	if status := post(token("device"), "dev:claude-code"); status != http.StatusNoContent || seen != User() {
		t.Fatalf("device: %d as %+v", status, seen)
	}
	// A personal token: the user; marked as a development tool, dev:<name>.
	if status := post(token("token"), ""); status != http.StatusNoContent || seen != User() {
		t.Fatalf("token: %d as %+v", status, seen)
	}
	if status := post(tokenWith("token", map[string]any{"dev": "claude-code"}), "user"); status != http.StatusNoContent || seen != Dev("claude-code") {
		t.Fatalf("dev token: %d as %+v", status, seen)
	}
	// An MCP client's grant: the AI over MCP, for the user.
	if status := post(token("mcp"), "user"); status != http.StatusNoContent || seen != Agent("mcp") {
		t.Fatalf("mcp: %d as %+v", status, seen)
	}
	// No token at all (an internal caller): the header, as before.
	if status := post("", "system:voice-worker"); status != http.StatusNoContent || seen != System("voice-worker") {
		t.Fatalf("internal: %d as %+v", status, seen)
	}
}

// The person an event is for travels with it as actorUserId: the user of a
// session or of legacy access, never a service's own work.
func TestEmitterRecordsTheActorUser(t *testing.T) {
	var received []Event
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var event Event
		_ = json.Unmarshal(raw, &event)
		received = append(received, event)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	emitter := NewEmitter(server.URL, "tasks", nil)
	first := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	event := Event{Type: "task.created", Subject: Subject{Type: "task", ID: "t"}, Outcome: Succeeded, Summary: "Task created"}

	emitter.Emit(identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: first, Via: identity.ViaSession}), event)
	emitter.Emit(identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: first, Via: identity.ViaLegacy}), event)
	emitter.Emit(identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindService}), event)
	emitter.Emit(context.Background(), event)
	other := uuid.New()
	explicit := event
	explicit.ActorUserID = &other
	emitter.Emit(identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: first}), explicit)

	if len(received) != 5 {
		t.Fatalf("received %d events", len(received))
	}
	want := []*uuid.UUID{&first, &first, nil, nil, &other}
	for i, event := range received {
		got := event.ActorUserID
		if (got == nil) != (want[i] == nil) || (got != nil && *got != *want[i]) {
			t.Errorf("event %d: actorUserId %v, want %v", i, got, want[i])
		}
	}
	raw, _ := json.Marshal(Event{Type: "x.y"})
	if strings.Contains(string(raw), "actorUserId") {
		t.Errorf("an event without a person carries actorUserId: %s", raw)
	}
}
