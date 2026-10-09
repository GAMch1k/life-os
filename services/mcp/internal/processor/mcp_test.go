package processor

import (
	"context"
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
