package processor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeClaude writes an executable stand-in for the Claude Code CLI.
func fakeClaude(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// recordingClaude is a fake CLI that records its arguments (one per line),
// working directory, and stdin into dir, then prints a successful result.
func recordingClaude(t *testing.T, dir string) string {
	t.Helper()
	return fakeClaude(t, `printf '%s\n' "$@" > `+dir+`/args
pwd > `+dir+`/pwd
cat > `+dir+`/stdin
echo '{"type":"result","subtype":"success","is_error":false,"result":"{}"}'`)
}

func recordedArgs(t *testing.T, dir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// flagValue is the argument after flag, and whether flag was given.
func flagValue(args []string, flag string) (string, bool) {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func TestClaudePlanReportsSessionTokensAndTheResult(t *testing.T) {
	claude := fakeClaude(t, `cat >/dev/null
echo '{"type":"system","subtype":"init","session_id":"session-123","tools":[]}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"{\"actions\":[]}"}]},"session_id":"session-123"}'
echo '{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1794290400,"rateLimitType":"seven_day","unifiedWindows":{"seven_day":{"utilization":0.63,"resetsAt":1794290400,"status":"allowed"}}}}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"{\"actions\":[]}","structured_output":null,"session_id":"session-123","usage":{"input_tokens":2,"cache_creation_input_tokens":1191,"cache_read_input_tokens":8000,"output_tokens":74,"output_tokens_details":{"thinking_tokens":12}}}'`)
	observer := &tokenRecorder{}
	limits := &ClaudeLimits{}
	plan, err := ClaudeCLI{Command: claude, WorkDir: t.TempDir(), Limits: limits}.Plan(WithRunObserver(context.Background(), observer), "buy milk")
	if err != nil || plan != `{"actions":[]}` {
		t.Fatalf("plan = %q, err = %v", plan, err)
	}
	if !observer.spawned || len(observer.sessions) != 1 || observer.sessions[0] != "session-123" {
		t.Fatalf("observer = %+v", observer)
	}
	// Input counts every prompt token, cached ones included, as for Codex.
	if len(observer.usage) != 1 || observer.usage[0] != (TokenUsage{Input: 9193, CachedInput: 8000, Output: 74, Reasoning: 12}) {
		t.Fatalf("usage = %+v", observer.usage)
	}
	usage, err := ClaudeCLI{Limits: limits}.Usage(context.Background())
	if err != nil || len(usage.Limits) != 1 {
		t.Fatalf("usage = %+v, err = %v", usage, err)
	}
	weekly := usage.Limits[0]
	if weekly.ID != "seven_day" || weekly.Reached != "" || len(weekly.Windows) != 1 || weekly.Windows[0].UsedPercent != 63 ||
		*weekly.Windows[0].WindowMinutes != 10080 || !weekly.Windows[0].ResetsAt.Equal(time.Unix(1794290400, 0)) {
		t.Fatalf("weekly = %+v", weekly)
	}
}

func TestClaudeStructuredOutputIsTheAnswer(t *testing.T) {
	claude := fakeClaude(t, `cat >/dev/null
echo '{"type":"result","subtype":"success","is_error":false,"result":"Here you go.","structured_output":{"ideas":[]}}'`)
	out, err := ClaudeCLI{Command: claude, WorkDir: t.TempDir()}.Ideas(context.Background(), "ideas", `{"type":"object"}`, WebSearchDisabled)
	if err != nil || out != `{"ideas":[]}` {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestClaudeRunsHaveNoToolsMCPServersOrHostSettings(t *testing.T) {
	dir := t.TempDir()
	claude := recordingClaude(t, dir)
	if _, err := (ClaudeCLI{Command: claude, WorkDir: dir}).Plan(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	args := recordedArgs(t, dir)
	for _, want := range []string{"-p", "--verbose", "--no-session-persistence", "--strict-mcp-config", "--disable-slash-commands"} {
		if !hasArg(args, want) {
			t.Fatalf("args %q lack %q", args, want)
		}
	}
	for flag, want := range map[string]string{"--input-format": "stream-json", "--output-format": "stream-json", "--model": "sonnet",
		"--tools": "", "--permission-mode": "dontAsk", "--setting-sources": "", "--system-prompt": claudeSystemPrompt} {
		if got, ok := flagValue(args, flag); !ok || got != want {
			t.Fatalf("%s = %q (given %v), want %q; args %q", flag, got, ok, want, args)
		}
	}
	// Planning has no schema, no tools to allow, and the CLI's own effort.
	for _, flag := range []string{"--json-schema", "--allowed-tools", "--effort"} {
		if hasArg(args, flag) {
			t.Fatalf("args %q have %s", args, flag)
		}
	}
	// The run starts in the workdir and the prompt never goes on the
	// command line.
	if pwd, _ := os.ReadFile(filepath.Join(dir, "pwd")); !sameDir(t, strings.TrimSpace(string(pwd)), dir) {
		t.Fatalf("pwd = %q, want %q", pwd, dir)
	}
	if strings.Contains(strings.Join(args, " "), "Inbox item") {
		t.Fatalf("the prompt is on the command line: %q", args)
	}
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

func TestClaudeSchemaModelEffortAndWebSearch(t *testing.T) {
	dir := t.TempDir()
	claude := recordingClaude(t, dir)
	cli := ClaudeCLI{Command: claude, WorkDir: dir, Model: "opus", Effort: "low"}
	schema := `{"type":"object","properties":{"ideas":{"type":"array"}}}`
	for _, tc := range []struct {
		webSearch, tools string
	}{{WebSearchDisabled, ""}, {"", ""}, {WebSearchCached, "WebSearch"}, {WebSearchLive, "WebSearch,WebFetch"}} {
		if _, err := cli.Ideas(context.Background(), "ideas", schema, tc.webSearch); err != nil {
			t.Fatal(err)
		}
		args := recordedArgs(t, dir)
		if got, _ := flagValue(args, "--tools"); got != tc.tools {
			t.Fatalf("web search %q: --tools %q, want %q", tc.webSearch, got, tc.tools)
		}
		allowed, ok := flagValue(args, "--allowed-tools")
		if ok != (tc.tools != "") || allowed != tc.tools {
			t.Fatalf("web search %q: --allowed-tools %q (given %v)", tc.webSearch, allowed, ok)
		}
		for flag, want := range map[string]string{"--json-schema": schema, "--model": "opus", "--effort": "low"} {
			if got, _ := flagValue(args, flag); got != want {
				t.Fatalf("%s = %q, want %q", flag, got, want)
			}
		}
	}
	for name, call := range map[string]func() error{
		"categories":  func() error { _, err := cli.Categories(context.Background(), "p", schema); return err },
		"budget plan": func() error { _, err := cli.BudgetPlan(context.Background(), "p", schema); return err },
	} {
		if err := call(); err != nil {
			t.Fatal(err)
		}
		args := recordedArgs(t, dir)
		if got, _ := flagValue(args, "--tools"); got != "" || hasArg(args, "--allowed-tools") {
			t.Fatalf("%s searches the web: %q", name, args)
		}
		if got, _ := flagValue(args, "--json-schema"); got != schema {
			t.Fatalf("%s: --json-schema %q", name, got)
		}
	}
}

func TestClaudeImagesGoOnStdinWithThePrompt(t *testing.T) {
	dir := t.TempDir()
	claude := recordingClaude(t, dir)
	images := CaptureImages{Images: []Image{{ContentType: "image/jpeg", Data: []byte("jpeg bytes")}, {ContentType: "image/png", Data: []byte("png bytes")}}, Omitted: 1}
	if _, err := (ClaudeCLI{Command: claude, WorkDir: dir}).Plan(WithImages(context.Background(), images), "Photo"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	if strings.Count(string(raw), "\n") != 1 {
		t.Fatalf("stdin is not one stream-json line: %q", raw)
	}
	var message struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	content := message.Message.Content
	if message.Type != "user" || message.Message.Role != "user" || len(content) != 3 {
		t.Fatalf("message = %+v", message)
	}
	for i, image := range images.Images {
		block := content[i]
		if block.Type != "image" || block.Source.Type != "base64" || block.Source.MediaType != image.ContentType || block.Source.Data != base64.StdEncoding.EncodeToString(image.Data) {
			t.Fatalf("block %d = %+v", i, block)
		}
	}
	text := content[2]
	if text.Type != "text" || !strings.HasPrefix(text.Text, planPrompt) || !strings.Contains(text.Text, "comes with 2 attached image(s)") ||
		!strings.Contains(text.Text, "1 more image(s)") || !strings.HasSuffix(text.Text, "Inbox item: Photo") {
		t.Fatalf("text block = %.200q…", text.Text)
	}
	// Without images the message holds the prompt alone.
	if _, err := (ClaudeCLI{Command: claude, WorkDir: dir}).Categories(context.Background(), "the prompt", "{}"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "stdin"))
	if want := `{"message":{"content":[{"text":"the prompt","type":"text"}],"role":"user"},"type":"user"}` + "\n"; string(raw) != want {
		t.Fatalf("stdin = %q, want %q", raw, want)
	}
}

func TestClaudeFailureUsesTheCLIsOwnReason(t *testing.T) {
	claude := fakeClaude(t, `cat >/dev/null
echo '{"type":"system","subtype":"init","session_id":"s"}'
echo '{"type":"system","subtype":"api_retry","attempt":1,"error_status":401,"error":"authentication_failed"}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"Failed to authenticate. API Error: 401 OAuth access token is invalid."}]},"error":"authentication_failed","is_api_error_message":true,"api_error_status":401}'
echo '{"type":"result","subtype":"success","is_error":true,"api_error_status":401,"result":"Failed to authenticate. API Error: 401 OAuth access token is invalid.","usage":{"input_tokens":0,"output_tokens":0}}'
echo "prompt echo: buy milk" >&2
exit 1`)
	observer := &tokenRecorder{}
	_, err := ClaudeCLI{Command: claude, WorkDir: t.TempDir()}.Plan(WithRunObserver(context.Background(), observer), "buy milk")
	if err == nil || !strings.Contains(err.Error(), "ERROR: Failed to authenticate. API Error: 401 OAuth access token is invalid.") || strings.Contains(err.Error(), "buy milk") {
		t.Fatalf("err = %v", err)
	}
	if classifyCodexError(err.Error()) != CodexUnauthenticated {
		t.Fatalf("classified as %s", classifyCodexError(err.Error()))
	}
	if len(observer.usage) != 0 {
		t.Fatalf("a failed run without tokens reported usage %+v", observer.usage)
	}
	// An error result fails the run even when the CLI exits 0, and so does
	// output without a result.
	for name, body := range map[string]string{
		"error result": `echo '{"type":"result","subtype":"error_max_turns","is_error":true,"errors":["max turns reached"]}'`,
		"no result":    `echo 'something went wrong'`,
	} {
		_, err := ClaudeCLI{Command: fakeClaude(t, "cat >/dev/null\n"+body), WorkDir: t.TempDir()}.Plan(context.Background(), "x")
		if err == nil || classifyCodexError(err.Error()) != CodexFailing {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestClaudeUsageLimitIsReportedWithItsResetTime(t *testing.T) {
	claude := fakeClaude(t, `cat >/dev/null
echo '{"type":"system","subtype":"init","session_id":"s"}'
echo '{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1794290400,"rateLimitType":"five_hour","unifiedWindows":{"five_hour":{"utilization":1.0,"resetsAt":1794290400,"status":"rejected"},"seven_day":{"utilization":0.4,"resetsAt":1794700000,"status":"allowed"}}}}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"You'"'"'ve hit your limit · resets 7am (Europe/Berlin)"}]},"error":"rate_limit","api_error":"usage_limit_reached","api_error_params":{"rate_limit_info":{"status":"rejected","resetsAt":1794290400,"rateLimitType":"five_hour"}},"api_error_status":429}'
echo '{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"You'"'"'ve hit your limit · resets 7am (Europe/Berlin)"}'
exit 1`)
	now := time.Unix(1794280000, 0)
	limits := &ClaudeLimits{}
	cli := ClaudeCLI{Command: claude, WorkDir: t.TempDir(), Limits: limits, Now: func() time.Time { return now }}
	_, err := cli.Plan(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "ERROR: usage limit reached, resets 2026-11-10T06:00:00Z: You've hit your limit") {
		t.Fatalf("err = %v", err)
	}
	if classifyCodexError(err.Error()) != CodexUsageLimited {
		t.Fatalf("classified as %s", classifyCodexError(err.Error()))
	}
	usage, _ := cli.Usage(context.Background())
	if len(usage.Limits) != 2 || usage.Limits[0].ID != "five_hour" || usage.Limits[0].Reached == "" || usage.Limits[1].Reached != "" {
		t.Fatalf("limits = %+v", usage.Limits)
	}
	at, ok := usage.UsageRecovery()
	if !ok || !at.Equal(time.Unix(1794290400, 0)) {
		t.Fatalf("recovery = %s, %v", at, ok)
	}
	// The retry waits for the reset rather than backing off.
	policy := RetryPolicy{Now: func() time.Time { return now }, Jitter: func() float64 { return 0 }, RecoveryAt: func(context.Context) (time.Time, bool) {
		usage, _ := cli.Usage(context.Background())
		return usage.UsageRecovery()
	}}
	if retryAt, reason := policy.next(context.Background(), failureCodex, 0, err.Error()); reason != reasonUsageLimited || !retryAt.Equal(at) {
		t.Fatalf("retry at %s (%s)", retryAt, reason)
	}
	// Once the reset has passed, the limit is no longer reached.
	now = time.Unix(1794290400, 0).Add(time.Minute)
	if usage, _ := cli.Usage(context.Background()); usage.Limits[0].Reached != "" {
		t.Fatalf("a passed reset is still reached: %+v", usage.Limits[0])
	}
}

func TestClaudeThrottlingIsARateLimitWithoutAReset(t *testing.T) {
	claude := fakeClaude(t, `cat >/dev/null
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"API Error: Request rejected (429)"}]},"error":"rate_limit","api_error_status":429}'
echo '{"type":"result","subtype":"success","is_error":true,"result":"API Error: Request rejected (429)"}'
exit 1`)
	limits := &ClaudeLimits{}
	_, err := ClaudeCLI{Command: claude, WorkDir: t.TempDir(), Limits: limits}.Plan(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "ERROR: rate limit: API Error: Request rejected (429)") || classifyCodexError(err.Error()) != CodexUsageLimited {
		t.Fatalf("err = %v", err)
	}
	usage, _ := ClaudeCLI{Limits: limits}.Usage(context.Background())
	if _, ok := usage.UsageRecovery(); ok {
		t.Fatalf("throttling reported a reset: %+v", usage)
	}
}

func TestClaudeRunsGetASanitizedSubscriptionEnvironment(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api-should-not-leak")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "auth-token-should-not-leak")
	t.Setenv("ANTHROPIC_BASE_URL", "https://proxy.invalid")
	t.Setenv("OPENAI_API_KEY", "sk-should-not-leak")
	t.Setenv("LIFE_OS_FINANCE_DB_PASSWORD", "secret")
	t.Setenv("LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN", "the-env-file-name-is-not-passed")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-subscription")
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/lifeos-claude/.claude")
	t.Setenv("HOME", "/tmp/lifeos-claude")
	claude := fakeClaude(t, `cat >/dev/null
env > `+envFile+`
echo '{"type":"result","subtype":"success","is_error":false,"result":"{}"}'`)
	berlin, _ := time.LoadLocation("Europe/Berlin")
	if _, err := (ClaudeCLI{Command: claude, WorkDir: dir, TimeZone: berlin}).Plan(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(envFile)
	env := string(raw)
	for _, leaked := range []string{"ANTHROPIC_", "should-not-leak", "proxy.invalid", "LIFE_OS_"} {
		if strings.Contains(env, leaked) {
			t.Fatalf("leaked %q:\n%s", leaked, env)
		}
	}
	for _, want := range []string{"CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-subscription", "CLAUDE_CONFIG_DIR=/tmp/lifeos-claude/.claude", "HOME=/tmp/lifeos-claude", "PATH=",
		"DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_TELEMETRY=1", "TZ=Europe/Berlin"} {
		if !strings.Contains(env, want) {
			t.Fatalf("missing %q:\n%s", want, env)
		}
	}
	// The ANTHROPIC_ prefix is refused even if it were ever allowlisted.
	claudeAllowedEnv["ANTHROPIC_API_KEY"] = true
	defer delete(claudeAllowedEnv, "ANTHROPIC_API_KEY")
	for _, entry := range claudeEnv([]string{"ANTHROPIC_API_KEY=sk", "PATH=/bin"}, nil) {
		if strings.HasPrefix(entry, "ANTHROPIC_") {
			t.Fatalf("claudeEnv passed %q", entry)
		}
	}
}

func TestClaudeLoggedInNeedsASubscriptionSignIn(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		status string
		exit   int
		want   bool
	}{
		{`{"loggedIn":true,"authMethod":"oauth_token"}`, 0, true},
		{`{"loggedIn":true,"authMethod":"claude.ai"}`, 0, true},
		{`{"loggedIn":true,"authMethod":"api_key","apiKeySource":"ANTHROPIC_API_KEY"}`, 0, false},
		{`{"loggedIn":false,"authMethod":"none"}`, 1, false},
		{`not json`, 0, false},
	} {
		claude := fakeClaude(t, `printf '%s\n' "$@" > `+dir+`/args
echo '`+tc.status+`'
exit `+string(rune('0'+tc.exit)))
		limits := &ClaudeLimits{}
		if got := (ClaudeCLI{Command: claude, WorkDir: dir, Limits: limits}).LoggedIn(context.Background()); got != tc.want {
			t.Fatalf("%s: LoggedIn = %v", tc.status, got)
		}
		if args := recordedArgs(t, dir); strings.Join(args, " ") != "auth status --json" {
			t.Fatalf("args = %q", args)
		}
		if tc.want {
			if usage, _ := (ClaudeCLI{Limits: limits}).Usage(context.Background()); usage.AuthMode == "" {
				t.Fatal("the sign-in method is not reported with the usage")
			}
		}
	}
}

func TestClaudeTimeoutStopsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child.pid")
	claude := fakeClaude(t, `trap '' TERM
sleep 300 &
echo $! > `+child+`
echo '{"type":"system","subtype":"init","session_id":"s"}'
while true; do sleep 1; done`)
	_, err := ClaudeCLI{Command: claude, WorkDir: dir, Timeout: 500 * time.Millisecond, Grace: 300 * time.Millisecond}.Plan(context.Background(), "x")
	if !errors.Is(err, ErrRunTimedOut) || !strings.Contains(err.Error(), "Claude Code run timed out after 500ms") {
		t.Fatalf("err = %v", err)
	}
	if alive(t, child) {
		t.Fatal("a child of the timed-out run is still running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	if _, err := (ClaudeCLI{Command: claude, WorkDir: dir, Grace: 300 * time.Millisecond}).Plan(ctx, "x"); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("cancelled err = %v", err)
	}
}

func TestClaudeSlotsLimitConcurrentRuns(t *testing.T) {
	claude := fakeClaude(t, `cat >/dev/null
echo '{"type":"result","subtype":"success","is_error":false,"result":"{}"}'`)
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := (ClaudeCLI{Command: claude, WorkDir: t.TempDir(), Slots: slots}).Plan(ctx, "x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want to wait for a slot", err)
	}
}

func TestClaudeUsageBeforeAnyRunHasNoLimits(t *testing.T) {
	for _, cli := range []ClaudeCLI{{}, {Limits: &ClaudeLimits{}}} {
		usage, err := cli.Usage(context.Background())
		if err != nil || usage.Limits == nil || len(usage.Limits) != 0 || usage.CheckedAt.IsZero() {
			t.Fatalf("usage = %+v, err = %v", usage, err)
		}
	}
	// A limit without windows is still reported by its type.
	limits := &ClaudeLimits{}
	limits.observe(claudeRateLimitInfo{Status: "rejected", ResetsAt: 1794290400, RateLimitType: "seven_day_opus"}, time.Unix(1794280000, 0))
	usage := limits.usage(time.Unix(1794280000, 0))
	if len(usage.Limits) != 1 || usage.Limits[0].ID != "seven_day_opus" || usage.Limits[0].Reached == "" || usage.Limits[0].Windows[0].UsedPercent != 100 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestLoadConfigSelectsTheProvider(t *testing.T) {
	t.Setenv("LIFE_OS_CODEX_MODEL", "gpt-6-luna")
	t.Setenv("LIFE_OS_CODEX_REASONING_EFFORT", "medium")
	cfg, err := LoadConfig()
	if err != nil || cfg.Provider != ProviderCodex || cfg.Model != "gpt-6-luna" || cfg.ReasoningEffort != "medium" || cfg.Command() != "codex" {
		t.Fatalf("default: cfg = %+v, err = %v", cfg, err)
	}
	t.Setenv("LIFE_OS_AI_PROVIDER", "claude")
	cfg, err = LoadConfig()
	if err != nil || cfg.Provider != ProviderClaude || cfg.Model != "sonnet" || cfg.ReasoningEffort != "" || cfg.Command() != "claude" || cfg.Profile() != "sonnet" {
		t.Fatalf("claude: cfg = %+v, err = %v", cfg, err)
	}
	t.Setenv("LIFE_OS_CLAUDE_MODEL", "opus")
	t.Setenv("LIFE_OS_CLAUDE_EFFORT", "high")
	t.Setenv("LIFE_OS_CLAUDE_COMMAND", "/opt/claude")
	if cfg, err = LoadConfig(); err != nil || cfg.Model != "opus" || cfg.ReasoningEffort != "high" || cfg.Command() != "/opt/claude" || cfg.Profile() != "opus/high" {
		t.Fatalf("claude settings: cfg = %+v, err = %v", cfg, err)
	}
	t.Setenv("LIFE_OS_CLAUDE_EFFORT", "medium-ish")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "LIFE_OS_CLAUDE_EFFORT") {
		t.Fatalf("bad effort: err = %v", err)
	}
	t.Setenv("LIFE_OS_AI_PROVIDER", "gemini")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "LIFE_OS_AI_PROVIDER") {
		t.Fatalf("bad provider: err = %v", err)
	}
}
