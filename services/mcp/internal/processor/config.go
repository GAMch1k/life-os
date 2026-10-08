package processor

import (
	"fmt"
	"os"
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
}

func LoadConfig() (Config, error) {
	pollMillis, err := strconv.Atoi(env("LIFE_OS_PROCESSOR_POLL_MILLIS", "1000"))
	if err != nil || pollMillis < 100 {
		return Config{}, fmt.Errorf("LIFE_OS_PROCESSOR_POLL_MILLIS must be an integer of at least 100")
	}
	c := Config{Model: env("LIFE_OS_CODEX_MODEL", "gpt-5.6-terra"), CodexCommand: env("LIFE_OS_CODEX_COMMAND", "codex"), InboxURL: env("INBOX_SERVICE_URL", "http://inbox:8080"), MCPURL: env("MCP_SERVICE_URL", "http://mcp:8080/mcp"), WorkDir: env("LIFE_OS_CODEX_WORKDIR", "."), PollInterval: time.Duration(pollMillis) * time.Millisecond, HealthPort: env("LIFE_OS_PROCESSOR_HEALTH_PORT", "8081")}
	if c.CodexCommand == "" {
		return c, fmt.Errorf("LIFE_OS_CODEX_COMMAND must not be empty")
	}
	return c, nil
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
