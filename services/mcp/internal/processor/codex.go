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
// through MCP. Each run appends the owner's local time (timeContext) and then
// the item, so the fixed instructions stay a stable prompt prefix.
const planPrompt = "Classify this Inbox item. Return only JSON in this schema: {\"actions\":[{\"tool\":\"task_create|task_update|task_complete|task_reopen|calendar_create|calendar_update|calendar_cancel|finance_create_manual_transaction\",\"arguments\":{...}}],\"ambiguous\":boolean,\"confidence\":0.0,\"reason\":string}. Existing Task mutations require a taskQuery and should add taskQueryAlternatives. Each term is matched as a case-insensitive substring of existing Task titles and notes, so use the shortest distinctive word or phrase that would literally appear in the Task title (for example \"chicken\" or \"Anna\"), never a paraphrase or a whole sentence. The owner writes Tasks in English, Russian, and German, and the title may be in another language or word form than the item, so taskQueryAlternatives is an array of up to 6 more terms for the same thing: its translations into the other two languages and word stems that also match inflected forms, long enough to stay distinctive (\"куриц\", not \"кур\"). For \"I already bought the chicken\": taskQuery \"chicken\", taskQueryAlternatives [\"куриц\", \"Hähnchen\", \"Huhn\"]; for \"Я уже вынес мусор\": \"мусор\", [\"trash\", \"garbage\", \"Müll\"]. Alternatives name the same thing as taskQuery, never a more generic word or a fragment that other Tasks would share; omit them when the item quotes the exact Task title. The processor searches open Tasks (completed ones for task_reopen) with every term and proceeds only when exactly one Task matches in total; otherwise it asks the user, so you do not need to know the existing Tasks. A statement that something has been done (for example \"I already bought the chicken\" or \"I called the dentist\") is task_complete for the Task it refers to, with taskQuery set to that distinctive word (\"chicken\", \"dentist\"). Set ambiguous true and actions to [] only when no Task can be identified from the words at all. task_update may only use taskQuery, taskQueryAlternatives, title, notes, dueAt (RFC3339), clearDueAt. task_complete/task_reopen may only use taskQuery, taskQueryAlternatives. Never create a Task as fallback for an uncertain existing Task. task_create arguments may only use title, notes, dueAt (RFC3339). calendar_create arguments may only use title, description, location (where it takes place, such as \"Dr. Weber\" or an address; keep it out of the title), allDay, startAt/endAt (RFC3339) or startDate/endDate (ISO date; give the end only when the item states it or a duration, otherwise the processor makes a timed event one hour and an all-day event one day), reminders (only when the item asks for one: an array of {\"kind\":\"relative\",\"minutesBefore\":30} or {\"kind\":\"localTime\",\"daysBefore\":1,\"localTime\":\"19:00\"}; they are added to the owner's default reminders), and priority (only when the item says how important it is: \"high\" for important or urgent, such as \"wichtig\" or \"важно\", \"low\" for not important or low priority; otherwise leave it out). Tasks are to-dos (call, buy, renew, pay, …) with an optional due date; Calendar events are appointments and meetings with a time and maybe a place. Moving or rescheduling something changes a Task when it reads as a to-do (\"Move the electrician call to Friday\" is task_update) and an event when it reads as an appointment or meeting. Changing or moving an existing event is calendar_update, and cancelling, deleting, or removing one is calendar_cancel. A request to be reminded of, or to add a reminder to, an event that already exists (even when the item restates the event) is calendar_update with eventQuery and reminders, not calendar_create. Both require an eventQuery and should add eventQueryAlternatives, chosen exactly like taskQuery and taskQueryAlternatives but matched against event titles, locations, and notes: for \"Move the dentist appointment to Monday at 9\": eventQuery \"dentist\", eventQueryAlternatives [\"Zahnarzt\", \"стоматолог\", \"зубн\"]. The processor searches active events from today on and changes one only when exactly one matches, otherwise it asks the owner, so you do not need to know the existing events; the owner approves every cancellation. calendar_update may only use eventQuery, eventQueryAlternatives, and the fields that change: title, description, location, allDay, startAt/endAt or startDate/endDate, reminders (added to the event's own; to replace them, also name reminders in clear), priority, and clear (an array naming description, location, or reminders to remove). To reschedule, give the new start, and the end only when the item states it or a duration; the processor otherwise keeps the event's length. calendar_cancel may only use eventQuery, eventQueryAlternatives, and onDate (the ISO date of the day the item names for the event, such as the capture's date for \"Cancel gym today\"; leave it out when it names no day): for a repeating event the processor cancels only its occurrence on that day, else its next one. Never create an event as a fallback for an uncertain existing event. Repeating events are set up only in the Calendar: for an item asking for a new repeating event (\"every Saturday\"), set ambiguous true and actions to [] with reason \"Recurring events are created in the Calendar.\". finance_create_manual_transaction records cash the owner spent or received, such as \"spent 12 euros at the bakery\" or \"потратил 20 евро наличкой в Lidl\"; propose it only when the item states the amount. It may only use amount ({\"amount\": signed integer in cents, negative for money spent, such as -1200 for 12 euros spent, \"currency\": three-letter code, EUR when the item names none}), counterparty (who was paid or who paid, such as \"Bäckerei\" or \"Lidl\"), description (what it was for, only when the item says), occurredOn (ISO date, only when the item names another day than the capture's, such as yesterday), and note. Leave out accountId: the processor records it in the owner's Cash account. Card and bank payments arrive from the bank connection by themselves, so for an item about paying by card or bank transfer set ambiguous true and actions to [] with reason \"Card and bank payments arrive from the bank connection.\". Never claim an action succeeded or execute an action. If information is materially unclear or confidence is below 0.8, set ambiguous true and actions to []. "

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
	// TimeZone is the owner's configured timezone. Relative dates in a
	// capture are resolved in the capture's own timezone when it has one
	// (WithCaptureTimeZone), else in this one, and every Codex run gets the
	// zone it uses as TZ. Nil means UTC.
	TimeZone *time.Location
	// Now is the clock for the planning prompt; tests replace it.
	Now func() time.Time
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

