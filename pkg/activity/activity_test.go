package activity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
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
	if got := TraceFromHeaders(http.Header{CausationIDHeader: {"not-a-uuid"}, ActorHeader: {"admin:root"}}.Get); got.CausationID != nil || got.Actor != User() {
		t.Fatalf("untrusted headers produced %+v", got)
	}
	userHeader := http.Header{}
	Trace{Actor: User()}.Apply(userHeader)
	if len(userHeader) != 0 {
		t.Fatalf("user trace wrote headers %v", userHeader)
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
	if calls.Load() != 2 || received.Source != "tasks" || received.Actor != User() || received.OccurredAt.IsZero() || received.CorrelationID == "" {
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
