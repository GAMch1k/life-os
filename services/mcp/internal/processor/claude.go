package processor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ClaudeCLI is the alternative agent runtime (LIFE_OS_AI_PROVIDER=claude):
// the Claude Code CLI run headless (print mode) on the owner's Claude
// subscription, through the long-lived OAuth token of `claude setup-token`
// (CLAUDE_CODE_OAUTH_TOKEN). It never uses API billing: ANTHROPIC_API_KEY and
// the other ANTHROPIC_* settings are never passed to a run.
//
// A run gets the same controls as a Codex run: a slot, a deadline, an
// explicit working directory, a sanitized environment, its own process group,
// and graceful termination. It has no tools (web search only for the ideas job
// when configured), no MCP servers, no settings, CLAUDE.md, skills, or slash
// commands from the host, and no saved session. The prompt and the capture's
// images go in on stdin as one stream-json user message; the session id,
// token usage, rate limits, and the final (structured) answer are read from
// the stream-json output.
type ClaudeCLI struct {
	Command, Model, WorkDir string
	// Effort, when set, is passed as --effort (low, medium, high, xhigh,
	// max); empty keeps the CLI's default.
	Effort string
	// Timeout bounds one run (default 5 minutes).
	Timeout time.Duration
	// Grace is how long a cancelled or timed-out run may take to exit after
	// SIGTERM before its process group is killed (default 5 seconds).
	Grace time.Duration
	// Slots limits concurrent runs; nil means unlimited.
	Slots chan struct{}
	// TimeZone is the owner's configured timezone, as for CodexCLI.
	TimeZone *time.Location
	// Now is the clock for the planning prompt; tests replace it.
	Now func() time.Time
	// Run builds the command; tests replace it.
	Run func(context.Context, string, ...string) *exec.Cmd
	// Limits keeps the subscription's rate limits as the runs report them,
	// for Usage; nil keeps none.
	Limits *ClaudeLimits
}

// claudeSystemPrompt replaces Claude Code's own coding-agent system prompt:
// the runs plan and answer, they do not code.
const claudeSystemPrompt = "You are the AI of Life OS, the owner's personal organizer, called by a program without a person watching. Follow the user message's instructions exactly and answer only with what it asks for."

// claudeDefaultModel is the model alias used when none is configured.
const claudeDefaultModel = "sonnet"

// claudeSpec is what one Claude Code run gets beyond the runtime's controls.
type claudeSpec struct {
	// prompt builds the prompt in the run's timezone.
	prompt func(*time.Location) string
	images CaptureImages
	// schema, when set, is the JSON Schema of the answer (--json-schema).
	schema string
	// webSearch is the web search mode (WebSearchDisabled, Cached, or Live).
	webSearch string
}

// Plan plans one capture with the same prompt as CodexCLI.Plan. Web search
// is always off for captures.
func (c ClaudeCLI) Plan(ctx context.Context, content string) (string, error) {
	images := imagesFrom(ctx)
	return c.run(ctx, claudeSpec{images: images, webSearch: WebSearchDisabled, prompt: func(zone *time.Location) string {
		return planPrompt + movePrompt + timeContext(c.now(), capturedAtFrom(ctx), zone) + imagesNote(images) + recentContextFrom(ctx) + "Inbox item: " + content
	}})
}

// Ideas asks for recipe ideas, the answer shaped by the JSON schema, with web
// search as configured for this job alone.
func (c ClaudeCLI) Ideas(ctx context.Context, prompt, schema, webSearch string) (string, error) {
	return c.run(ctx, claudeSpec{schema: schema, webSearch: webSearch, prompt: func(*time.Location) string { return prompt }})
}

// Categories asks for transaction categories, the answer shaped by the JSON
// schema. It never searches the web.
func (c ClaudeCLI) Categories(ctx context.Context, prompt, schema string) (string, error) {
	return c.run(ctx, claudeSpec{schema: schema, webSearch: WebSearchDisabled, prompt: func(*time.Location) string { return prompt }})
}

// BudgetPlan asks for a month's budget plan, the answer shaped by the JSON
// schema. It never searches the web.
func (c ClaudeCLI) BudgetPlan(ctx context.Context, prompt, schema string) (string, error) {
	return c.run(ctx, claudeSpec{schema: schema, webSearch: WebSearchDisabled, prompt: func(*time.Location) string { return prompt }})
}

