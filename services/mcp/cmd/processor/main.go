package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
	// The owner's timezone must resolve even in an image without zoneinfo.
	_ "time/tzdata"

	"github.com/GAMch1k/life-os/services/mcp/internal/processor"
)

// The processor is deliberately a separate non-interactive process: scheduler,
// retry policy, and credentials are operational concerns, not domain handlers.
func main() {
	content := flag.String("content", "", "raw Inbox content to plan without executing (diagnostic)")
	dryRun := flag.Bool("dry-run", false, "print proposed Inbox MCP calls without mutations")
	serve := flag.Bool("serve", false, "run the managed automatic Inbox processor")
	usage := flag.Bool("usage", false, "print the account usage reported by the AI provider's CLI (diagnostic)")
	flag.Parse()
	cfg, err := processor.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, warning := range cfg.Warnings {
		fmt.Fprintln(os.Stderr, warning)
	}
	// Resolve the configured CLI once, so every run uses the same executable
	// and a missing CLI fails loudly at startup.
	binary, err := exec.LookPath(cfg.Command())
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s CLI %q not found: %v\n", cfg.Provider, cfg.Command(), err)
		os.Exit(78)
	}
	planner := newProvider(cfg, binary)
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
	runner := processor.Runner{Planner: planner, Tools: &processor.MCPClient{URL: cfg.MCPURL}, Model: cfg.Profile(), DryRun: *dryRun, TimeZone: cfg.TimeZone, Media: processor.InboxMedia{URL: cfg.InboxURL}, ContextMinutes: cfg.ContextMinutes,
		// A capture waits for the earlier captures of its context window at
		// most as long as an abandoned attempt takes to be recovered.
		Order: processor.NewCaptureOrder(cfg.StaleAfter),
		Logf:  func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }}
	if *serve {
		monitor := processor.NewMonitor(cfg.Model)
		runner.Owner = instanceID()
		monitor.ConfigureRuntime(runner.Owner, binary, cfg.CodexTimeout, cfg.MaxConcurrency)
		runner.OnRuntime = monitor.RecordRuntime
		runner.OnTokens = monitor.RecordTokens
		monitor.SetReasoningEffort(cfg.ReasoningEffort)
		monitor.SetTimeZone(cfg.TimeZone.String())
		runner.Planner = monitor.Monitored(planner)
		runner.Retry = processor.RetryPolicy{RecoveryAt: usageRecovery(planner, monitor)}
		if *dryRun {
			fmt.Fprintln(os.Stderr, "--serve and --dry-run cannot be used together")
			os.Exit(2)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		// Credentials can expire or be removed while the process runs.
		go monitor.WatchAuth(ctx, planner.LoggedIn, 5*time.Minute)
		// Return attempts abandoned by a processor that died to the queue.
		go monitor.WatchReconcile(ctx, func(ctx context.Context) (int, error) {
			return runner.Reconcile(ctx, cfg.StaleAfter)
		}, 30*time.Second)
		// Account usage and reset times, as reported by the Codex CLI (Claude
		// Code reports them with each run).
		go monitor.WatchUsage(ctx, planner.Usage, 5*time.Minute)
		// Recipe ideas from the pantry and the preferences, daily and on the
		// owner's request, through the same Codex runtime and MCP.
		ideas := &processor.IdeasJob{Generator: monitor.MonitoredIdeas(planner), Tools: &processor.MCPClient{URL: cfg.MCPURL}, TimeZone: cfg.TimeZone, DailyAt: cfg.IdeasAt, WebSearch: cfg.IdeasWebSearch, OnStatus: monitor.SetIdeas, Logf: func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }}
		go ideas.Loop(ctx)
		// The AI's categories for the transactions Finance's rules leave
		// without one, polled every few minutes, through the same runtime.
		categories := &processor.CategoriesJob{Chooser: monitor.MonitoredCategories(planner), Tools: &processor.MCPClient{URL: cfg.MCPURL}, Enabled: cfg.CategoriesEnabled, TimeZone: cfg.TimeZone, Retry: runner.Retry, OnStatus: monitor.SetCategories, Logf: func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }}
		go categories.Loop(ctx)
		// The AI's budget plans, asked for by the owner in Finance and on
		// the 1st of each month, through the same runtime.
		budgetPlans := &processor.BudgetPlanJob{Planner: monitor.MonitoredBudgetPlans(planner), Tools: &processor.MCPClient{URL: cfg.MCPURL}, Enabled: cfg.BudgetPlansEnabled, Monthly: cfg.BudgetPlanMonthly, TimeZone: cfg.TimeZone, Retry: runner.Retry, OnStatus: monitor.SetBudgetPlans, Logf: func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }}
		go budgetPlans.Loop(ctx)
		service := &processor.Service{Run: func(ctx context.Context) error {
			_, err := runner.Run(ctx)
			if ctx.Err() == nil {
				monitor.RecordPoll(err)
			}
			return err
		}, PollInterval: cfg.PollInterval, Monitor: monitor, Ideas: ideas}
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

// newProvider is the AI runtime LIFE_OS_AI_PROVIDER selects: the Codex CLI
// (the default) or the Claude Code CLI, with the same runtime controls.
func newProvider(cfg processor.Config, binary string) processor.Provider {
	slots := make(chan struct{}, cfg.MaxConcurrency)
	if cfg.Provider == processor.ProviderClaude {
		return processor.ClaudeCLI{Command: binary, Model: cfg.Model, Effort: cfg.ReasoningEffort, WorkDir: cfg.WorkDir, Timeout: cfg.CodexTimeout, Slots: slots, TimeZone: cfg.TimeZone, Limits: &processor.ClaudeLimits{}}
	}
	return processor.CodexCLI{Command: binary, Model: cfg.Model, ReasoningEffort: cfg.ReasoningEffort, WorkDir: cfg.WorkDir, Timeout: cfg.CodexTimeout, Slots: slots, TimeZone: cfg.TimeZone}
}

// usageRecovery reads when the exhausted usage limit resets, straight from
// the provider's CLI. A Codex result is reused for a minute so a batch of
// failing items does not start an app-server each; Claude Code's limits are
// kept in memory from its runs and always read fresh.
func usageRecovery(planner processor.Provider, monitor *processor.Monitor) func(context.Context) (time.Time, bool) {
	reuse := time.Minute
	if _, ok := planner.(processor.ClaudeCLI); ok {
		reuse = 0
	}
	var mu sync.Mutex
	var checked time.Time
	var at time.Time
	var ok bool
	return func(ctx context.Context) (time.Time, bool) {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(checked) > reuse {
			usage, err := planner.Usage(ctx)
			monitor.RecordUsage(usage, err)
			checked = time.Now()
			at, ok = time.Time{}, false
			if err == nil {
				at, ok = usage.UsageRecovery()
			}
		}
		return at, ok
	}
}

// instanceID names this processor run on attempt runtime records. It changes
// on every start, so a restarted processor reconciles its predecessor's
// abandoned attempts.
func instanceID() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s-%d", host, time.Now().UnixNano())
}
