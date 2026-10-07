package main

import (
	"fmt"
	"github.com/GAMch1k/life-os/services/mcp/internal/processor"
	"os"
)

// The processor is deliberately a separate non-interactive process: scheduler,
// retry policy, and credentials are operational concerns, not domain handlers.
func main() {
	cfg, err := processor.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Printf("Codex Inbox processor configured with model profile %s and MCP endpoint %s\n", cfg.Model, cfg.MCPURL)
}
