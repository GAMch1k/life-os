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
	"github.com/GAMch1k/life-os/services/finance/internal/api"
	"github.com/GAMch1k/life-os/services/finance/internal/budget"
	serviceconfig "github.com/GAMch1k/life-os/services/finance/internal/config"
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
	owner, err := cfg.OwnerLocation()
	if err != nil {
		logger.Warn("owner timezone", "error", err)
	}
	pool, err := database.Open(ctx, cfg.DatabaseConfig())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	handler := api.NewHandler(pool, api.Options{
		Location:        owner,
		DefaultCurrency: cfg.DefaultCurrency,
		Activity:        activity.NewEmitter(cfg.ActivityURL, "finance", logger),
	})
	// The built-in Cash account exists before the first request: a manual
	// transaction without an account goes there. The default categories
	// and merchant rules are added once.
	ensureCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	cash, err := handler.EnsureCashAccount(ensureCtx)
	if err == nil {
		var seeded budget.SeedResult
		if seeded, err = handler.EnsureBudgetDefaults(ensureCtx); err == nil && seeded.Applied {
			logger.Info("default budget categories and merchant rules added", "categoriesCreated", seeded.CategoriesCreated,
				"categoriesAdopted", seeded.CategoriesAdopted, "categoriesSkipped", seeded.CategoriesSkipped, "rulesCreated", seeded.RulesCreated)
		}
	}
	cancel()
	if err != nil {
		pool.Close()
		return fmt.Errorf("ensure the Cash account and budget defaults: %w", err)
	}
	logger.Info("built-in Cash account ready", "accountId", cash.ID.String(), "currency", cash.Currency)

	app := httpserver.New(
		cfg.ServiceName,
		httpserver.WithLogger(logger),
		httpserver.WithReadinessCheck(pool.Ping),
	)
	api.Register(app, handler)
	app.OnShutdown(database.ClosePool(pool))

	return app.Run(fmt.Sprintf(":%d", cfg.Port))
}
