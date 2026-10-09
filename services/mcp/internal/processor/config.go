package processor

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"
)

// Config keeps model selection operational rather than embedded in Inbox logic.
type Config struct {
	Model        string
	CodexCommand string
	InboxURL     string
	MCPURL       string
	WorkDir      string
	PollInterval time.Duration
	HealthPort   string
	// CodexTimeout bounds one planning run.
	CodexTimeout time.Duration
	// MaxConcurrency limits simultaneous Codex planning runs.
	MaxConcurrency int
	// StaleAfter is the heartbeat age after which an unfinished attempt is
	// considered abandoned and reconciled.
	StaleAfter time.Duration
	// ReasoningEffort is passed to Codex as model_reasoning_effort when set;
	// empty keeps the Codex configuration's default.
	ReasoningEffort string
}

// Profile names the model and reasoning effort, for example
// "gpt-6-luna/low", as recorded on processing attempts.
func (c Config) Profile() string {
	if c.ReasoningEffort == "" {
		return c.Model
	}
	return c.Model + "/" + c.ReasoningEffort
}

func LoadConfig() (Config, error) {
	pollMillis, err := strconv.Atoi(env("LIFE_OS_PROCESSOR_POLL_MILLIS", "1000"))
	if err != nil || pollMillis < 100 {
		return Config{}, fmt.Errorf("LIFE_OS_PROCESSOR_POLL_MILLIS must be an integer of at least 100")
	}
	c := Config{Model: env("LIFE_OS_CODEX_MODEL", "gpt-5.6-luna"), CodexCommand: env("LIFE_OS_CODEX_COMMAND", "codex"), InboxURL: env("INBOX_SERVICE_URL", "http://inbox:8080"), MCPURL: env("MCP_SERVICE_URL", "http://mcp:8080/mcp"), WorkDir: env("LIFE_OS_CODEX_WORKDIR", "."), PollInterval: time.Duration(pollMillis) * time.Millisecond, HealthPort: env("LIFE_OS_PROCESSOR_HEALTH_PORT", "8081")}
	timeout, err := strconv.Atoi(env("LIFE_OS_CODEX_TIMEOUT_SECONDS", "300"))
	if err != nil || timeout < 1 {
		return c, fmt.Errorf("LIFE_OS_CODEX_TIMEOUT_SECONDS must be a positive integer")
	}
	concurrency, err := strconv.Atoi(env("LIFE_OS_CODEX_MAX_CONCURRENCY", "1"))
	if err != nil || concurrency < 1 {
		return c, fmt.Errorf("LIFE_OS_CODEX_MAX_CONCURRENCY must be a positive integer")
	}
	stale, err := strconv.Atoi(env("LIFE_OS_PROCESSOR_STALE_AFTER_SECONDS", "120"))
	if err != nil || stale < 90 {
		return c, fmt.Errorf("LIFE_OS_PROCESSOR_STALE_AFTER_SECONDS must be an integer of at least 90 (three heartbeats)")
	}
	c.CodexTimeout, c.MaxConcurrency, c.StaleAfter = time.Duration(timeout)*time.Second, concurrency, time.Duration(stale)*time.Second
	c.ReasoningEffort = os.Getenv("LIFE_OS_CODEX_REASONING_EFFORT")
	if !effortPattern.MatchString(c.ReasoningEffort) {
		return c, fmt.Errorf("LIFE_OS_CODEX_REASONING_EFFORT must be a lowercase effort name such as none, low, or medium")
	}
	if c.CodexCommand == "" {
		return c, fmt.Errorf("LIFE_OS_CODEX_COMMAND must not be empty")
	}
	return c, nil
}

var effortPattern = regexp.MustCompile(`^[a-z]*$`)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
