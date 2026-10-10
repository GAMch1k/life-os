package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/notify"
	serviceconfig "github.com/GAMch1k/life-os/services/monitor/internal/config"
	"github.com/GAMch1k/life-os/services/monitor/internal/monitor"
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
	// The monitor notifies as system:monitor, so its notifications have the
	// source "monitor", which is also where it reads its own history.
	notifier := notify.New(cfg.NotificationsURL, "monitor")
	if !notifier.Enabled() {
		logger.Warn("NOTIFICATIONS_SERVICE_URL is empty: status changes are logged, not notified")
	}
	logger.Info("monitoring the runtime status",
		"status_url", cfg.StatusURL,
		"interval", cfg.Interval().String(),
		"confirmations", cfg.Confirmations,
	)
	statusTimeout := min(cfg.Interval(), monitor.DefaultStatusTimeout)
	mon := monitor.New(monitor.Options{
		Status:        &monitor.StatusClient{URL: cfg.StatusURL, Timeout: statusTimeout},
		Notifier:      notifier,
		Interval:      cfg.Interval(),
		Confirmations: cfg.Confirmations,
		Logger:        logger,
	})

	app := httpserver.New(
		cfg.ServiceName,
		httpserver.WithLogger(logger),
		httpserver.WithReadinessCheck(mon.Ready),
	)

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	loopCtx, stopLoop := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		mon.Run(loopCtx)
	}()
	app.OnShutdown(func(shutdown context.Context) error {
		stopLoop()
		select {
		case <-stopped:
			return nil
		case <-shutdown.Done():
			return fmt.Errorf("status monitor did not stop: %w", shutdown.Err())
		}
	})

	return app.RunContext(runCtx, fmt.Sprintf(":%d", cfg.Port))
}
