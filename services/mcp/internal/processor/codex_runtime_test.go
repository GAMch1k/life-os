package processor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeCodex writes an executable stand-in for the Codex CLI. The script body
// runs with $out set to the -o output file argument.
func fakeCodex(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nout=\"\"\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = \"-o\" ]; then out=$2; shift; fi\n  shift\ndone\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// alive reports whether the pid written to file still exists.
func alive(t *testing.T, file string) bool {
	t.Helper()
	var raw []byte
	for i := 0; i < 50 && len(raw) == 0; i++ {
		raw, _ = os.ReadFile(file)
		if len(raw) == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("pid file %s: %q", file, raw)
	}
	// A killed process may stay a zombie until its new parent reaps it.
	for i := 0; i < 50; i++ {
		if syscall.Kill(pid, 0) != nil {
			return false
		}
		if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
			if fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:])); len(fields) > 0 && fields[0] == "Z" {
				return false
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

type recordingObserver struct {
	mu       sync.Mutex
	spawned  bool
	sessions []string
}

func (o *recordingObserver) Spawned() { o.mu.Lock(); o.spawned = true; o.mu.Unlock() }
func (o *recordingObserver) Session(id string) {
	o.mu.Lock()
	o.sessions = append(o.sessions, id)
	o.mu.Unlock()
}

func TestPlanReportsSpawnAndSessionAndReturnsTheOutput(t *testing.T) {
	codex := fakeCodex(t, `echo '{"type":"thread.started","thread_id":"thread-123"}'
echo '{"type":"turn.started"}'
printf '{"actions":[]}' > "$out"
echo '{"type":"turn.completed","usage":{"input_tokens":1}}'`)
	observer := &recordingObserver{}
	plan, err := CodexCLI{Command: codex, WorkDir: t.TempDir()}.Plan(WithRunObserver(context.Background(), observer), "buy milk")
	if err != nil || plan != `{"actions":[]}` {
		t.Fatalf("plan = %q, err = %v", plan, err)
	}
	if !observer.spawned || len(observer.sessions) != 1 || observer.sessions[0] != "thread-123" {
		t.Fatalf("observer = %+v", observer)
	}
}

func TestPlanFailureUsesCodexOwnReason(t *testing.T) {
	codex := fakeCodex(t, `echo '{"type":"thread.started","thread_id":"t"}'
echo '{"type":"error","message":"Reconnecting... 5/5 (unexpected status 401 Unauthorized)"}'
echo '{"type":"turn.failed","error":{"message":"unexpected status 401 Unauthorized: Missing bearer"}}'
echo "banner and prompt echo: buy milk" >&2
exit 1`)
	_, err := CodexCLI{Command: codex, WorkDir: t.TempDir()}.Plan(context.Background(), "buy milk")
	if err == nil || !strings.Contains(err.Error(), "ERROR: unexpected status 401 Unauthorized: Missing bearer") || strings.Contains(err.Error(), "buy milk") {
		t.Fatalf("err = %v", err)
	}
	if classifyCodexError(err.Error()) != CodexUnauthenticated {
		t.Fatalf("classified as %s", classifyCodexError(err.Error()))
	}
	// A CLI without JSON output still yields its ERROR lines.
	plain := fakeCodex(t, `echo "ERROR: You’ve hit your usage limit."; exit 1`)
	if _, err := (CodexCLI{Command: plain, WorkDir: t.TempDir()}).Plan(context.Background(), "x"); err == nil || classifyCodexError(err.Error()) != CodexUsageLimited {
		t.Fatalf("plain err = %v", err)
	}
}

func TestTimeoutStopsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child.pid")
	// The run ignores SIGTERM and leaves a child behind: both must die.
	codex := fakeCodex(t, `trap '' TERM
sleep 300 &
echo $! > `+child+`
echo '{"type":"thread.started","thread_id":"t"}'
while true; do sleep 1; done`)
	started := time.Now()
	_, err := CodexCLI{Command: codex, WorkDir: dir, Timeout: 500 * time.Millisecond, Grace: 300 * time.Millisecond}.Plan(context.Background(), "x")
	if !errors.Is(err, ErrRunTimedOut) {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	if alive(t, child) {
		t.Fatal("a child of the timed-out run is still running")
	}
}

func TestCancellationIsGracefulAndLeavesNoOrphans(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child.pid")
	stopped := filepath.Join(dir, "stopped")
	// A well-behaved run exits on SIGTERM; its child must not survive.
	codex := fakeCodex(t, `sleep 300 &
echo $! > `+child+`
trap 'touch `+stopped+`; exit 143' TERM
while true; do sleep 0.1; done`)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	_, err := CodexCLI{Command: codex, WorkDir: dir, Grace: 2 * time.Second}.Plan(ctx, "x")
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(stopped); statErr != nil {
		t.Fatal("the run did not receive SIGTERM before being killed")
	}
	if alive(t, child) {
		t.Fatal("a child of the cancelled run is still running")
	}
}

func TestRunsGetASanitizedEnvironment(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env")
	t.Setenv("OPENAI_API_KEY", "sk-should-not-leak")
	t.Setenv("LIFE_OS_FINANCE_DB_PASSWORD", "secret")
	t.Setenv("CODEX_HOME", "/tmp/lifeos-codex")
	codex := fakeCodex(t, `env > `+envFile+`
printf '{}' > "$out"`)
	if _, err := (CodexCLI{Command: codex, WorkDir: dir}).Plan(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(envFile)
	env := string(raw)
	if strings.Contains(env, "sk-should-not-leak") || strings.Contains(env, "LIFE_OS_FINANCE_DB_PASSWORD") {
		t.Fatalf("leaked environment:\n%s", env)
	}
	if !strings.Contains(env, "CODEX_HOME=/tmp/lifeos-codex") || !strings.Contains(env, "PATH=") {
		t.Fatalf("missing required environment:\n%s", env)
	}
}

func TestPlanArgumentsKeepTheRunReadOnlyAndIsolated(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	path := filepath.Join(dir, "codex")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _ = CodexCLI{Command: path, WorkDir: dir, Model: "gpt-5.6-luna"}.Plan(context.Background(), "x")
	raw, _ := os.ReadFile(argsFile)
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	joined := strings.Join(args, " ")
	for _, want := range []string{"exec --json --ephemeral", "-s read-only", "-C " + dir, "-m gpt-5.6-luna", "-c mcp_servers={}"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args %q lack %q", joined, want)
		}
	}
}

func TestSlotsLimitConcurrentRuns(t *testing.T) {
	codex := fakeCodex(t, `printf '{}' > "$out"`)
	slots := make(chan struct{}, 1)
	slots <- struct{}{} // the only slot is taken
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := (CodexCLI{Command: codex, WorkDir: t.TempDir(), Slots: slots}).Plan(ctx, "x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want to wait for a slot", err)
	}
	<-slots
	if _, err := (CodexCLI{Command: codex, WorkDir: t.TempDir(), Slots: slots}).Plan(context.Background(), "x"); err != nil {
		t.Fatalf("free slot: %v", err)
	}
}

