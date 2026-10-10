package activity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/httpserver"
)

func TestEventIDIsStablePerOccurrence(t *testing.T) {
	if EventID("task.created", "a") != EventID("task.created", "a") {
		t.Fatal("same occurrence produced different ids")
	}
	if EventID("task.created", "a") == EventID("task.completed", "a") || EventID("x.y", "a", "b") == EventID("x.y", "ab") {
		t.Fatal("distinct occurrences collided")
	}
}

func TestTraceRoundTripsThroughHeaders(t *testing.T) {
	causation := uuid.New()
	header := http.Header{}
	Trace{CorrelationID: "corr-1", CausationID: &causation, Actor: Agent("codex-inbox-processor")}.Apply(header)
	trace := TraceFromHeaders(header.Get)
	if trace.CorrelationID != "corr-1" || *trace.CausationID != causation || trace.Actor != Agent("codex-inbox-processor") {
		t.Fatalf("trace = %+v", trace)
	}
	if got := TraceFromHeaders(http.Header{CausationIDHeader: {"not-a-uuid"}, ActorHeader: {"admin:root"}}.Get); got.CausationID != nil || !got.Actor.IsZero() {
		t.Fatalf("untrusted headers produced %+v", got)
	}
	// The user is forwarded explicitly, so the next hop's actor gate accepts it.
	userHeader := http.Header{}
	Trace{Actor: User()}.Apply(userHeader)
	if got := userHeader.Get(ActorHeader); got != "user" {
		t.Fatalf("user trace wrote %q", got)
	}
	if got := TraceFromHeaders(userHeader.Get).Actor; got != User() {
		t.Fatalf("user trace read back as %+v", got)
	}
	unknown := http.Header{}
	Trace{}.Apply(unknown)
	if len(unknown) != 0 {
		t.Fatalf("empty trace wrote headers %v", unknown)
	}
}

func TestParseActorAcceptsOnlyExplicitActors(t *testing.T) {
	for value, want := range map[string]Actor{
		"user":                        User(),
		"agent:codex-inbox-processor": Agent("codex-inbox-processor"),
		"system:voice-worker":         System("voice-worker"),
		"dev:claude-code":             Dev("claude-code"),
		" dev:claude-code ":           Dev("claude-code"),
		"dev:e2e.smoke_1":             Dev("e2e.smoke_1"),
	} {
		if got, ok := ParseActor(value); !ok || got != want {
			t.Errorf("ParseActor(%q) = %+v, %v; want %+v", value, got, ok, want)
		}
	}
	for _, value := range []string{"", " ", "User", "user:someone", "agent:", "dev:", "Dev:claude-code", "dev:claude code", "dev:claude/code", "developer:claude-code", "admin:root", "dev:" + strings.Repeat("x", 65)} {
		if got, ok := ParseActor(value); ok || !got.IsZero() {
			t.Errorf("ParseActor(%q) = %+v, %v; want rejected", value, got, ok)
		}
	}
}

func TestRequireActorGatesWritesOnly(t *testing.T) {
	app := httpserver.New("test", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	app.Use(RequireActor())
	var seen Actor
	handler := func(c fiber.Ctx) error {
		seen = FromRequest(c).Actor
		return c.SendStatus(http.StatusNoContent)
	}
	app.Get("/v1/things", handler)
	app.Post("/v1/things", handler)
	app.Delete("/v1/things", handler)
	request := func(method, actor string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, "/v1/things", nil)
		if actor != "" {
			req.Header.Set(ActorHeader, actor)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Error struct{ Code string } `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body.Error.Code
	}
	for _, actor := range []string{"", "agent:", "nobody", "user:owner"} {
		for _, method := range []string{http.MethodPost, http.MethodDelete} {
			if status, code := request(method, actor); status != http.StatusBadRequest || code != ActorRequiredCode {
				t.Errorf("%s with actor %q = %d %q, want 400 %s", method, actor, status, code, ActorRequiredCode)
			}
		}
	}
	for actor, want := range map[string]Actor{"user": User(), "dev:claude-code": Dev("claude-code"), "agent:mcp": Agent("mcp"), "system:voice-worker": System("voice-worker")} {
		if status, _ := request(http.MethodPost, actor); status != http.StatusNoContent || seen != want {
			t.Errorf("POST with actor %q = %d as %+v", actor, status, seen)
		}
	}
	if status, _ := request(http.MethodGet, ""); status != http.StatusNoContent {
		t.Errorf("GET without actor = %d", status)
	}
	for _, path := range []string{httpserver.HealthPath, httpserver.ReadyPath} {
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Errorf("%s = %v %v", path, resp, err)
		}
	}
}

func TestDevActorRoundTripsThroughHeaders(t *testing.T) {
	header := http.Header{}
	Trace{Actor: Dev("claude-code")}.Apply(header)
	if got := header.Get(ActorHeader); got != "dev:claude-code" {
		t.Fatalf("header = %q", got)
	}
	if got := TraceFromHeaders(header.Get).Actor; got != Dev("claude-code") {
		t.Fatalf("actor = %+v", got)
	}
}

func TestEmitterRetriesAndFillsDefaults(t *testing.T) {
	var calls atomic.Int32
	var received Event
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &received)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a finished request must not drop the event
	NewEmitter(server.URL, "tasks", nil).Emit(ctx, Event{Type: "task.created", Subject: Subject{Type: "task", ID: "t"}, Outcome: Succeeded, Summary: "Task created"})
	if calls.Load() != 2 || received.Source != "tasks" || received.Actor != System("tasks") || received.OccurredAt.IsZero() || received.CorrelationID == "" {
		t.Fatalf("calls=%d received=%+v", calls.Load(), received)
	}
}

func TestNilEmitterIsNoOp(t *testing.T) {
	var emitter *Emitter
	emitter.Emit(context.Background(), Event{Type: "task.created"})
	NewEmitter("", "tasks", nil).Emit(context.Background(), Event{Type: "task.created"})
}

func TestPreviewCollapsesAndTruncates(t *testing.T) {
	if got := Preview("  buy\n milk  ", 20); got != "buy milk" {
		t.Fatalf("got %q", got)
	}
	if got := Preview("abcdef", 4); got != "abc…" {
		t.Fatalf("got %q", got)
	}
}
