package processor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CodexCLI uses the machine's existing Codex/ChatGPT authentication. It keeps
// the processor out of the API-key and direct-model credential business.
type CodexCLI struct {
	Command, Model, WorkDir string
	Run                     func(context.Context, string, ...string) *exec.Cmd
}

func (c CodexCLI) Plan(ctx context.Context, content string) (string, error) {
	file, err := os.CreateTemp("", "life-os-codex-plan-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	_ = file.Close()
	defer os.Remove(name)
	cmd := c.runner()(ctx, c.command(), "exec", "--ephemeral", "--skip-git-repo-check", "-s", "read-only", "-C", c.WorkDir, "-m", c.Model, "-o", name, "Classify this Inbox item. Return only JSON in this schema: {\"actions\":[{\"tool\":\"task_create|task_update|task_complete|task_reopen|calendar_create|finance_create_manual_transaction\",\"arguments\":{...}}],\"ambiguous\":boolean,\"confidence\":0.0,\"reason\":string}. Existing Task mutations require a precise taskQuery (the identifying title/phrase) which the processor will search. If identity is unclear, set ambiguous true and actions to []. task_update may only use taskQuery, title, notes, dueAt (RFC3339), clearDueAt. task_complete/task_reopen may only use taskQuery. Never create a Task as fallback for an uncertain existing Task. task_create arguments may only use title, notes, dueAt (RFC3339). calendar_create arguments may only use title, description, allDay, startAt/endAt (RFC3339) or startDate/endDate (ISO date). finance_create_manual_transaction may only use accountId, categoryId, amount ({amount signed integer,currency}), occurredOn (ISO date), note; only propose it if accountId, amount, currency, and date are explicit in trusted source metadata or content. Never claim an action succeeded or execute an action. If information is materially unclear or confidence is below 0.8, set ambiguous true and actions to []. Never propose calendar update or deletion. Inbox item: "+content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("run Codex CLI: %w: %s", err, codexFailure(out))
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// codexFailure keeps the CLI's own ERROR lines. The full output also echoes
// the banner and the prompt (including the user's capture), which buries the
// reason, such as a usage limit, behind text that gets truncated.
func codexFailure(out []byte) string {
	var errs []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ERROR:") && !seen[line] {
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
	return c.runner()(ctx, c.command(), "login", "status").Run() == nil
}

func (c CodexCLI) command() string {
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
func (c CodexCLI) valid() { _ = filepath.Clean(c.WorkDir) }
