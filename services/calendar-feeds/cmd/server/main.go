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
	// the feeds' IANA timezones resolve.
	_ "time/tzdata"

	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/database"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/api"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/calendar"
	serviceconfig "github.com/GAMch1k/life-os/services/calendar-feeds/internal/config"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/feeds"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/ics"
	store "github.com/GAMch1k/life-os/services/calendar-feeds/internal/repository/generated"
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
	schedule, err := feeds.ParseSchedule(cfg.RefreshAt, owner)
	if err != nil {
		return err
	}
	if cfg.AllowPrivateURLs {
		logger.Warn("CALENDAR_FEEDS_ALLOW_PRIVATE_URLS is set: calendar feeds may use http and private addresses (development only)")
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
	fetcher := ics.NewFetcher(cfg.MaxBytes, time.Duration(cfg.TimeoutSeconds)*time.Second, cfg.AllowPrivateURLs)
	emitter := activity.NewEmitter(cfg.ActivityURL, "calendar-feeds", logger)
	refresher := feeds.NewRefresher(pool, fetcher, calendar.New(cfg.CalendarURL), schedule, emitter, logger)
	scheduler := feeds.NewScheduler(refresher, time.Minute, logger)
	// Before the scheduler starts, so a new or reconfigured ZAW feed is
	// registered and refreshed right away.
	ensureZAW(ctx, refresher, cfg.ZAWICSURL, logger)
	api.Register(app, api.NewHandler(store.New(pool), refresher, scheduler.Wake))
	app.OnShutdown(database.ClosePool(pool))

	// The scheduler stops (finishing a refresh under way) before the pool
	// closes: shutdown hooks run in reverse order.
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	background(app, "calendar feed scheduler", scheduler.Run)

	return app.RunContext(runCtx, fmt.Sprintf(":%d", cfg.Port))
}

// background runs work until the app shuts down, and waits for it then.
func background(app *httpserver.App, name string, work func(context.Context)) {
	workCtx, stopWork := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		work(workCtx)
	}()
	app.OnShutdown(func(shutdown context.Context) error {
		stopWork()
		select {
		case <-stopped:
			return nil
		case <-shutdown.Done():
			return fmt.Errorf("%s did not stop: %w", name, shutdown.Err())
		}
	})
}

// ensureZAW keeps the ZAW waste-collection feed configured from
// LIFE_OS_ZAW_ICS_URL; unset leaves an existing one as it is. A failure is
// logged, not fatal: the service runs without the feed. The URL names a
// street and is never logged.
func ensureZAW(ctx context.Context, refresher *feeds.Refresher, url string, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if strings.TrimSpace(url) == "" {
		existing, found, err := refresher.PresetFeed(ctx, feeds.PresetZAW)
		switch {
		case err != nil:
			logger.Warn("ZAW feed not checked", "error", err)
		case found:
			logger.Info("LIFE_OS_ZAW_ICS_URL is not set; the existing ZAW feed is left as it is",
				"feed", googleuuid.UUID(existing.ID.Bytes), "enabled", existing.Enabled)
		default:
			logger.Info("LIFE_OS_ZAW_ICS_URL is not set; no ZAW feed is configured")
		}
		return
	}
	result, err := refresher.EnsurePreset(ctx, feeds.ZAWPreset(url))
	if err != nil {
		logger.Error("ZAW feed not configured", "error", err)
		return
	}
	logger.Info("ZAW feed ensured", "feed", googleuuid.UUID(result.Feed.ID.Bytes), "action", result.Action,
		"enabled", result.Feed.Enabled, "refreshDue", result.Due, "registrationPending", result.Feed.RegisterPending)
}
