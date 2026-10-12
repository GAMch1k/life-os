package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/database"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/notifications/internal/api"
	serviceconfig "github.com/GAMch1k/life-os/services/notifications/internal/config"
	"github.com/GAMch1k/life-os/services/notifications/internal/delivery"
	repository "github.com/GAMch1k/life-os/services/notifications/internal/repository/generated"
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
	queries := repository.New(pool)

	// Web push works once its keys are set: the API subscribes devices and
	// sends test pushes with it, and the channel delivers when it is listed.
	var webPush *delivery.WebPushChannel
	pushConfig := delivery.WebPushConfig{
		PublicKey:  cfg.WebPushPublicKey,
		PrivateKey: cfg.WebPushPrivateKey,
		Subject:    cfg.WebPushSubjectOrDefault(),
		Store:      queries,
		Logger:     logger,
	}
	if pushConfig.Set() {
		if webPush, err = delivery.NewWebPush(pushConfig); err != nil {
			pool.Close()
			return fmt.Errorf("configure web push: %w", err)
		}
	}
	channels, err := delivery.Build(cfg.ChannelNames(), delivery.Settings{
		Logger: logger,
		Ntfy: delivery.NtfyConfig{
			URL:         cfg.NtfyURL,
			Topic:       cfg.NtfyTopic,
			Token:       cfg.NtfyToken,
			LinkBaseURL: cfg.LinkBaseURL,
		},
		WebPush: webPush,
	})
	if err != nil {
		pool.Close()
		return err
	}
	if len(channels) == 0 {
		logger.Info("no notification channels configured (NOTIFICATIONS_CHANNELS): notifications are stored and listed only")
	} else {
		logger.Info("notification channels configured", "channels", delivery.Names(channels))
	}

	app := httpserver.New(
		cfg.ServiceName,
		httpserver.WithLogger(logger),
		httpserver.WithReadinessCheck(pool.Ping),
	)
	emitter := activity.NewEmitter(cfg.ActivityURL, "notifications", logger)
	worker := delivery.NewWorker(queries, channels, delivery.Options{
		Policy: delivery.Policy{
			BaseDelay: serviceconfig.Seconds(cfg.RetryBaseSeconds),
			MaxDelay:  serviceconfig.Seconds(cfg.RetryMaxSeconds),
			MaxAge:    serviceconfig.Seconds(cfg.MaxAgeSeconds),
		},
		Poll:        time.Duration(cfg.WorkerPollMillis) * time.Millisecond,
		Lease:       serviceconfig.Seconds(cfg.LeaseSeconds),
		SendTimeout: serviceconfig.Seconds(cfg.SendTimeoutSeconds),
	}, emitter, logger)
	handler := api.NewHandler(pool, delivery.Names(channels)).WithWake(worker.Wake).WithActivity(emitter)
	if webPush != nil {
		handler = handler.WithPush(webPush)
	}
	api.Register(app, handler)
	app.OnShutdown(database.ClosePool(pool))

	// The worker stops (finishing a delivery under way) before the pool
	// closes: shutdown hooks run in reverse order.
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerCtx, stopWorker := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		worker.Run(workerCtx)
	}()
	app.OnShutdown(func(shutdown context.Context) error {
		stopWorker()
		select {
		case <-stopped:
			return nil
		case <-shutdown.Done():
			return fmt.Errorf("notification delivery worker did not stop: %w", shutdown.Err())
		}
	})

	return app.RunContext(runCtx, fmt.Sprintf(":%d", cfg.Port))
}
