package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/GAMch1k/life-os/services/mcp/internal/processor"
)

// The processor is deliberately a separate non-interactive process: scheduler,
// retry policy, and credentials are operational concerns, not domain handlers.
func main() {
	content := flag.String("content", "", "raw Inbox content to plan without executing (diagnostic)")
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
	if err := (processor.Runner{Planner: planner, Tools: &processor.MCPClient{URL: cfg.MCPURL}, Model: cfg.Model}).Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
