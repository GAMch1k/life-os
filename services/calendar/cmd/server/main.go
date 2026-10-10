package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
	// The alpine runtime image has no zoneinfo; embed it so the owner's and
	// requests' IANA timezones resolve.
	_ "time/tzdata"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/database"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/notify"
	"github.com/GAMch1k/life-os/services/calendar/internal/api"
	"github.com/GAMch1k/life-os/services/calendar/internal/changefeed"
	serviceconfig "github.com/GAMch1k/life-os/services/calendar/internal/config"
	"github.com/GAMch1k/life-os/services/calendar/internal/google"
	"github.com/GAMch1k/life-os/services/calendar/internal/reminders"
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
	pool, err := database.Open(ctx, cfg.DatabaseConfig())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	app := httpserver.New(
		cfg.ServiceName,
		httpserver.WithLogger(logger),
		httpserver.WithReadinessCheck(pool.Ping),
		// An integration's snapshot of a large calendar is one request.
		httpserver.WithFiberConfig(fiber.Config{BodyLimit: 16 << 20}),
	)
	// ICS feeds are refreshed by the calendar-feeds integration service
	// through the integration API; Calendar downloads nothing itself.
	emitter := activity.NewEmitter(cfg.ActivityURL, "calendar", logger)
	googleConfig, err := cfg.Google()
	switch {
	case err != nil:
		logger.Warn(err.Error())
	case !googleConfig.Configured():
		logger.Info("Google Calendar is not configured (LIFE_OS_GOOGLE_CLIENT_ID, LIFE_OS_GOOGLE_CLIENT_SECRET, LIFE_OS_GOOGLE_TOKEN_KEY)")
	}
	googleService := google.NewService(googleConfig, pool, google.GoogleEndpoints, emitter, logger)
	syncer := google.NewSyncer(googleService, owner, time.Duration(cfg.GoogleSyncSeconds)*time.Second, logger)
	handler := api.NewHandler(pool, ownerTimeZone).WithActivity(emitter).WithGoogle(googleService)
	// Reminder delivery reads the events and occurrences as the API lists
	// them and sends every effective reminder through Notifications.
	dispatcher := reminders.New(pool, handler, notify.New(cfg.NotificationsURL, "calendar"), emitter, reminders.Config{
		Interval: time.Duration(cfg.ReminderIntervalSeconds) * time.Second,
		Grace:    time.Duration(cfg.ReminderGraceMinutes) * time.Minute,
		Owner:    owner,
	}, logger)
	api.Register(app, handler.WithReminders(dispatcher))
	app.OnShutdown(database.ClosePool(pool))

	// The Google syncer, reminder delivery, and the change feed pruning stop
	// (finishing a sync or run under way) before the pool closes: shutdown
	// hooks run in reverse order.
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	background(app, "Google Calendar sync", syncer.Run)
	background(app, "calendar reminder delivery", dispatcher.Run)
	pruner := changefeed.NewPruner(pool, time.Duration(cfg.ChangeRetentionDays)*24*time.Hour, logger)
	background(app, "calendar change feed pruning", pruner.Run)

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
