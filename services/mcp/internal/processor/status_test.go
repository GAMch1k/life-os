package processor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type stubPlanner struct{ err error }

func (p stubPlanner) Plan(context.Context, string) (string, error) { return "{}", p.err }

func TestCodexFailureKeepsTheCLIErrorNotThePromptEcho(t *testing.T) {
	out := []byte("WARNING: proceeding\nOpenAI Codex v0.160.0\n--------\nmodel: gpt-5.6-terra\nuser\nClassify this Inbox item ... Inbox item: buy milk\n" +
		"ERROR: You’ve hit your usage limit. Upgrade to Pro or try again at 6:22 PM.\n" +
		"ERROR: You’ve hit your usage limit. Upgrade to Pro or try again at 6:22 PM.\n")
	got := codexFailure(out)
	if got != "ERROR: You’ve hit your usage limit. Upgrade to Pro or try again at 6:22 PM." {
		t.Fatalf("codexFailure = %q", got)
	}
	long := []byte(strings.Repeat("x", 600) + "tail")
	if got := codexFailure(long); !strings.HasSuffix(got, "tail") || len([]rune(got)) != 501 {
		t.Fatalf("fallback kept %q", got)
	}
}

func TestMonitorDerivesActionableCodexState(t *testing.T) {
	m := NewMonitor("gpt-5.6-luna")
	if got := m.Snapshot().Codex.State; got != CodexUnknown {
		t.Fatalf("initial state = %s", got)
	}
	limited := m.Monitored(stubPlanner{err: errors.New("run Codex CLI: exit status 1: ERROR: You’ve hit your usage limit. try again at 6:22 PM.")})
	_, _ = limited.Plan(context.Background(), "x")
	_, _ = limited.Plan(context.Background(), "x")
	status := m.Snapshot()
	if status.Codex.State != CodexUsageLimited || status.Codex.ConsecutiveFailures != 2 || !strings.Contains(status.Codex.LastError, "6:22 PM") {
		t.Fatalf("limited = %+v", status.Codex)
	}
	_, _ = m.Monitored(stubPlanner{}).Plan(context.Background(), "x")
	if status := m.Snapshot().Codex; status.State != CodexReady || status.ConsecutiveFailures != 0 || status.LastError != "" {
		t.Fatalf("recovered = %+v", status)
	}
	_, _ = m.Monitored(stubPlanner{err: errors.New("ERROR: 401 Unauthorized")}).Plan(context.Background(), "x")
	if got := m.Snapshot().Codex.State; got != CodexUnauthenticated {
		t.Fatalf("401 state = %s", got)
	}
	_, _ = m.Monitored(stubPlanner{err: errors.New("exit status 2: segfault")}).Plan(context.Background(), "x")
	if got := m.Snapshot().Codex.State; got != CodexFailing {
		t.Fatalf("generic failure state = %s", got)
	}
	// A failed login check wins over anything learned from planning.
	_, _ = m.Monitored(stubPlanner{}).Plan(context.Background(), "x")
	m.RecordAuth(false)
	if got := m.Snapshot().Codex.State; got != CodexUnauthenticated {
		t.Fatalf("logged-out state = %s", got)
	}
}

func TestShutdownIsNotRecordedAsACodexFailure(t *testing.T) {
	m := NewMonitor("model")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = m.Monitored(stubPlanner{err: context.Canceled}).Plan(ctx, "x")
	if status := m.Snapshot().Codex; status.ConsecutiveFailures != 0 || status.LastFailedAt != nil {
		t.Fatalf("status = %+v", status)
	}
}

func TestWatchAuthRecordsChecksUntilCancelled(t *testing.T) {
	m := NewMonitor("model")
	ctx, cancel := context.WithCancel(context.Background())
	checks := make(chan struct{}, 10)
	done := make(chan struct{})
	go func() {
		m.WatchAuth(ctx, func(context.Context) bool { checks <- struct{}{}; return true }, 10*time.Millisecond)
		close(done)
	}()
	<-checks
	<-checks
	cancel()
	<-done
	status := m.Snapshot().Codex
	if status.Authenticated == nil || !*status.Authenticated || status.AuthCheckedAt == nil {
		t.Fatalf("auth status = %+v", status)
	}
}

func TestStatusEndpointServesTheSnapshot(t *testing.T) {
	m := NewMonitor("gpt-5.6-luna")
	m.RecordPoll(errors.New("mcp unreachable"))
	s := &Service{Run: func(context.Context) error { return nil }, PollInterval: time.Second, Monitor: m}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/status", nil))
	var status Status
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || response.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s, err = %v", response.Code, response.Body, err)
	}
	if status.Model != "gpt-5.6-luna" || status.Poll.LastError != "mcp unreachable" || status.Poll.LastSucceededAt != nil || status.Codex.State != CodexUnknown {
		t.Fatalf("status = %+v", status)
	}
}
