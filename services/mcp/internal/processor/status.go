package processor

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Codex runtime states reported on the processor's private /status endpoint.
const (
	CodexReady           = "ready"
	CodexUsageLimited    = "usage_limited"
	CodexUnauthenticated = "unauthenticated"
	CodexFailing         = "failing"
	CodexUnknown         = "unknown"
)

// Status is what this processor process has observed since it started. It is
// operational state like /ready, not domain data: it lives in memory and
// resets on restart.
type Status struct {
	Model     string      `json:"model"`
	StartedAt time.Time   `json:"startedAt"`
	Poll      PollStatus  `json:"poll"`
	Codex     CodexStatus `json:"codex"`
	Usage     UsageStatus `json:"usage"`
}

// UsageStatus is the latest account usage read from the Codex CLI. Usage is
// the last successful read; Error is set when the latest read failed.
type UsageStatus struct {
	Usage *Usage `json:"current"`
	Error string `json:"error,omitempty"`
}

type PollStatus struct {
	LastAt          *time.Time `json:"lastAt"`
	LastSucceededAt *time.Time `json:"lastSucceededAt"`
	// LastError is set while the latest poll pass failed (MCP/Inbox unreachable).
	LastError string `json:"lastError,omitempty"`
}

type CodexStatus struct {
	State string `json:"state"`
	// Authenticated is the result of the latest `codex login status` check;
	// null until the first check completes.
	Authenticated       *bool      `json:"authenticated"`
	AuthCheckedAt       *time.Time `json:"authCheckedAt"`
	LastSucceededAt     *time.Time `json:"lastSucceededAt"`
	LastFailedAt        *time.Time `json:"lastFailedAt"`
	LastError           string     `json:"lastError,omitempty"`
	ConsecutiveFailures int        `json:"consecutiveFailures"`
}

// Monitor records poll passes, Codex planning outcomes, and authentication
// checks for the status endpoint.
type Monitor struct {
	mu     sync.RWMutex
	status Status
	now    func() time.Time
}

func NewMonitor(model string) *Monitor {
	m := &Monitor{now: time.Now}
	m.status = Status{Model: model, StartedAt: m.now().UTC()}
	return m
}

func (m *Monitor) stamp() *time.Time {
	at := m.now().UTC()
	return &at
}

// RecordPoll records the outcome of one processor poll pass.
func (m *Monitor) RecordPoll(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	at := m.stamp()
	m.status.Poll.LastAt = at
	if err != nil {
		m.status.Poll.LastError = err.Error()
		return
	}
	m.status.Poll.LastSucceededAt, m.status.Poll.LastError = at, ""
}

// RecordPlan records the outcome of one Codex planning run.
func (m *Monitor) RecordPlan(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	at := m.stamp()
	if err != nil {
		m.status.Codex.LastFailedAt, m.status.Codex.LastError = at, err.Error()
		m.status.Codex.ConsecutiveFailures++
		return
	}
	m.status.Codex.LastSucceededAt, m.status.Codex.LastError = at, ""
	m.status.Codex.ConsecutiveFailures = 0
}

// RecordAuth records the result of a `codex login status` check.
func (m *Monitor) RecordAuth(authenticated bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Codex.Authenticated, m.status.Codex.AuthCheckedAt = &authenticated, m.stamp()
}

// RecordUsage records the result of a Codex usage read.
func (m *Monitor) RecordUsage(usage Usage, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.status.Usage.Error = err.Error()
		return
	}
	m.status.Usage = UsageStatus{Usage: &usage}
}

// WatchAuth checks authentication now and then on every interval until ctx ends.
func (m *Monitor) WatchAuth(ctx context.Context, check func(context.Context) bool, every time.Duration) {
	repeat(ctx, every, func(ctx context.Context) {
		if ok := check(ctx); ctx.Err() == nil {
			m.RecordAuth(ok)
		}
	})
}

// WatchUsage reads account usage now and then on every interval until ctx ends.
func (m *Monitor) WatchUsage(ctx context.Context, read func(context.Context) (Usage, error), every time.Duration) {
	repeat(ctx, every, func(ctx context.Context) {
		if usage, err := read(ctx); ctx.Err() == nil || err == nil {
			m.RecordUsage(usage, err)
		}
	})
}

func repeat(ctx context.Context, every time.Duration, check func(context.Context)) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		check(checkCtx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Snapshot returns the current status with the derived Codex state.
func (m *Monitor) Snapshot() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	status := m.status
	status.Codex.State = codexState(status.Codex)
	return status
}

func codexState(codex CodexStatus) string {
	if codex.Authenticated != nil && !*codex.Authenticated {
		return CodexUnauthenticated
	}
	if codex.ConsecutiveFailures > 0 {
		return classifyCodexError(codex.LastError)
	}
	if codex.LastSucceededAt != nil {
		return CodexReady
	}
	return CodexUnknown
}

// classifyCodexError maps the CLI's error text to an actionable state.
func classifyCodexError(message string) string {
	text := strings.ToLower(message)
	switch {
	case strings.Contains(text, "usage limit"), strings.Contains(text, "rate limit"), strings.Contains(text, "429"):
		return CodexUsageLimited
	case strings.Contains(text, "not logged in"), strings.Contains(text, "401"), strings.Contains(text, "unauthorized"), strings.Contains(text, "codex login"):
		return CodexUnauthenticated
	default:
		return CodexFailing
	}
}

// Monitored wraps a planner so every planning run is recorded. A run cut short
// by shutdown is not an outcome.
func (m *Monitor) Monitored(p planner) planner { return monitoredPlanner{inner: p, monitor: m} }

type monitoredPlanner struct {
	inner   planner
	monitor *Monitor
}

func (p monitoredPlanner) Plan(ctx context.Context, content string) (string, error) {
	out, err := p.inner.Plan(ctx, content)
	if ctx.Err() == nil {
		p.monitor.RecordPlan(err)
	}
	return out, err
}