type tokenRecorder struct {
	recordingObserver
	usage []TokenUsage
}

func (r *tokenRecorder) Tokens(usage TokenUsage) { r.usage = append(r.usage, usage) }

func TestReasoningEffortIsPassedAndTokenUsageReported(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	codex := fakeCodex(t, `echo "$0" >/dev/null
printf '{}' > "$out"
echo '{"type":"turn.completed","usage":{"input_tokens":13000,"cached_input_tokens":11000,"output_tokens":42,"reasoning_output_tokens":7}}'`)
	// Record the arguments with a wrapper around the fake CLI.
	wrapper := filepath.Join(dir, "codex-args")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argsFile+"\nexec "+codex+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	observer := &tokenRecorder{}
	if _, err := (CodexCLI{Command: wrapper, WorkDir: dir, Model: "gpt-6-luna", ReasoningEffort: "low"}).Plan(WithRunObserver(context.Background(), observer), "x"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(argsFile)
	if !strings.Contains(strings.ReplaceAll(string(raw), "\n", " "), "-c model_reasoning_effort=low") {
		t.Fatalf("args lack the reasoning effort: %q", raw)
	}
	if len(observer.usage) != 1 || observer.usage[0] != (TokenUsage{Input: 13000, CachedInput: 11000, Output: 42, Reasoning: 7}) {
		t.Fatalf("usage = %+v", observer.usage)
	}
	// Without an effort, the Codex configuration's default applies.
	if _, err := (CodexCLI{Command: wrapper, WorkDir: dir, Model: "gpt-6-luna"}).Plan(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(argsFile)
	if strings.Contains(string(raw), "model_reasoning_effort") {
		t.Fatalf("an effort was passed without being configured: %q", raw)
	}
}
