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
	"github.com/GAMch1k/life-os/services/food/internal/api"
	serviceconfig "github.com/GAMch1k/life-os/services/food/internal/config"
	"github.com/GAMch1k/life-os/services/food/internal/notifications"
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
	expiryTime, err := cfg.ExpiryClock()
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
	emitter := activity.NewEmitter(cfg.ActivityURL, "food", logger)
	// Expiry reminders read the pantry and send through Notifications.
	dispatcher := notifications.New(pool, notify.New(cfg.NotificationsURL, "food"), emitter, notifications.Config{
		Interval:   time.Duration(cfg.NotifyIntervalSeconds) * time.Second,
		Grace:      time.Duration(cfg.NotifyGraceMinutes) * time.Minute,
		Owner:      owner,
		ExpiryTime: expiryTime,
	}, logger)
	api.Register(app, api.NewHandler(pool, api.Options{Location: owner, Activity: emitter}).WithNotifications(dispatcher))
	app.OnShutdown(database.ClosePool(pool))
	// Expiry reminders stop (finishing a run under way) before the pool
	// closes: shutdown hooks run in reverse order.
	background(app, "expiry reminders", dispatcher.Run)

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
