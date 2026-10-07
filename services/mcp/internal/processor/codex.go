package processor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	cmd := c.runner()(ctx, c.command(), "exec", "--ephemeral", "-s", "read-only", "-C", c.WorkDir, "-m", c.Model, "-o", name, "Classify this Inbox item. Return only JSON in this schema: {\"actions\":[{\"tool\":\"task_create|calendar_create|finance_create_manual_transaction\",\"arguments\":{...}}],\"ambiguous\":boolean,\"confidence\":0.0,\"reason\":string}. task_create arguments may only use title, notes, dueAt (RFC3339). calendar_create arguments may only use title, description, allDay, startAt/endAt (RFC3339) or startDate/endDate (ISO date). finance_create_manual_transaction may only use accountId, categoryId, amount ({amount signed integer,currency}), occurredOn (ISO date), note; only propose it if accountId, amount, currency, and date are explicit in trusted source metadata or content. Never claim an action succeeded or execute an action. If information is materially unclear or confidence is below 0.8, set ambiguous true and actions to []. Never propose calendar update or deletion. Inbox item: "+content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("run Codex CLI: %w: %s", err, string(out))
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	return string(raw), nil
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
