package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GAMch1k/life-os/pkg/activity"
)

func TestPostForwardsActionIdempotencyKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Idempotency-Key"); got != "action-stable-key" {
			t.Fatalf("Idempotency-Key = %q", got)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"id":"original-row"}`))
	}))
	defer server.Close()

	_, first, err := post(context.Background(), server.Client(), activity.Trace{}, server.URL, "/v1/tasks", taskAction{Title: "Only once"}, "action-stable-key")
	if err != nil || first.Data["id"] != "original-row" {
		t.Fatalf("first post = %#v, err = %v", first, err)
	}
	_, second, err := post(context.Background(), server.Client(), activity.Trace{}, server.URL, "/v1/tasks", taskAction{Title: "Only once"}, "action-stable-key")
	if err != nil || second.Data["id"] != "original-row" {
		t.Fatalf("replayed post = %#v, err = %v", second, err)
	}
}

func TestAuditedMutationRecordsRequestAndCompletion(t *testing.T) {
	var mu sync.Mutex
	var events []activity.Event
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var event activity.Event
		_ = json.Unmarshal(raw, &event)
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer sink.Close()
	var serviceHeaders http.Header
	tasks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serviceHeaders = r.Header.Clone()
		_, _ = w.Write([]byte(`{"id":"task-1"}`))
	}))
	defer tasks.Close()

	started := activity.EventID("inbox.processing.started", "attempt-1")
	incoming := http.Header{}
	activity.Trace{CorrelationID: "corr-1", CausationID: &started, Actor: activity.Agent("codex-inbox-processor")}.Apply(incoming)
	req := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: incoming}}
	emitter := activity.NewEmitter(sink.URL, "mcp", nil)
	_, out, err := audited(context.Background(), emitter, req, "task_create", taskAction{Title: "Buy milk"}, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
		return post(context.Background(), tasks.Client(), trace, tasks.URL, "/v1/tasks", taskAction{Title: "Buy milk"})
	})
	if err != nil || out.Data["id"] != "task-1" {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if len(events) != 2 || events[0].Type != "mcp.action.requested" || events[1].Type != "mcp.action.completed" {
		t.Fatalf("events = %+v", events)
	}
	requested := events[0]
	if requested.CorrelationID != "corr-1" || *requested.CausationID != started || requested.Actor != activity.Agent("codex-inbox-processor") {
		t.Fatalf("requested = %+v", requested)
	}
	if *events[1].CausationID != requested.ID || events[1].Data["resultId"] != "task-1" {
		t.Fatalf("completed = %+v", events[1])
	}
	forwarded := activity.TraceFromHeaders(serviceHeaders.Get)
	if forwarded.CorrelationID != "corr-1" || *forwarded.CausationID != requested.ID || forwarded.Actor != activity.Agent("codex-inbox-processor") {
		t.Fatalf("service saw %+v", forwarded)
	}
}

func TestUnnamedMCPCallerIsAnAgent(t *testing.T) {
	trace := traceOf(&mcp.CallToolRequest{})
	if trace.Actor != activity.Agent("mcp") || trace.CorrelationID == "" {
		t.Fatalf("trace = %+v", trace)
	}
	// MCP is itself the agent: a caller claiming to be the user is still the
	// agent, while a development tool keeps its own actor.
	for claim, want := range map[string]activity.Actor{"user": activity.Agent("mcp"), "nobody": activity.Agent("mcp"), "dev:claude-code": activity.Dev("claude-code")} {
		header := http.Header{}
		header.Set(activity.ActorHeader, claim)
		if got := traceOf(&mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: header}}).Actor; got != want {
			t.Errorf("caller %q recorded as %+v, want %+v", claim, got, want)
		}
	}
}

func TestAttemptUpdateForwardsTheRetrySchedule(t *testing.T) {
	plain := attemptUpdate(inboxAction{Status: "processed"})
	if _, ok := plain["nextRetryAt"]; ok {
		t.Fatalf("plain update = %#v", plain)
	}
	retry := attemptUpdate(inboxAction{Status: "retryable", NextRetryAt: "2026-10-09T23:00:00Z", RetryReason: "usage_limited"})
	if retry["nextRetryAt"] != "2026-10-09T23:00:00Z" || retry["retryReason"] != "usage_limited" {
		t.Fatalf("retry update = %#v", retry)
	}
}
