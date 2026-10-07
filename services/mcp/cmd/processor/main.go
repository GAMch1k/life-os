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
	content := flag.String("content", "", "raw Inbox content to plan")
	flag.Parse()
	cfg, err := processor.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *content == "" {
		fmt.Fprintln(os.Stderr, "--content is required")
		os.Exit(2)
	}
	plan, err := (processor.ResponsesClient{APIKey: cfg.APIKey, Model: cfg.Model}).Plan(context.Background(), *content)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(plan)
}
