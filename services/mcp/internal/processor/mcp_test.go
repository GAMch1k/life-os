package processor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GAMch1k/life-os/pkg/activity"
)

func TestMCPClientInitializesAndCallsTool(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("content-type", "text/event-stream")
		if requests == 1 {
			w.Header().Set("Mcp-Session-Id", "session-1")
			_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n"))
			return
		}
		if r.Header.Get("Mcp-Session-Id") != "session-1" {
			t.Fatalf("request missed session header")
		}
		if requests == 3 {
			_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[]}}\n\n"))
		}
	}))
	defer server.Close()
	client := MCPClient{URL: server.URL}
	result, err := client.Call(context.Background(), "task_create", map[string]any{"title": "Call insurer"})
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `{"content":[]}` {
		t.Fatalf("result = %s", result)
	}
	if requests != 3 {
		t.Fatalf("requests = %d, want 3", requests)
	}
}

func TestMCPClientForwardsWorkflowTrace(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[]}}\n\n"))
	}))
	defer server.Close()
	causation := activity.EventID("inbox.processing.started", "attempt-1")
	ctx := activity.WithTrace(context.Background(), activity.Trace{CorrelationID: "voice-job-1", CausationID: &causation, Actor: activity.Agent("codex-inbox-processor")})
	client := MCPClient{URL: server.URL, sessionID: "session-1"}
	if _, err := client.Call(ctx, "task_create", map[string]any{"title": "Call insurer"}); err != nil {
		t.Fatal(err)
	}
	trace := activity.TraceFromHeaders(seen.Get)
	if trace.CorrelationID != "voice-job-1" || *trace.CausationID != causation || trace.Actor != activity.Agent("codex-inbox-processor") {
		t.Fatalf("trace = %+v", trace)
	}
}

func TestMCPClientStartsANewSessionAfterTheServerForgetsIt(t *testing.T) {
	sessions := 0
	live := map[string]bool{}
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		session := r.Header.Get("Mcp-Session-Id")
		if session == "" {
			sessions++
			id := fmt.Sprintf("session-%d", sessions)
			live[id] = true
			w.Header().Set("Mcp-Session-Id", id)
			_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n"))
			return
		}
		if !live[session] {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		calls = append(calls, session)
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[]}}\n\n"))
	}))
	defer server.Close()
	client := MCPClient{URL: server.URL}
	if _, err := client.Call(context.Background(), "inbox_list", nil); err != nil {
		t.Fatal(err)
	}
	// The MCP service restarts and forgets every session.
	live = map[string]bool{}
	if _, err := client.Call(context.Background(), "inbox_list", nil); err != nil {
		t.Fatalf("call after restart: %v", err)
	}
	if sessions != 2 {
		t.Fatalf("sessions = %d, want 2", sessions)
	}
	// session-1 for the first call and the initialized notification, then
	// session-2 for the notification and the retried call.
	if calls[len(calls)-1] != "session-2" {
		t.Fatalf("calls = %v", calls)
	}
}