// claudeTools are the built-in tools a run may use: none, except web search
// for the ideas job. The processor's web search modes map onto Claude Code's
// tools: cached allows WebSearch (Claude Code has no cached index), live also
// allows WebFetch to read the pages found.
func claudeTools(webSearch string) string {
	switch webSearch {
	case WebSearchCached:
		return "WebSearch"
	case WebSearchLive:
		return "WebSearch,WebFetch"
	}
	return ""
}

// args are the CLI arguments of one run.
func (c ClaudeCLI) args(spec claudeSpec) []string {
	tools := claudeTools(spec.webSearch)
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--model", c.model(), "--no-session-persistence",
		// Only these built-in tools exist for the run ("" is none) …
		"--tools", tools,
		// … and anything that would ask for a permission is denied.
		"--permission-mode", "dontAsk",
		// No MCP servers, no user, project, or local settings, no skills.
		"--strict-mcp-config", "--setting-sources", "", "--disable-slash-commands",
		"--system-prompt", claudeSystemPrompt}
	if tools != "" {
		args = append(args, "--allowed-tools", tools)
	}
	if c.Effort != "" {
		args = append(args, "--effort", c.Effort)
	}
	if spec.schema != "" {
		args = append(args, "--json-schema", spec.schema)
	}
	return args
}

// claudeInput is the run's stdin: one stream-json user message with the
// capture's images (base64 image blocks) and then the prompt.
func claudeInput(prompt string, images []Image) ([]byte, error) {
	content := make([]map[string]any, 0, len(images)+1)
	for _, image := range images {
		content = append(content, map[string]any{"type": "image", "source": map[string]any{
			"type": "base64", "media_type": image.ContentType, "data": base64.StdEncoding.EncodeToString(image.Data)}})
	}
	content = append(content, map[string]any{"type": "text", "text": prompt})
	raw, err := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}})
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// claudeTimeout is a run stopped at its deadline; it is ErrRunTimedOut.
type claudeTimeout struct{ after time.Duration }

func (e claudeTimeout) Error() string {
	return fmt.Sprintf("Claude Code run timed out after %s", e.after)
}
func (claudeTimeout) Is(target error) bool { return target == ErrRunTimedOut }

// run starts one Claude Code run with the runtime's controls and returns its
// answer: the structured output when a schema was given, else the result text.
func (c ClaudeCLI) run(ctx context.Context, spec claudeSpec) (string, error) {
	// Images over Claude's limits are made smaller first; one that cannot be
	// is errImageNotSendable, and no run starts.
	images, err := fitClaudeImages(spec.images.Images)
	if err != nil {
		return "", err
	}
	release, err := acquireSlot(ctx, c.Slots)
	if err != nil {
		return "", err
	}
	defer release()
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// This run's prompt and its TZ use the capture's own zone when known.
	c.TimeZone = c.location(ctx)
	input, err := claudeInput(spec.prompt(c.TimeZone), images)
	if err != nil {
		return "", fmt.Errorf("prepare Claude Code input: %w", err)
	}
	cmd := c.command(runCtx, c.args(spec)...)
	cmd.Stdin = bytes.NewReader(input)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, limit: 64 * 1024}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start Claude Code CLI: %w", err)
	}
	observer := observerFrom(ctx)
	if observer != nil {
		observer.Spawned()
	}
	events := readClaudeEvents(stdout, observer, c.Limits, c.now)
	waitErr := cmd.Wait()
	killGroup(cmd)
	switch {
	case ctx.Err() != nil:
		return "", fmt.Errorf("Claude Code run cancelled: %w", ctx.Err())
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return "", claudeTimeout{after: timeout}
	case waitErr != nil:
		return "", fmt.Errorf("run Claude Code CLI: %w: %s", waitErr, events.failure(stderr.String()))
	case events.result == nil || events.result.IsError:
		return "", fmt.Errorf("run Claude Code CLI: %s", events.failure(stderr.String()))
	}
	return events.answer(), nil
}

// command builds a Claude Code CLI command with the runtime's controls: the
// working directory, the sanitized environment, its own process group, and
// graceful termination on cancellation.
func (c ClaudeCLI) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := c.runner()(ctx, c.binary(), args...)
	cmd.Env = claudeEnv(os.Environ(), c.TimeZone)
	if c.WorkDir != "" {
		cmd.Dir = c.WorkDir
	}
	manageProcess(cmd, c.Grace)
	return cmd
}

