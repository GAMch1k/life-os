package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	// The alpine runtime image has no zoneinfo; embed it so the owner's and
	// requests' IANA timezones resolve.
	_ "time/tzdata"

	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/database"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/calendar/internal/api"
	serviceconfig "github.com/GAMch1k/life-os/services/calendar/internal/config"
	"github.com/GAMch1k/life-os/services/calendar/internal/ics"
	"github.com/GAMch1k/life-os/services/calendar/internal/sources"
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
	ownerTimeZone, err := cfg.OwnerTimeZone()
	if err != nil {
		logger.Warn(err.Error())
	}
	owner, err := time.LoadLocation(ownerTimeZone)
	if err != nil {
		return fmt.Errorf("load owner timezone: %w", err)
	}
	schedule, err := sources.ParseSchedule(cfg.SourceRefreshAt, owner)
	if err != nil {
		return err
	}
	if cfg.ICSAllowPrivateURLs {
		logger.Warn("CALENDAR_ICS_ALLOW_PRIVATE_URLS is set: calendar feeds may use http and private addresses (development only)")
	}
	pool, err := database.Open(ctx, cfg.DatabaseConfig())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	app := httpserver.New(
		cfg.ServiceName,
		httpserver.WithLogger(logger),
		httpserver.WithReadinessCheck(pool.Ping),
	)
	fetcher := ics.NewFetcher(cfg.ICSMaxBytes, time.Duration(cfg.ICSTimeoutSeconds)*time.Second, cfg.ICSAllowPrivateURLs)
	emitter := activity.NewEmitter(cfg.ActivityURL, "calendar", logger)
	refresher := sources.NewRefresher(pool, fetcher, schedule, emitter, logger)
	scheduler := sources.NewScheduler(refresher, time.Minute, logger)
	// Before the scheduler starts, so a new or reconfigured ZAW source is
	// refreshed right away.
	ensureZAW(ctx, refresher, cfg.ZAWICSURL, logger)
	api.Register(app, api.NewHandler(pool, ownerTimeZone).WithSources(refresher, scheduler.Wake).WithActivity(emitter))
	app.OnShutdown(database.ClosePool(pool))

	// The scheduler stops (finishing a refresh under way) before the pool
	// closes: shutdown hooks run in reverse order.
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	schedulerCtx, stopScheduler := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		scheduler.Run(schedulerCtx)
	}()
	app.OnShutdown(func(shutdown context.Context) error {
		stopScheduler()
		select {
		case <-stopped:
			return nil
		case <-shutdown.Done():
			return fmt.Errorf("calendar source scheduler did not stop: %w", shutdown.Err())
		}
	})

	return app.RunContext(runCtx, fmt.Sprintf(":%d", cfg.Port))
}

// ensureZAW keeps the ZAW waste-collection source configured from
// CALENDAR_ZAW_ICS_URL; unset leaves an existing one as it is. A failure is
// logged, not fatal: Calendar serves without the source. The URL names a
// street and is never logged.
func ensureZAW(ctx context.Context, refresher *sources.Refresher, url string, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if strings.TrimSpace(url) == "" {
		existing, found, err := refresher.PresetSource(ctx, sources.PresetZAW)
		switch {
		case err != nil:
			logger.Warn("ZAW source not checked", "error", err)
		case found:
			logger.Info("CALENDAR_ZAW_ICS_URL is not set; the existing ZAW source is left as it is",
				"source", googleuuid.UUID(existing.ID.Bytes), "enabled", existing.Enabled)
		default:
			logger.Info("CALENDAR_ZAW_ICS_URL is not set; no ZAW source is configured")
		}
		return
	}
	result, err := refresher.EnsurePreset(ctx, sources.ZAWPreset(url))
	if err != nil {
		logger.Error("ZAW source not configured", "error", err)
		return
	}
	logger.Info("ZAW source ensured", "source", googleuuid.UUID(result.Source.ID.Bytes), "action", result.Action,
		"enabled", result.Source.Enabled, "refreshDue", result.Due)
}
