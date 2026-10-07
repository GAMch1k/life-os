package processor

import (
	"fmt"
	"os"
)

// Config keeps model selection operational rather than embedded in Inbox logic.
type Config struct {
	Model    string
	APIKey   string
	InboxURL string
	MCPURL   string
}

func LoadConfig() (Config, error) {
	c := Config{Model: env("LIFE_OS_CODEX_MODEL", "gpt-5.6-mini"), APIKey: os.Getenv("OPENAI_API_KEY"), InboxURL: env("INBOX_SERVICE_URL", "http://inbox:8080"), MCPURL: env("MCP_SERVICE_URL", "http://mcp:8080/mcp")}
	if c.APIKey == "" {
		return c, fmt.Errorf("OPENAI_API_KEY is required for Codex Inbox processing")
	}
	return c, nil
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
