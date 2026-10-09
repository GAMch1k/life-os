package processor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// planPrompt instructs Codex to classify one Inbox item into a constrained
// JSON plan. Codex only plans; the processor validates and executes actions
// through MCP.
const planPrompt = "Classify this Inbox item. Return only JSON in this schema: {\"actions\":[{\"tool\":\"task_create|task_update|task_complete|task_reopen|calendar_create|finance_create_manual_transaction\",\"arguments\":{...}}],\"ambiguous\":boolean,\"confidence\":0.0,\"reason\":string}. Existing Task mutations require a taskQuery. It is matched as a case-insensitive substring of existing Task titles and notes, so use the shortest distinctive word or phrase that would literally appear in the Task title (for example \"chicken\" or \"Anna\"), never a paraphrase or a whole sentence. The processor searches existing Tasks with it and proceeds only when exactly one Task matches; otherwise it asks the user, so you do not need to know the existing Tasks. A statement that something has been done (for example \"I already bought the chicken\" or \"I called the dentist\") is task_complete for the Task it refers to, with taskQuery set to that distinctive word (\"chicken\", \"dentist\"). Set ambiguous true and actions to [] only when no Task can be identified from the words at all. task_update may only use taskQuery, title, notes, dueAt (RFC3339), clearDueAt. task_complete/task_reopen may only use taskQuery. Never create a Task as fallback for an uncertain existing Task. task_create arguments may only use title, notes, dueAt (RFC3339). calendar_create arguments may only use title, description, allDay, startAt/endAt (RFC3339) or startDate/endDate (ISO date). finance_create_manual_transaction may only use accountId, categoryId, amount ({amount signed integer,currency}), occurredOn (ISO date), note; only propose it if accountId, amount, currency, and date are explicit in trusted source metadata or content. Never claim an action succeeded or execute an action. If information is materially unclear or confidence is below 0.8, set ambiguous true and actions to []. Never propose calendar update or deletion. Inbox item: "

// CodexCLI is the processor's agent runtime: the machine's existing,
// authenticated Codex CLI run as a managed subprocess. It keeps the processor
// out of the API-key and direct-model credential business.
//
// Each run gets an explicit working directory, a sanitized environment (no
// inherited secrets or API keys, and no host MCP servers), its own process
// group, a deadline, graceful termination (SIGTERM, then SIGKILL after Grace)
// with cleanup of every child, and structured JSONL output from which the
// Codex session id and failure reason are read.
type CodexCLI struct {
	Command, Model, WorkDir string
	// ReasoningEffort, when set, overrides the Codex model_reasoning_effort.
	ReasoningEffort string
	// Timeout bounds one planning run (default 5 minutes).
	Timeout time.Duration
	// Grace is how long a cancelled or timed-out run may take to exit after
	// SIGTERM before its process group is killed (default 5 seconds).
	Grace time.Duration
	// Slots limits concurrent planning runs; nil means unlimited.
	Slots chan struct{}
	// Run builds the command; tests replace it.
	Run func(context.Context, string, ...string) *exec.Cmd
}

// ErrRunTimedOut marks a planning run stopped at its deadline.
var ErrRunTimedOut = errors.New("Codex run timed out")

// TokenUsage is what Codex reports a run consumed (turn.completed usage).
type TokenUsage struct {
	Input       int `json:"input"`
	CachedInput int `json:"cachedInput"`
	Output      int `json:"output"`
	Reasoning   int `json:"reasoning"`
}

// RunObserver receives lifecycle events of one planning run.
type RunObserver interface {
	// Spawned is called once the Codex process has started.
	Spawned()
	// Session is called when Codex reports its session (thread) id.
	Session(id string)
}

type observerKey struct{}

// WithRunObserver attaches an observer to the planning runs started with ctx.
func WithRunObserver(ctx context.Context, observer RunObserver) context.Context {
	return context.WithValue(ctx, observerKey{}, observer)
}

// tokenObserver is implemented by observers that record token usage.
type tokenObserver interface {
	Tokens(TokenUsage)
}

func observerFrom(ctx context.Context) RunObserver {
	if observer, ok := ctx.Value(observerKey{}).(RunObserver); ok {
		return observer
	}
	return nil
}

