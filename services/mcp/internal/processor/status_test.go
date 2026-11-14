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

	"github.com/GAMch1k/life-os/pkg/identity"
	"github.com/GAMch1k/life-os/pkg/identity/identitytest"
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
	unauthenticated := []byte("ERROR: Reconnecting... 1/5\nERROR: Reconnecting... 2/5\nERROR: unexpected status 401 Unauthorized: Missing bearer or basic authentication in header\n")
	if got := codexFailure(unauthenticated); got != "ERROR: unexpected status 401 Unauthorized: Missing bearer or basic authentication in header" {
		t.Fatalf("codexFailure = %q", got)
	} else if classifyCodexError(got) != CodexUnauthenticated {
		t.Fatalf("401 classified as %s", classifyCodexError(got))
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

// The AI subscription's plan and usage are the server's admins'
// (docs/architecture/multi-user.md#roles-and-checks): a member's identity
// token is refused; legacy access and the Dashboard without a token are not.
func TestStatusEndpointIsForAdmins(t *testing.T) {
	signer := identitytest.New(t)
	s := &Service{Run: func(context.Context) error { return nil }, PollInterval: time.Second,
		Monitor: NewMonitor("model"), Verifier: identity.NewVerifier(signer.Keys, nil)}
	member := "5c1d7a8e-0b7f-4d2a-9b1e-2f6a3c4d5e6f"
	for _, tc := range []struct {
		name, token string
		code        int
	}{
		{"the Dashboard (no token)", "", http.StatusOK},
		{"the owner", signer.Token("", "owner", "session"), http.StatusOK},
		{"legacy access", signer.Token("", "owner", "legacy"), http.StatusOK},
		{"an admin", signer.Token(member, "admin", "session"), http.StatusOK},
		{"a member", signer.Token(member, "member", "session"), http.StatusForbidden},
		{"a forged token", "forged.token.value", http.StatusUnauthorized},
	} {
		request := httptest.NewRequest(http.MethodGet, "/status", nil)
		if tc.token != "" {
			request.Header.Set(identity.Header, tc.token)
		}
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != tc.code {
			t.Errorf("%s: %d %s, want %d", tc.name, response.Code, response.Body, tc.code)
		}
	}
	// Without trusted keys, any token is refused, never let through.
	open := &Service{Run: func(context.Context) error { return nil }, PollInterval: time.Second, Monitor: NewMonitor("model")}
	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	request.Header.Set(identity.Header, signer.Token("", "owner", "session"))
	response := httptest.NewRecorder()
	open.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Errorf("without keys: %d", response.Code)
	}
}

func TestMonitorFollowsTheRuntime(t *testing.T) {
	m := NewMonitor("model")
	m.ConfigureRuntime("processor-a", "/usr/local/bin/codex", 5*time.Minute, 1)
	for _, state := range []string{RuntimeSpawning, RuntimeRunning} {
		m.RecordRuntime(RuntimeEvent{ItemID: "item-1", AttemptID: "attempt-1", State: state})
	}
	m.RecordRuntime(RuntimeEvent{ItemID: "item-1", AttemptID: "attempt-1", State: RuntimeRunning, SessionID: "thread-1"})
	runtime := m.Snapshot().Runtime
	if runtime.Current == nil || runtime.Current.State != RuntimeRunning || runtime.Current.SessionID != "thread-1" || runtime.TimeoutSeconds != 300 || runtime.Instance != "processor-a" {
		t.Fatalf("runtime = %+v", runtime)
	}
	m.RecordRuntime(RuntimeEvent{ItemID: "item-1", AttemptID: "attempt-1", State: RuntimeTimedOut})
	m.RecordRuntime(RuntimeEvent{ItemID: "item-2", AttemptID: "attempt-2", State: RuntimeCompleted})
	m.RecordReconcile(2, nil)
	runtime = m.Snapshot().Runtime
	if runtime.Current != nil || runtime.TimedOut != 1 || runtime.Completed != 1 || runtime.Recovered != 2 || runtime.LastReconcileAt == nil {
		t.Fatalf("runtime = %+v", runtime)
	}
}

func TestMonitorSumsTokenUsage(t *testing.T) {
	m := NewMonitor("gpt-6-luna")
	m.SetReasoningEffort("low")
	m.RecordTokens(TokenUsage{Input: 100, CachedInput: 60, Output: 10, Reasoning: 2})
	m.RecordTokens(TokenUsage{Input: 50, Output: 5})
	status := m.Snapshot()
	if status.ReasoningEffort != "low" || status.Runtime.Tokens.Runs != 2 || status.Runtime.Tokens.Input != 150 || status.Runtime.Tokens.Reasoning != 2 || status.Runtime.LastRunTokens.Output != 5 {
		t.Fatalf("status = %+v", status)
	}
}

func TestConfigProfileAndEffortValidation(t *testing.T) {
	t.Setenv("LIFE_OS_CODEX_MODEL", "gpt-6-luna")
	t.Setenv("LIFE_OS_CODEX_REASONING_EFFORT", "none")
	cfg, err := LoadConfig()
	if err != nil || cfg.Profile() != "gpt-6-luna/none" {
		t.Fatalf("profile = %q, err = %v", cfg.Profile(), err)
	}
	t.Setenv("LIFE_OS_CODEX_REASONING_EFFORT", "")
	if cfg, _ := LoadConfig(); cfg.Profile() != "gpt-6-luna" {
		t.Fatalf("profile without effort = %q", cfg.Profile())
	}
	t.Setenv("LIFE_OS_CODEX_REASONING_EFFORT", "low; rm -rf /")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("an invalid effort was accepted")
	}
}

func TestStatusNamesTheActiveProvider(t *testing.T) {
	for provider, name := range map[string]string{ProviderCodex: "Codex", ProviderClaude: "Claude Code"} {
		m := NewMonitor("model")
		m.SetProvider(provider)
		raw, err := json.Marshal(m.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		var status struct {
			Provider     string `json:"provider"`
			ProviderName string `json:"providerName"`
		}
		if err := json.Unmarshal(raw, &status); err != nil || status.Provider != provider || status.ProviderName != name {
			t.Fatalf("status = %s", raw)
		}
	}
}
