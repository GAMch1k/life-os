package processor

import (
	"fmt"
	"os"
)

// Config keeps model selection operational rather than embedded in Inbox logic.
type Config struct {
	Model        string
	CodexCommand string
	InboxURL     string
	MCPURL       string
}

func LoadConfig() (Config, error) {
	c := Config{Model: env("LIFE_OS_CODEX_MODEL", "gpt-5.6-terra"), CodexCommand: env("LIFE_OS_CODEX_COMMAND", "codex"), InboxURL: env("INBOX_SERVICE_URL", "http://inbox:8080"), MCPURL: env("MCP_SERVICE_URL", "http://mcp:8080/mcp")}
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