func (c CodexCLI) Plan(ctx context.Context, content string) (string, error) {
	if c.Slots != nil {
		select {
		case c.Slots <- struct{}{}:
			defer func() { <-c.Slots }()
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	file, err := os.CreateTemp("", "life-os-codex-plan-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	_ = file.Close()
	defer os.Remove(name)

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "-s", "read-only", "-C", c.WorkDir, "-m", c.Model, "-c", "mcp_servers={}"}
	if c.ReasoningEffort != "" {
		args = append(args, "-c", "model_reasoning_effort="+c.ReasoningEffort)
	}
	cmd := c.command(runCtx, append(args, "-o", name, planPrompt+content)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, limit: 64 * 1024}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start Codex CLI: %w", err)
	}
	observer := observerFrom(ctx)
	if observer != nil {
		observer.Spawned()
	}
	events := readRunEvents(stdout, observer)
	waitErr := cmd.Wait()
	killGroup(cmd)
	switch {
	case ctx.Err() != nil:
		return "", fmt.Errorf("Codex run cancelled: %w", ctx.Err())
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return "", fmt.Errorf("%w after %s", ErrRunTimedOut, timeout)
	case waitErr != nil:
		return "", fmt.Errorf("run Codex CLI: %w: %s", waitErr, events.failure(stderr.String()))
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// command builds a Codex CLI command with the runtime's controls: sanitized
// environment, own process group, and graceful termination on cancellation.
func (c CodexCLI) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := c.runner()(ctx, c.binary(), args...)
	cmd.Env = sanitizedEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Ask the whole group to stop; Wait kills it after WaitDelay.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = c.grace()
	return cmd
}

func (c CodexCLI) grace() time.Duration {
	if c.Grace > 0 {
		return c.Grace
	}
	return 5 * time.Second
}

// killGroup removes anything the run left behind in its process group.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// allowedEnv is everything a Codex run inherits; credentials come only from
// the Codex home (the existing ChatGPT sign-in), never from API keys.
var allowedEnv = map[string]bool{"PATH": true, "HOME": true, "USER": true, "CODEX_HOME": true, "LANG": true, "LC_ALL": true, "TZ": true, "TMPDIR": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true}

func sanitizedEnv(environ []string) []string {
	env := make([]string, 0, len(allowedEnv))
	for _, entry := range environ {
		if key, _, ok := strings.Cut(entry, "="); ok && allowedEnv[key] {
			env = append(env, entry)
		}
	}
	return env
}

// runEvents is what a run's JSONL output said about itself.
type runEvents struct {
	sessionID string
	// turnFailure is the reason of a turn.failed event.
	turnFailure string
	// errors are error events other than transport reconnects.
	errors []string
	// text is non-JSON output, for example from a CLI that predates --json.
	text strings.Builder
}

func readRunEvents(stdout io.Reader, observer RunObserver) *runEvents {
	events := &runEvents{}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Message  string `json:"message"`
			Error    struct {
				Message string `json:"message"`
			} `json:"error"`
			Usage *struct {
				Input       int `json:"input_tokens"`
				CachedInput int `json:"cached_input_tokens"`
				Output      int `json:"output_tokens"`
				Reasoning   int `json:"reasoning_output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type == "" {
			if events.text.Len() < 64*1024 {
				events.text.Write(line)
				events.text.WriteByte('\n')
			}
			continue
		}
		switch event.Type {
		case "thread.started":
			if event.ThreadID != "" && events.sessionID == "" {
				events.sessionID = event.ThreadID
				if observer != nil {
					observer.Session(event.ThreadID)
				}
			}
		case "turn.completed":
			if tokens, ok := observer.(tokenObserver); ok && event.Usage != nil {
				tokens.Tokens(TokenUsage{Input: event.Usage.Input, CachedInput: event.Usage.CachedInput, Output: event.Usage.Output, Reasoning: event.Usage.Reasoning})
			}
		case "turn.failed":
			events.turnFailure = event.Error.Message
		case "error":
			if !strings.HasPrefix(event.Message, "Reconnecting") {
				events.errors = append(events.errors, event.Message)
			}
		}
	}
	_, _ = io.Copy(io.Discard, stdout)
	return events
}

// failure is the reason a run failed: Codex's own turn.failed message, else
// its last error event, else the CLI's ERROR lines from text output.
func (e *runEvents) failure(stderr string) string {
	if e.turnFailure != "" {
		return "ERROR: " + e.turnFailure
	}
	if len(e.errors) > 0 {
		return "ERROR: " + e.errors[len(e.errors)-1]
	}
	return codexFailure([]byte(e.text.String() + stderr))
}

type limitedWriter struct {
	w     io.Writer
	limit int
	n     int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if remaining := l.limit - l.n; remaining > 0 {
		chunk := p
		if len(chunk) > remaining {
			chunk = chunk[:remaining]
		}
		written, _ := l.w.Write(chunk)
		l.n += written
	}
	return len(p), nil
}

// codexFailure keeps the CLI's own ERROR lines. The full output also echoes
// the banner and the prompt (including the user's capture), which buries the
// reason, such as a usage limit, behind text that gets truncated.
func codexFailure(out []byte) string {
	var errs []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		// "Reconnecting... n/5" lines are transport retries, not the reason.
		if strings.HasPrefix(line, "ERROR:") && !strings.HasPrefix(line, "ERROR: Reconnecting") && !seen[line] {
			seen[line] = true
			errs = append(errs, line)
		}
	}
	if len(errs) > 0 {
		return strings.Join(errs, " ")
	}
	text := []rune(strings.TrimSpace(string(out)))
	if len(text) > 500 {
		return "…" + string(text[len(text)-500:])
	}
	return string(text)
}

// LoggedIn reports whether `codex login status` succeeds for the configured
// Codex home. It reads local credentials only and uses no model quota.
func (c CodexCLI) LoggedIn(ctx context.Context) bool {
	cmd := c.command(ctx, "login", "status")
	err := cmd.Run()
	killGroup(cmd)
	return err == nil
}

func (c CodexCLI) binary() string {
	if c.Command != "" {
		return c.Command
	}
	return "codex"
}
func (c CodexCLI) runner() func(context.Context, string, ...string) *exec.Cmd {
	if c.Run != nil {
		return c.Run
	}
	return exec.CommandContext
}
