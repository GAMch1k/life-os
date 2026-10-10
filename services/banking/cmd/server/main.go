package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/database"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/notify"
	"github.com/GAMch1k/life-os/services/banking/internal/api"
	"github.com/GAMch1k/life-os/services/banking/internal/banking"
	serviceconfig "github.com/GAMch1k/life-os/services/banking/internal/config"
	"github.com/GAMch1k/life-os/services/banking/internal/finance"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "service startup failed: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := serviceconfig.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	logger := httpserver.NewJSONLogger(os.Stdout, cfg.ParsedLogLevel())
	settings, settingsErr := banking.LoadSettings(cfg.EnableBankingAppID, cfg.EnableBankingKeyFile, cfg.TokenKey, cfg.RedirectURI, cfg.EnableBankingAPIURL)
	setupProblem := ""
	switch {
	case settingsErr != nil:
		setupProblem = settingsErr.Error()
		logger.Warn("bank connections are not configured", "problem", setupProblem)
	case !settings.Configured():
		logger.Info("bank connections are not configured (" + banking.EnvAppID + ", " + banking.EnvKeyFile + ", " + banking.EnvTokenKey + ")")
	default:
		logger.Info("bank connections configured", "provider", banking.ProviderEnableBanking, "redirect", settings.RedirectURI)
	}
	if cfg.FinanceURL == "" {
		logger.Warn("FINANCE_SERVICE_URL is not set: syncs cannot be delivered")
	}
	pool, err := database.Open(ctx, cfg.DatabaseConfig())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	emitter := activity.NewEmitter(cfg.ActivityURL, "banking", logger)
	service := banking.New(settings, setupProblem, pool, finance.New(cfg.FinanceURL), notify.New(cfg.NotificationsURL, "banking"), emitter, cfg.Options(), logger)

	app := httpserver.New(
		cfg.ServiceName,
		httpserver.WithLogger(logger),
		httpserver.WithReadinessCheck(pool.Ping),
	)
	api.Register(app, api.NewHandler(service, cfg.Location().String()))
	app.OnShutdown(database.ClosePool(pool))

	// The scheduler stops (after a sync under way, unless shutdown runs out
	// of time) before the pool closes: shutdown hooks run in reverse order.
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	schedulerCtx, stopScheduler := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		service.Run(schedulerCtx, time.Duration(cfg.TickSeconds)*time.Second)
	}()
	app.OnShutdown(func(shutdown context.Context) error {
		stopScheduler()
		select {
		case <-stopped:
			return nil
		case <-shutdown.Done():
			return fmt.Errorf("bank sync scheduler did not stop: %w", shutdown.Err())
		}
	})

	return app.RunContext(runCtx, fmt.Sprintf(":%d", cfg.Port))
}
