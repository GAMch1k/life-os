package main

import (
	"context"
	"encoding/json"
	"errors"
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

// auditedEvents runs audited with a tool that answers out (or fails with
// err) and returns the events it recorded.
func auditedEvents(t *testing.T, out Output, err error) []activity.Event {
	t.Helper()
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
	_, _, _ = audited(context.Background(), activity.NewEmitter(sink.URL, "mcp", nil), &mcp.CallToolRequest{}, "recipe_import", map[string]any{"url": "https://example.com"}, func(activity.Trace) (*mcp.CallToolResult, Output, error) {
		return nil, out, err
	})
	if len(events) != 2 || events[0].Type != "mcp.action.requested" {
		t.Fatalf("events = %+v", events)
	}
	return events
}

func TestAuditedRecordsWhatTheToolReallyDid(t *testing.T) {
	// Nothing saved: not a success, with the reason.
	done := auditedEvents(t, Output{Data: map[string]any{"saved": false, "method": "preview", "reason": "Instagram posts can't be read directly: paste the caption to fill in the ingredients and steps."}}, nil)[1]
	if done.Type != "mcp.action.not_done" || done.Outcome != activity.Failed || done.Data["reason"] != "Instagram posts can't be read directly: paste the caption to fill in the ingredients and steps." ||
		done.Summary != "recipe_import not done: Instagram posts can't be read directly: paste the caption to fill in the ingredients and steps." || done.Data["resultId"] != nil {
		t.Fatalf("not done = %+v", done)
	}
	// Without a reason, a plain one.
	if done := auditedEvents(t, Output{Data: map[string]any{"saved": false}}, nil)[1]; done.Type != "mcp.action.not_done" || done.Data["reason"] != "nothing was saved" {
		t.Fatalf("not done = %+v", done)
	}
	// A saved recipe names it by its recipeId.
	done = auditedEvents(t, Output{Data: map[string]any{"saved": true, "method": "structured", "recipeId": "recipe-1", "recipe": map[string]any{"id": "recipe-1", "title": "Gulasch"}}}, nil)[1]
	if done.Type != "mcp.action.completed" || done.Outcome != activity.Succeeded || done.Data["resultId"] != "recipe-1" || done.Data["alreadySaved"] != nil {
		t.Fatalf("saved = %+v", done)
	}
	// One saved before is done, and says so.
	done = auditedEvents(t, Output{Data: map[string]any{"saved": true, "alreadySaved": true, "recipeId": "recipe-2"}}, nil)[1]
	if done.Type != "mcp.action.completed" || done.Data["resultId"] != "recipe-2" || done.Data["alreadySaved"] != true || done.Summary != "Completed recipe_import: already saved" {
		t.Fatalf("already saved = %+v", done)
	}
	// A nested entity's id; an id wins over the rest.
	if done := auditedEvents(t, Output{Data: map[string]any{"created": false, "staple": map[string]any{"id": "staple-1"}}}, nil)[1]; done.Data["resultId"] != "staple-1" {
		t.Fatalf("staple = %+v", done)
	}
	if done := auditedEvents(t, Output{Data: map[string]any{"id": "entry-1", "recipeId": "recipe-1"}}, nil)[1]; done.Data["resultId"] != "entry-1" {
		t.Fatalf("meal = %+v", done)
	}
	// Several different ids are all named.
	done = auditedEvents(t, Output{Data: map[string]any{"taskId": "task-1", "eventId": "event-1"}}, nil)[1]
	if ids, _ := done.Data["resultIds"].(map[string]any); done.Data["resultId"] != nil || ids["taskId"] != "task-1" || ids["eventId"] != "event-1" {
		t.Fatalf("several = %+v", done)
	}
	// A list names nothing; a failure is a failure.
	if done := auditedEvents(t, Output{Data: map[string]any{"items": []any{map[string]any{"id": "item-1"}}}}, nil)[1]; done.Data["resultId"] != nil || done.Data["resultIds"] != nil {
		t.Fatalf("list = %+v", done)
	}
	if done := auditedEvents(t, Output{}, errors.New("recipe-import is down"))[1]; done.Type != "mcp.action.failed" || done.Outcome != activity.Failed || done.Data["error"] != "recipe-import is down" {
		t.Fatalf("failed = %+v", done)
	}
}