type capturedAtKey struct{}

// WithCapturedAt tells planning runs started with ctx when the item was
// captured, so "tomorrow" in a capture that waited for a retry still means
// the day after it was captured.
func WithCapturedAt(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, capturedAtKey{}, at)
}

func capturedAtFrom(ctx context.Context) time.Time {
	at, _ := ctx.Value(capturedAtKey{}).(time.Time)
	return at
}

type captureTimeZoneKey struct{}

// WithCaptureTimeZone tells planning runs started with ctx the timezone the
// item was captured in, such as the capturing browser's (an IANA name). An
// empty or unknown name leaves the configured timezone in place.
func WithCaptureTimeZone(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, captureTimeZoneKey{}, name)
}

// location is the timezone one planning run resolves dates in: the
// capture's own when it is a valid IANA name, else the configured one.
func (c CodexCLI) location(ctx context.Context) *time.Location {
	name, _ := ctx.Value(captureTimeZoneKey{}).(string)
	if location, ok := loadTimeZone(name); ok {
		return location
	}
	return c.TimeZone
}

// delayedCapture is how long after its capture an item is planned before
// the prompt also states the current time.
const delayedCapture = 10 * time.Minute

// timeContext states the owner's timezone and local date, time, and weekday
// for one planning run. Without it Codex sees only the container's UTC clock,
// so "tomorrow at 10" landed two hours off in Berlin, or on the wrong day near
// midnight. The coming week is spelled out because weekday arithmetic is
// where models slip, and its offsets cover a daylight-saving change.
//
// Relative dates mean what they meant when the owner captured the item, so
// the capture time is the reference when known: a capture taken at 23:59 and
// planned at 00:00, or retried hours later, keeps its "tomorrow".
func timeContext(now, capturedAt time.Time, location *time.Location) string {
	if location == nil {
		location = time.UTC
	}
	local := func(t time.Time) string {
		return t.In(location).Format("Monday 2006-01-02 15:04 (-07:00)")
	}
	reference := now
	var b strings.Builder
	if capturedAt.IsZero() {
		fmt.Fprintf(&b, "The owner's timezone is %s; it is now %s there. ", location, local(now))
	} else {
		reference = capturedAt
		fmt.Fprintf(&b, "The owner's timezone is %s. The item was captured on %s there; resolve relative dates from that moment", location, local(capturedAt))
		if now.Sub(capturedAt) > delayedCapture {
			fmt.Fprintf(&b, " (it is now %s)", local(now))
		}
		b.WriteString(". ")
	}
	day := reference.In(location)
	b.WriteString("The coming days are ")
	for i := 1; i <= 7; i++ {
		next := time.Date(day.Year(), day.Month(), day.Day()+i, 12, 0, 0, 0, location)
		if i > 1 {
			b.WriteString(", ")
		}
		b.WriteString(next.Format("Monday 2006-01-02 (-07:00)"))
	}
	tomorrowAtTen := time.Date(day.Year(), day.Month(), day.Day()+1, 10, 0, 0, 0, location)
	fmt.Fprintf(&b, ". Interpret every date and time in the item in the owner's timezone: today, tomorrow, tonight, this or next week, and bare clock times are relative to that local date and time, and a weekday (\"on Friday\") means its next occurrence that is not already past. Write dueAt, startAt, and endAt as RFC3339 with the owner's UTC offset on that date (for example, tomorrow at 10:00 is %s), and startDate, endDate, and occurredOn as the owner's local date. ", tomorrowAtTen.Format(time.RFC3339))
	return b.String()
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
	// This run's prompt and its TZ use the capture's own zone when known.
	c.TimeZone = c.location(ctx)
	prompt := planPrompt + timeContext(c.now(), capturedAtFrom(ctx), c.TimeZone) + "Inbox item: " + content
	cmd := c.command(runCtx, append(args, "-o", name, prompt)...)
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
	if c.TimeZone != nil {
		// Codex's own environment context then reports the owner's zone too,
		// rather than contradicting the prompt with the container's UTC.
		cmd.Env = append(withoutKey(cmd.Env, "TZ"), "TZ="+c.TimeZone.String())
	}
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

func withoutKey(environ []string, key string) []string {
	kept := environ[:0]
	for _, entry := range environ {
		if !strings.HasPrefix(entry, key+"=") {
			kept = append(kept, entry)
		}
	}
	return kept
}

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

func (c CodexCLI) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
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
