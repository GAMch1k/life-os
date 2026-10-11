package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/database"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/notify"
	"github.com/GAMch1k/life-os/services/inbox/internal/api"
	serviceconfig "github.com/GAMch1k/life-os/services/inbox/internal/config"
	"github.com/GAMch1k/life-os/services/inbox/internal/media"
	repository "github.com/GAMch1k/life-os/services/inbox/internal/repository/generated"
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
	pool, err := database.Open(ctx, cfg.DatabaseConfig())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	app := httpserver.New(
		cfg.ServiceName,
		httpserver.WithLogger(logger),
		httpserver.WithReadinessCheck(pool.Ping),
		// Room for an upload of five capture images.
		httpserver.WithFiberConfig(fiber.Config{BodyLimit: cfg.RequestBodyLimit()}),
	)
	emitter := activity.NewEmitter(cfg.ActivityURL, "inbox", logger)
	notifier := api.NewNotifier(notify.New(cfg.NotificationsURL, "inbox"), emitter, logger)
	handler := api.NewHandler(repository.New(pool), emitter, notifier).WithMedia(media.Store{Root: cfg.MediaDir}, cfg.MaxImageBytes)
	api.Register(app, handler)
	app.OnShutdown(database.ClosePool(pool))
	// Notifications still being sent finish (each within its timeout) before
	// the process exits.
	app.OnShutdown(notifier.Wait)
	// Images no capture refers to are removed a day after their upload. The
	// loop stops before the pool closes: shutdown hooks run in reverse order.
	app.OnShutdown(pruneMedia(handler, logger))

	return app.Run(fmt.Sprintf(":%d", cfg.Port))
}

// pruneMedia removes orphaned capture images every hour (and once at
// startup) until shutdown, which the returned hook waits for.
func pruneMedia(handler *api.Handler, logger *slog.Logger) func(context.Context) error {
	ctx, stop := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if removed, err := handler.PruneMedia(ctx, time.Now()); err != nil && ctx.Err() == nil {
				logger.Warn("capture media not pruned", "error", err)
			} else if removed > 0 {
				logger.Info("pruned orphaned capture media", "removed", removed)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func(shutdown context.Context) error {
		stop()
		select {
		case <-stopped:
			return nil
		case <-shutdown.Done():
			return fmt.Errorf("capture media pruning did not stop: %w", shutdown.Err())
		}
	}
}
