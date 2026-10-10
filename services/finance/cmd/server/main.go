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
	"github.com/GAMch1k/life-os/services/finance/internal/alerts"
	"github.com/GAMch1k/life-os/services/finance/internal/api"
	"github.com/GAMch1k/life-os/services/finance/internal/budget"
	serviceconfig "github.com/GAMch1k/life-os/services/finance/internal/config"
	repository "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
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

	// Budget alerts check the plan in the background and right after money
	// or a plan changes; the money alerts detect subscriptions and watch
	// renewals, large debits, and low balances the same way.
	notifier := notify.New(cfg.NotificationsURL, "finance")
	budgetAlerts := budget.NewAlerts(repository.New(pool), notifier,
		budget.AlertConfig{Owner: owner, Currency: cfg.DefaultCurrency}, logger)
	moneyAlerts := alerts.New(pool, notifier, alerts.Config{Owner: owner, Currency: cfg.DefaultCurrency}, logger)
	handler := api.NewHandler(pool, api.Options{
		Location:        owner,
		DefaultCurrency: cfg.DefaultCurrency,
		Activity:        activity.NewEmitter(cfg.ActivityURL, "finance", logger),
		BudgetChanged:   budgetAlerts.Wake,
		MoneyChanged:    moneyAlerts.Wake,
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
	// The alerts stop (finishing a check under way) before the pool closes:
	// shutdown hooks run in reverse order.
	background(app, "budget alerts", budgetAlerts.Run)
	background(app, "money alerts", moneyAlerts.Run)

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