// claudeAllowedEnv is everything a Claude Code run inherits. Its credential
// is the subscription's OAuth token (CLAUDE_CODE_OAUTH_TOKEN), or locally the
// signed-in user's own login; HOME and CLAUDE_CONFIG_DIR point at a writable
// tmpfs in the container.
var claudeAllowedEnv = map[string]bool{"PATH": true, "HOME": true, "USER": true, "CLAUDE_CONFIG_DIR": true, "CLAUDE_CODE_OAUTH_TOKEN": true, "LANG": true, "LC_ALL": true, "TZ": true, "TMPDIR": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true}

// claudeFixedEnv turns off what a headless run must not do: update itself,
// send telemetry or error reports, or read CLAUDE.md files.
var claudeFixedEnv = []string{"DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1", "CLAUDE_CODE_DISABLE_CLAUDE_MDS=1"}

// claudeEnv is a run's environment. ANTHROPIC_* (an API key, an auth token, a
// base URL) is stripped by name as well as by the allowlist: a run must never
// bill the API or talk to another endpoint instead of the subscription.
func claudeEnv(environ []string, zone *time.Location) []string {
	env := make([]string, 0, len(claudeAllowedEnv)+len(claudeFixedEnv))
	for _, entry := range environ {
		key, _, ok := strings.Cut(entry, "=")
		if ok && claudeAllowedEnv[key] && !strings.HasPrefix(key, "ANTHROPIC_") {
			env = append(env, entry)
		}
	}
	env = append(env, claudeFixedEnv...)
	if zone != nil {
		env = append(withoutKey(env, "TZ"), "TZ="+zone.String())
	}
	return env
}

// claudeRateLimitInfo is Claude Code's rate_limit_info: the subscription's
// limit status as the API's responses reported it.
type claudeRateLimitInfo struct {
	// Status is allowed, allowed_warning, or rejected (requests are refused).
	Status string `json:"status"`
	// ResetsAt is when the limit named by RateLimitType resets (Unix seconds).
	ResetsAt      int64    `json:"resetsAt"`
	RateLimitType string   `json:"rateLimitType"`
	Utilization   *float64 `json:"utilization"`
	// UnifiedWindows are the session (five_hour) and weekly windows, when
	// the CLI reports them; utilization is a fraction of the window.
	UnifiedWindows map[string]struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    int64   `json:"resetsAt"`
		Status      string  `json:"status"`
	} `json:"unifiedWindows"`
}

