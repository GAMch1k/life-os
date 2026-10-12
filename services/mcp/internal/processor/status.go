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
	Model string `json:"model"`
	// ReasoningEffort is the configured model_reasoning_effort ("" = Codex
	// default).
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	// TimeZone is the configured timezone relative dates are resolved in
	// when a capture does not name its own (sourceMetadata.timeZone).
	TimeZone  string             `json:"timeZone,omitempty"`
	StartedAt time.Time          `json:"startedAt"`
	Poll      PollStatus         `json:"poll"`
	Codex     CodexStatus        `json:"codex"`
	Usage     UsageStatus        `json:"usage"`
	Runtime   AgentRuntimeStatus `json:"runtime"`
	// Ideas is the recipe ideas job: when it runs next and what the last
	// run did; absent when the job is not configured.
	Ideas *IdeasStatus `json:"ideas,omitempty"`
	// Categories is the AI categories job: whether it is on, what waits,
	// and what the last batch did; absent when the job is not configured.
	Categories *CategoriesStatus `json:"categories,omitempty"`
}

// AgentRuntimeStatus describes the Codex runtime: its configuration, the run
// in progress, and what happened to earlier runs since the processor started.
type AgentRuntimeStatus struct {
	Instance       string `json:"instance"`
	Binary         string `json:"binary"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
	MaxConcurrency int    `json:"maxConcurrency"`
	// Current is the attempt being planned or executed right now.
	Current   *RuntimeRun `json:"current"`
	Completed int         `json:"completed"`
	Failed    int         `json:"failed"`
	TimedOut  int         `json:"timedOut"`
	Cancelled int         `json:"cancelled"`
	// Recovered counts abandoned attempts this processor reconciled.
	Recovered int `json:"recovered"`
	// Tokens sums the usage Codex reported for planning runs since start;
	// LastRunTokens is the latest run's.
	Tokens             TokenTotals `json:"tokens"`
	LastRunTokens      *TokenUsage `json:"lastRunTokens"`
	LastReconcileAt    *time.Time  `json:"lastReconcileAt"`
	LastReconcileError string      `json:"lastReconcileError,omitempty"`
}

type TokenTotals struct {
	Runs int `json:"runs"`
	TokenUsage
}

type RuntimeRun struct {
	ItemID    string    `json:"itemId"`
	AttemptID string    `json:"attemptId"`
	State     string    `json:"state"`
	SessionID string    `json:"sessionId,omitempty"`
	Since     time.Time `json:"since"`
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

// ConfigureRuntime records the runtime's static configuration.
func (m *Monitor) ConfigureRuntime(instance, binary string, timeout time.Duration, maxConcurrency int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Runtime.Instance, m.status.Runtime.Binary = instance, binary
	m.status.Runtime.TimeoutSeconds, m.status.Runtime.MaxConcurrency = int(timeout.Seconds()), maxConcurrency
}

// RecordRuntime follows an attempt's runtime states.
func (m *Monitor) RecordRuntime(event RuntimeEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime := &m.status.Runtime
	current := runtime.Current
	if current == nil || current.AttemptID != event.AttemptID {
		current = &RuntimeRun{ItemID: event.ItemID, AttemptID: event.AttemptID, Since: m.now().UTC()}
	}
	if current.State == event.State && current.SessionID == event.SessionID && runtime.Current == current {
		return // a heartbeat
	}
	current.State = event.State
	if event.SessionID != "" {
		current.SessionID = event.SessionID
	}
	runtime.Current = current
	switch event.State {
	case RuntimeCompleted:
		runtime.Completed++
	case RuntimeFailed:
		runtime.Failed++
	case RuntimeTimedOut:
		runtime.TimedOut++
	case RuntimeCancelled:
		runtime.Cancelled++
	default:
		return
	}
	// The planning run is over; the attempt's actions are quick.
	runtime.Current = nil
}

// SetReasoningEffort records the configured reasoning effort.
func (m *Monitor) SetReasoningEffort(effort string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.ReasoningEffort = effort
}

// SetTimeZone records the planner's configured timezone, which captures
// without their own timezone are planned in.
func (m *Monitor) SetTimeZone(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.TimeZone = name
}

// SetIdeas records the recipe ideas job's status.
func (m *Monitor) SetIdeas(status IdeasStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Ideas = &status
}

// SetCategories records the AI categories job's status.
func (m *Monitor) SetCategories(status CategoriesStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Categories = &status
}

// RecordTokens adds one planning run's token usage.
func (m *Monitor) RecordTokens(usage TokenUsage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	totals := &m.status.Runtime.Tokens
	totals.Runs++
	totals.Input += usage.Input
	totals.CachedInput += usage.CachedInput
	totals.Output += usage.Output
	totals.Reasoning += usage.Reasoning
	m.status.Runtime.LastRunTokens = &usage
}

// RecordReconcile records a reconciliation pass.
func (m *Monitor) RecordReconcile(recovered int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Runtime.LastReconcileAt = m.stamp()
	m.status.Runtime.Recovered += recovered
	m.status.Runtime.LastReconcileError = ""
	if err != nil {
		m.status.Runtime.LastReconcileError = err.Error()
	}
}

// WatchReconcile reconciles abandoned attempts now and then on every interval.
func (m *Monitor) WatchReconcile(ctx context.Context, reconcile func(context.Context) (int, error), every time.Duration) {
	repeat(ctx, every, func(ctx context.Context) {
		if recovered, err := reconcile(ctx); ctx.Err() == nil {
			m.RecordReconcile(recovered, err)
		}
	})
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

// MonitoredIdeas records the ideas job's Codex runs like planning runs: their
// outcome in the Codex state (a usage limit hit by the job shows) and their
// token usage.
func (m *Monitor) MonitoredIdeas(g ideaGenerator) ideaGenerator {
	return monitoredIdeas{inner: g, monitor: m}
}

type monitoredIdeas struct {
	inner   ideaGenerator
	monitor *Monitor
}

func (g monitoredIdeas) Ideas(ctx context.Context, prompt, schema, webSearch string) (string, error) {
	out, err := g.inner.Ideas(WithRunObserver(ctx, tokenCounter{monitor: g.monitor}), prompt, schema, webSearch)
	if ctx.Err() == nil {
		g.monitor.RecordPlan(err)
	}
	return out, err
}

// MonitoredCategories records the AI categories job's Codex runs like
// planning runs: their outcome in the Codex state and their token usage.
func (m *Monitor) MonitoredCategories(c categoryChooser) categoryChooser {
	return monitoredCategories{inner: c, monitor: m}
}

type monitoredCategories struct {
	inner   categoryChooser
	monitor *Monitor
}

func (c monitoredCategories) Categories(ctx context.Context, prompt, schema string) (string, error) {
	out, err := c.inner.Categories(WithRunObserver(ctx, tokenCounter{monitor: c.monitor}), prompt, schema)
	if ctx.Err() == nil {
		c.monitor.RecordPlan(err)
	}
	return out, err
}

// tokenCounter observes a Codex run only for its token usage.
type tokenCounter struct{ monitor *Monitor }

func (tokenCounter) Spawned()                  {}
func (tokenCounter) Session(string)            {}
func (c tokenCounter) Tokens(usage TokenUsage) { c.monitor.RecordTokens(usage) }
