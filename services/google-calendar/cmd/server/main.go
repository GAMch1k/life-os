package main

import (
	"context"
	"fmt"
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
)

func main() {
	if len(os.Args) > 1 {
		fmt.Fprintf(os.Stderr, "unknown command %q (the service runs without arguments)\n", os.Args[1])
		os.Exit(2)
	}
	if err := serve(context.Background()); err != nil {
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
