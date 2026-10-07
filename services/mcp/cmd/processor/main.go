package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/GAMch1k/life-os/services/mcp/internal/processor"
)

// The processor is deliberately a separate non-interactive process: scheduler,
// retry policy, and credentials are operational concerns, not domain handlers.
func main() {
	content := flag.String("content", "", "raw Inbox content to plan without executing (diagnostic)")
	dryRun := flag.Bool("dry-run", false, "print proposed Inbox MCP calls without mutations")
	flag.Parse()
	cfg, err := processor.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	planner := processor.CodexCLI{Command: cfg.CodexCommand, Model: cfg.Model, WorkDir: "."}
	if *content != "" {
		plan, err := planner.Plan(context.Background(), *content)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(plan)
		return
	}
	proposals, err := (processor.Runner{Planner: planner, Tools: &processor.MCPClient{URL: cfg.MCPURL}, Model: cfg.Model, DryRun: *dryRun}).Run(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *dryRun {
		_ = json.NewEncoder(os.Stdout).Encode(proposals)
	}
}
