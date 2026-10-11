package main

import (
	"context"
	"fmt"
	"os"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/recipe-import/internal/api"
	serviceconfig "github.com/GAMch1k/life-os/services/recipe-import/internal/config"
	"github.com/GAMch1k/life-os/services/recipe-import/internal/fetch"
	"github.com/GAMch1k/life-os/services/recipe-import/internal/food"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "service startup failed: %v\n", err)
		os.Exit(1)
	}
}

func run(_ context.Context) error {
	cfg, err := serviceconfig.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	logger := httpserver.NewJSONLogger(os.Stdout, cfg.ParsedLogLevel())
	if hosts := cfg.AllowedHosts(); len(hosts) > 0 {
		logger.Warn("these hosts are fetched from any address (RECIPE_IMPORT_ALLOW_HOSTS); for the verification stack only", "hosts", hosts)
	}
	// No database: recipe-import keeps nothing; the recipes are Food's.
	app := httpserver.New(cfg.ServiceName, httpserver.WithLogger(logger))
	fetcher := fetch.New(int64(cfg.MaxBytes), cfg.Timeout(), cfg.AllowedHosts())
	api.Register(app, api.NewHandler(fetcher, food.New(cfg.FoodURL), activity.NewEmitter(cfg.ActivityURL, "recipe-import", logger), logger))

	return app.Run(fmt.Sprintf(":%d", cfg.Port))
}
