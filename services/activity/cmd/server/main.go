package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/GAMch1k/life-os/pkg/database"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/activity/internal/api"
	"github.com/GAMch1k/life-os/services/activity/internal/api/generated"
	serviceconfig "github.com/GAMch1k/life-os/services/activity/internal/config"
	repository "github.com/GAMch1k/life-os/services/activity/internal/repository/generated"
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
	)
	handler := api.NewHandler(repository.New(pool))
	generated.RegisterHandlers(app, handler)
	// Diagnostics older than 90 days go at start and once a day, also
	// while no service sends any.
	pruning, stopPruning := context.WithCancel(ctx)
	pruned := make(chan struct{})
	go func() {
		defer close(pruned)
		handler.RunDiagnosticsPruner(pruning, 24*time.Hour, app.Logger())
	}()
	// Shutdown hooks run last-registered first: the pruner stops (and its
	// query ends) before the pool closes.
	app.OnShutdown(database.ClosePool(pool))
	app.OnShutdown(func(shutdown context.Context) error {
		stopPruning()
		select {
		case <-pruned:
		case <-shutdown.Done():
		}
		return nil
	})

	return app.Run(fmt.Sprintf(":%d", cfg.Port))
}