// claudeResult is the stream's final result message.
type claudeResult struct {
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	APIErrorStatus   int             `json:"api_error_status"`
	Errors           []any           `json:"errors"`
	Usage            *struct {
		Input         int `json:"input_tokens"`
		CacheCreation int `json:"cache_creation_input_tokens"`
		CacheRead     int `json:"cache_read_input_tokens"`
		Output        int `json:"output_tokens"`
		Details       *struct {
			Thinking int `json:"thinking_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

// claudeEvents is what a run's stream-json output said about itself.
type claudeEvents struct {
	sessionID string
	result    *claudeResult
	// limited is set when a usage limit refused the run; resetsAt is when
	// it resets, when reported. throttled is set for other rate limiting.
	limited, throttled bool
	resetsAt           time.Time
	// apiErrors are the texts of the CLI's API error messages.
	apiErrors []string
	// text is non-JSON output.
	text strings.Builder
}

func readClaudeEvents(stdout io.Reader, observer RunObserver, limits *ClaudeLimits, now func() time.Time) *claudeEvents {
	events := &claudeEvents{}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var head struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(line, &head) != nil || head.Type == "" {
			if events.text.Len() < 64*1024 {
				events.text.Write(line)
				events.text.WriteByte('\n')
			}
			continue
		}
		switch head.Type {
		case "system":
			if head.Subtype == "init" && head.SessionID != "" && events.sessionID == "" {
				events.sessionID = head.SessionID
				if observer != nil {
					observer.Session(head.SessionID)
				}
			}
		case "rate_limit_event":
			var event struct {
				Info *claudeRateLimitInfo `json:"rate_limit_info"`
			}
			if json.Unmarshal(line, &event) == nil && event.Info != nil {
				events.rateLimit(*event.Info, limits, now)
			}
		case "assistant":
			var event struct {
				Error          json.RawMessage `json:"error"`
				APIError       string          `json:"api_error"`
				APIErrorParams *struct {
					RateLimitInfo *claudeRateLimitInfo `json:"rate_limit_info"`
				} `json:"api_error_params"`
				Message struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &event) != nil {
				continue
			}
			var kind string
			_ = json.Unmarshal(event.Error, &kind)
			if kind == "" && event.APIError == "" {
				continue
			}
			for _, block := range event.Message.Content {
				if block.Type == "text" && block.Text != "" {
					events.apiErrors = append(events.apiErrors, block.Text)
				}
			}
			switch {
			case event.APIError == "usage_limit_reached":
				events.limited = true
			case kind == "rate_limit":
				events.throttled = true
			}
			if event.APIErrorParams != nil && event.APIErrorParams.RateLimitInfo != nil {
				events.rateLimit(*event.APIErrorParams.RateLimitInfo, limits, now)
			}
		case "result":
			var result claudeResult
			if json.Unmarshal(line, &result) != nil {
				continue
			}
			events.result = &result
			if tokens, ok := observer.(tokenObserver); ok && result.Usage != nil {
				usage := result.Usage
				reported := TokenUsage{
					// Input counts every prompt token, cached ones included,
					// as Codex reports it.
					Input:       usage.Input + usage.CacheCreation + usage.CacheRead,
					CachedInput: usage.CacheRead,
					Output:      usage.Output,
				}
				if usage.Details != nil {
					reported.Reasoning = usage.Details.Thinking
				}
				if reported != (TokenUsage{}) {
					tokens.Tokens(reported)
				}
			}
		}
	}
	_, _ = io.Copy(io.Discard, stdout)
	return events
}

// rateLimit records rate-limit information: for Usage, and as the run's own
// limit when it was refused.
func (e *claudeEvents) rateLimit(info claudeRateLimitInfo, limits *ClaudeLimits, now func() time.Time) {
	if limits != nil {
		limits.observe(info, now())
	}
	if info.Status == "rejected" {
		e.limited = true
		if info.ResetsAt > 0 {
			e.resetsAt = time.Unix(info.ResetsAt, 0).UTC()
		}
	}
}

// answer is the run's final answer: the structured output when there is one,
// else the result text.
func (e *claudeEvents) answer() string {
	if structured := bytes.TrimSpace(e.result.StructuredOutput); len(structured) > 0 && string(structured) != "null" {
		return string(structured)
	}
	return e.result.Result
}

// failure is the reason a run failed, in the form classifyCodexError reads: a
// usage limit says so (with its reset time when reported), otherwise the
// CLI's own error text.
func (e *claudeEvents) failure(stderr string) string {
	var reason string
	switch {
	case e.result != nil && strings.TrimSpace(e.result.Result) != "":
		reason = strings.TrimSpace(e.result.Result)
	case len(e.apiErrors) > 0:
		reason = e.apiErrors[len(e.apiErrors)-1]
	case e.result != nil && (e.result.Subtype != "" || len(e.result.Errors) > 0):
		parts := []string{e.result.Subtype}
		for _, item := range e.result.Errors {
			parts = append(parts, fmt.Sprint(item))
		}
		reason = strings.TrimSpace(strings.Join(parts, " "))
	default:
		reason = strings.TrimPrefix(codexFailure([]byte(e.text.String()+stderr)), "ERROR: ")
		if reason == "" {
			reason = "Claude Code ended without a result"
		}
	}
	if e.result != nil && e.result.APIErrorStatus != 0 && !strings.Contains(reason, fmt.Sprint(e.result.APIErrorStatus)) {
		reason += fmt.Sprintf(" (API status %d)", e.result.APIErrorStatus)
	}
	if e.limited {
		limit := "usage limit reached"
		if !e.resetsAt.IsZero() {
			limit += ", resets " + e.resetsAt.Format(time.RFC3339)
		}
		return "ERROR: " + limit + ": " + reason
	}
	if e.throttled {
		return "ERROR: rate limit: " + reason
	}
	return "ERROR: " + reason
}

// LoggedIn reports whether `claude auth status` finds a subscription sign-in
// (the OAuth token, or a claude.ai login). It reads local credentials only and
// uses no model quota. An API key does not count.
func (c ClaudeCLI) LoggedIn(ctx context.Context) bool {
	cmd := c.command(ctx, "auth", "status", "--json")
	out, err := cmd.Output()
	killGroup(cmd)
	if err != nil {
		return false
	}
	var status struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	if json.Unmarshal(out, &status) != nil {
		return false
	}
	if c.Limits != nil {
		c.Limits.setAuth(status.AuthMethod)
	}
	return status.LoggedIn && (status.AuthMethod == "oauth_token" || status.AuthMethod == "claude.ai")
}

// Usage reports the subscription's limits as the latest run reported them.
// Claude Code has no command that reads them without a model request, so
// there is nothing before the first run and no extra quota is used.
func (c ClaudeCLI) Usage(context.Context) (Usage, error) {
	if c.Limits == nil {
		return Usage{CheckedAt: c.now().UTC(), Limits: []UsageLimit{}}, nil
	}
	return c.Limits.usage(c.now()), nil
}

// ClaudeLimits keeps the latest rate-limit information Claude Code reported
// and the sign-in method, for Usage. Its zero value is ready to use.
type ClaudeLimits struct {
	mu         sync.Mutex
	at         time.Time
	info       *claudeRateLimitInfo
	authMethod string
}

func (l *ClaudeLimits) observe(info claudeRateLimitInfo, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// A refusal's own rate_limit_info names only its limit: the windows the
	// run reported before stay.
	if len(info.UnifiedWindows) == 0 && l.info != nil {
		info.UnifiedWindows = l.info.UnifiedWindows
	}
	l.at, l.info = now.UTC(), &info
}

func (l *ClaudeLimits) setAuth(method string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.authMethod = method
}

// claudeWindows names the subscription's limit windows and their lengths. The
// Status page shows a window as "<name> <length> usage", for example
// "Claude session 5-hour usage" or "Claude weekly usage".
var claudeWindows = map[string]struct {
	name    string
	minutes int
}{
	"five_hour":                  {"Claude session", 300},
	"seven_day":                  {"Claude", 10080},
	"seven_day_opus":             {"Claude Opus", 10080},
	"seven_day_sonnet":           {"Claude Sonnet", 10080},
	"seven_day_overage_included": {"Claude included", 10080},
}

// usage turns the latest information into the processor's Usage: one limit
// per reported window. A limit whose reset has passed is no longer reached.
func (l *ClaudeLimits) usage(now time.Time) Usage {
	l.mu.Lock()
	defer l.mu.Unlock()
	usage := Usage{CheckedAt: l.at, AuthMode: l.authMethod, Limits: []UsageLimit{}}
	if l.info == nil {
		usage.CheckedAt = now.UTC()
		return usage
	}
	info := *l.info
	add := func(id string, utilization *float64, resetsAt int64, rejected bool) {
		window := UsageWindow{}
		if utilization != nil {
			window.UsedPercent = *utilization * 100
		} else if rejected {
			window.UsedPercent = 100
		}
		limit := UsageLimit{ID: id, Name: "Claude limit", Windows: []UsageWindow{}}
		if known, ok := claudeWindows[id]; ok {
			limit.Name = known.name
			minutes := known.minutes
			window.WindowMinutes = &minutes
		}
		if resetsAt > 0 {
			at := time.Unix(resetsAt, 0).UTC()
			window.ResetsAt = &at
			if !at.After(now) {
				rejected = false
			}
		}
		if rejected {
			limit.Reached = "rate_limit_reached"
		}
		limit.Windows = append(limit.Windows, window)
		usage.Limits = append(usage.Limits, limit)
	}
	listed := map[string]bool{}
	for _, id := range []string{"five_hour", "seven_day", "seven_day_overage_included"} {
		window, ok := info.UnifiedWindows[id]
		if !ok {
			continue
		}
		listed[id] = true
		utilization := window.Utilization
		add(id, &utilization, window.ResetsAt, window.Status == "rejected" || (info.Status == "rejected" && info.RateLimitType == id))
	}
	// The limit the information is about, when no window above covers it.
	if id := info.RateLimitType; (id != "" && !listed[id]) || (id == "" && len(listed) == 0) {
		if id == "" {
			id = "claude"
		}
		add(id, info.Utilization, info.ResetsAt, info.Status == "rejected")
	}
	return usage
}

// location is the timezone one run resolves dates in: the capture's own when
// it is a valid IANA name, else the configured one.
func (c ClaudeCLI) location(ctx context.Context) *time.Location {
	name, _ := ctx.Value(captureTimeZoneKey{}).(string)
	if location, ok := loadTimeZone(name); ok {
		return location
	}
	return c.TimeZone
}

func (c ClaudeCLI) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c ClaudeCLI) model() string {
	if c.Model != "" {
		return c.Model
	}
	return claudeDefaultModel
}

func (c ClaudeCLI) binary() string {
	if c.Command != "" {
		return c.Command
	}
	return "claude"
}

func (c ClaudeCLI) runner() func(context.Context, string, ...string) *exec.Cmd {
	if c.Run != nil {
		return c.Run
	}
	return exec.CommandContext
}
