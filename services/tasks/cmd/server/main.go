package main

import (
	"context"
	"fmt"
	"os"
	"time"
	// The alpine runtime image has no zoneinfo; embed it so the owner's
	// timezone resolves.
	_ "time/tzdata"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/database"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/notify"
	"github.com/GAMch1k/life-os/services/tasks/internal/api"
	serviceconfig "github.com/GAMch1k/life-os/services/tasks/internal/config"
	"github.com/GAMch1k/life-os/services/tasks/internal/notifications"
	repository "github.com/GAMch1k/life-os/services/tasks/internal/repository/generated"
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
	schedule, err := cfg.NotificationSchedule()
	if err != nil {
		return err
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
	emitter := activity.NewEmitter(cfg.ActivityURL, "tasks", logger)
	// Task notifications read the open Tasks and, for the morning summary,
	// the day's events from Calendar, and send through Notifications.
	dispatcher := notifications.New(pool, notify.New(cfg.NotificationsURL, "tasks"), notifications.NewCalendar(cfg.CalendarURL, owner), emitter, notifications.Config{
		Interval: time.Duration(cfg.NotifyIntervalSeconds) * time.Second,
		Grace:    time.Duration(cfg.NotifyGraceMinutes) * time.Minute,
		Owner:    owner,
		Schedule: schedule,
	}, logger)
	api.Register(app, api.NewHandler(repository.New(pool), emitter).WithNotifications(dispatcher))
	app.OnShutdown(database.ClosePool(pool))
	// Task notifications stop (finishing a run under way) before the pool
	// closes: shutdown hooks run in reverse order.
	background(app, "task notifications", dispatcher.Run)

	return app.Run(fmt.Sprintf(":%d", cfg.Port))
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
