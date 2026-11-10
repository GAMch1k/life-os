package processor

import "context"

// AI providers the processor can run (LIFE_OS_AI_PROVIDER).
const (
	// ProviderCodex runs the OpenAI Codex CLI on the owner's ChatGPT sign-in
	// (CodexCLI). It is the default.
	ProviderCodex = "codex"
	// ProviderClaude runs the Claude Code CLI (ClaudeCLI) on the owner's
	// Claude plan or Anthropic API key (Config.ClaudeAuth).
	ProviderClaude = "claude"
)

// How Claude Code signs in (Config.ClaudeAuth), as the owner chose in setup.
const (
	// ClaudeAuthPlan is the Claude plan: the token of `claude setup-token`.
	ClaudeAuthPlan = "plan"
	// ClaudeAuthAPIKey is an Anthropic API key, billed per use by Anthropic.
	ClaudeAuthAPIKey = "api_key"
)

// ProviderName is the provider's name as the owner reads it: on the Status
// page and in what the processor reports.
func ProviderName(provider string) string {
	if provider == ProviderClaude {
		return "Claude Code"
	}
	return "Codex"
}

// Provider is the agent runtime behind every AI job of the processor:
// capture planning, recipe ideas, transaction categories, and budget plans,
// plus the sign-in check and the account usage shown on /status and used to
// schedule retries after a usage limit.
type Provider interface {
	planner
	ideaGenerator
	categoryChooser
	budgetPlanner
	// LoggedIn reports whether the runtime is signed in, without using model
	// quota.
	LoggedIn(context.Context) bool
	// Usage reports the account's usage limits and when they reset.
	Usage(context.Context) (Usage, error)
}

var (
	_ Provider = CodexCLI{}
	_ Provider = ClaudeCLI{}
)
