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
	handler := api.NewHandler(pool, ownerTimeZone).WithActivity(emitter)
	// Reminder delivery reads the events and occurrences as the API lists
	// them and sends every effective reminder through Notifications.
	dispatcher := reminders.New(pool, handler, notify.New(cfg.NotificationsURL, "calendar"), emitter, reminders.Config{
		Interval: time.Duration(cfg.ReminderIntervalSeconds) * time.Second,
		Grace:    time.Duration(cfg.ReminderGraceMinutes) * time.Minute,
		Owner:    owner,
	}, logger)
	api.Register(app, handler.WithReminders(dispatcher))
	app.OnShutdown(database.ClosePool(pool))

	// Reminder delivery and the change feed pruning stop (finishing a run
	// under way) before the pool closes: shutdown hooks run in reverse
	// order. Google Calendar and ICS feeds are integration services
	// (services/google-calendar, services/calendar-feeds) that reach
	// Calendar through its integration API.
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
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
