package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/database"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/voice/internal/api"
	serviceconfig "github.com/GAMch1k/life-os/services/voice/internal/config"
	repository "github.com/GAMch1k/life-os/services/voice/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/voice/internal/storage"
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
	api.Register(app, api.NewHandler(repository.New(pool), storage.Files{Root: cfg.StorageDir}, time.Duration(cfg.RetentionHours)*time.Hour, cfg.MaxUploadBytes, activity.NewEmitter(cfg.ActivityURL, "voice", logger), cfg.WhisperURL))
	app.OnShutdown(database.ClosePool(pool))

	return app.Run(fmt.Sprintf(":%d", cfg.Port))
}
