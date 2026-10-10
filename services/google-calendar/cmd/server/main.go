package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	// The alpine runtime image has no zoneinfo; embed it so the owner's and
	// Google's IANA timezones resolve.
	_ "time/tzdata"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/database"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/api"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/calendar"
	serviceconfig "github.com/GAMch1k/life-os/services/google-calendar/internal/config"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/google"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/handoff"
)

func main() {
	run := serve
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "handoff":
			run = runHandoff
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q (the service runs without arguments; `handoff` copies the Google connection from Calendar once)\n", os.Args[1])
			os.Exit(2)
		}
	}
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "google-calendar: %v\n", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context) error {
	cfg, err := serviceconfig.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	logger := httpserver.NewJSONLogger(os.Stdout, cfg.ParsedLogLevel())
	googleConfig, err := cfg.Google()
	switch {
	case err != nil:
		logger.Warn(err.Error())
	case !googleConfig.Configured():
		logger.Info("Google Calendar is not configured (LIFE_OS_GOOGLE_CLIENT_ID, LIFE_OS_GOOGLE_CLIENT_SECRET, LIFE_OS_GOOGLE_TOKEN_KEY)")
	}
	if cfg.CalendarURL == "" {
		logger.Warn("CALENDAR_SERVICE_URL is not set: nothing can be synced with Calendar")
	}
	pool, err := database.Open(ctx, cfg.DatabaseConfig())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	emitter := activity.NewEmitter(cfg.ActivityURL, calendar.Integration, logger)
	service := google.NewService(googleConfig, pool, cfg.Endpoints(), calendar.New(cfg.CalendarURL), emitter, logger)
	syncer := google.NewSyncer(service, cfg.Location(), time.Duration(cfg.SyncSeconds)*time.Second, time.Duration(cfg.FeedSeconds)*time.Second, logger)

	app := httpserver.New(
		cfg.ServiceName,
		httpserver.WithLogger(logger),
		httpserver.WithReadinessCheck(pool.Ping),
	)
	api.Register(app, api.NewHandler(service))
	app.OnShutdown(database.ClosePool(pool))

	// The syncer stops (finishing a sync under way, unless shutdown runs
	// out of time) before the pool closes: shutdown hooks run in reverse
	// order.
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	syncCtx, stopSync := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		syncer.Run(syncCtx)
	}()
	app.OnShutdown(func(shutdown context.Context) error {
		stopSync()
		select {
		case <-stopped:
			return nil
		case <-shutdown.Done():
			return fmt.Errorf("Google Calendar sync did not stop: %w", shutdown.Err())
		}
	})

	return app.RunContext(runCtx, fmt.Sprintf(":%d", cfg.Port))
}

// runHandoff copies the Google connection Calendar kept (before P11-07)
// from Calendar's database (HANDOFF_CALENDAR_DATABASE_URL, read only) into
// this service's (DATABASE_URL), and prints what it copied: counts only.
func runHandoff(ctx context.Context) error {
	cfg, err := serviceconfig.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if cfg.HandoffCalendarDatabaseURL == "" {
		return errors.New("set HANDOFF_CALENDAR_DATABASE_URL to Calendar's database (postgres://calendar_user:...@postgres:5432/calendar_db); it is only read")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	legacy, err := handoff.OpenLegacy(ctx, cfg.HandoffCalendarDatabaseURL)
	if err != nil {
		return err
	}
	defer legacy.Close()
	target, err := database.Open(ctx, cfg.DatabaseConfig())
	if err != nil {
		return fmt.Errorf("open google_calendar_db: %w", err)
	}
	defer target.Close()
	counts, err := handoff.Run(ctx, legacy, target)
	if err != nil {
		return fmt.Errorf("handoff: %w", err)
	}
	fmt.Println("Google Calendar handoff from Calendar's database:")
	for _, line := range counts.Lines() {
		fmt.Println("  " + line)
	}
	logger.Info("handoff done; start google-calendar")
	return nil
}
