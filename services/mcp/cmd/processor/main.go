package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GAMch1k/life-os/services/mcp/internal/processor"
)

// The processor is deliberately a separate non-interactive process: scheduler,
// retry policy, and credentials are operational concerns, not domain handlers.
func main() {
	content := flag.String("content", "", "raw Inbox content to plan without executing (diagnostic)")
	dryRun := flag.Bool("dry-run", false, "print proposed Inbox MCP calls without mutations")
	serve := flag.Bool("serve", false, "run the managed automatic Inbox processor")
	usage := flag.Bool("usage", false, "print the Codex account usage reported by the Codex CLI (diagnostic)")
	flag.Parse()
	cfg, err := processor.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	planner := processor.CodexCLI{Command: cfg.CodexCommand, Model: cfg.Model, WorkDir: cfg.WorkDir}
	if *usage {
		report, err := planner.Usage(context.Background())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(report)
		return
	}
	if *content != "" {
		plan, err := planner.Plan(context.Background(), *content)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(plan)
		return
	}
	runner := processor.Runner{Planner: planner, Tools: &processor.MCPClient{URL: cfg.MCPURL}, Model: cfg.Model, DryRun: *dryRun}
	if *serve {
		monitor := processor.NewMonitor(cfg.Model)
		runner.Planner = monitor.Monitored(planner)
		if *dryRun {
			fmt.Fprintln(os.Stderr, "--serve and --dry-run cannot be used together")
			os.Exit(2)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		// Credentials can expire or be removed while the process runs.
		go monitor.WatchAuth(ctx, planner.LoggedIn, 5*time.Minute)
		// Account usage and reset times, as reported by the Codex CLI.
		go monitor.WatchUsage(ctx, planner.Usage, 5*time.Minute)
		service := &processor.Service{Run: func(ctx context.Context) error {
			_, err := runner.Run(ctx)
			if ctx.Err() == nil {
				monitor.RecordPoll(err)
			}
			return err
		}, PollInterval: cfg.PollInterval, Monitor: monitor}
		if err := service.Serve(ctx, ":"+cfg.HealthPort); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	proposals, err := runner.Run(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *dryRun {
		_ = json.NewEncoder(os.Stdout).Encode(proposals)
	}
}
