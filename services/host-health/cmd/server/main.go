package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/host-health/internal/api"
	serviceconfig "github.com/GAMch1k/life-os/services/host-health/internal/config"
	"github.com/GAMch1k/life-os/services/host-health/internal/docker"
	"github.com/GAMch1k/life-os/services/host-health/internal/health"
	"github.com/GAMch1k/life-os/services/host-health/internal/host"
)

func main() {
	run := runServer
	if len(os.Args) > 1 && os.Args[1] == "docker-gateway" {
		run = runGateway
	}
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "service startup failed: %v\n", err)
		os.Exit(1)
	}
}

func runServer(ctx context.Context) error {
	cfg, err := serviceconfig.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger := httpserver.NewJSONLogger(os.Stdout, cfg.ParsedLogLevel())

	opts := health.Options{
		Mode:       "host",
		Thresholds: cfg.Thresholds(),
		Interval:   cfg.Interval(),
		Logger:     logger,
	}
	var prefill []health.Sample
	switch {
	case cfg.Fixture != "":
		fixture := health.Fixture{Scenario: cfg.Fixture}
		opts.Mode, opts.Host, opts.Docker = "fixture", fixture, fixture
		prefill = fixture.History(time.Now().UTC())
		logger.Warn("reporting a made-up host, not this one", "fixture", cfg.Fixture)
	default:
		opts.Host = host.Reader{ProcDir: cfg.ProcDir, Disks: cfg.DiskSpecs()}
		if cfg.DockerURL != "" {
			opts.Docker = docker.NewClient(cfg.DockerURL)
		} else {
			logger.Warn("HOST_HEALTH_DOCKER_URL is empty: no container facts")
		}
	}
	sampler := health.NewSampler(opts)
	sampler.Prefill(prefill)

	app := httpserver.New(
		cfg.ServiceName,
		httpserver.WithLogger(logger),
		httpserver.WithReadinessCheck(sampler.Ready),
	)
	api.Register(app, api.NewHandler(sampler, cfg.Interval()))

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	samplerCtx, stopSampler := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		sampler.Run(samplerCtx)
	}()
	app.OnShutdown(func(shutdown context.Context) error {
		stopSampler()
		select {
		case <-stopped:
			return nil
		case <-shutdown.Done():
			return fmt.Errorf("the sampler did not stop: %w", shutdown.Err())
		}
	})
	return app.RunContext(runCtx, fmt.Sprintf(":%d", cfg.Port))
}

// runGateway serves the read-only Docker gateway: the only process that
// opens the Docker socket (internal/docker).
func runGateway(ctx context.Context) error {
	cfg, err := serviceconfig.LoadGateway()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger := httpserver.NewJSONLogger(os.Stdout, cfg.ParsedLogLevel()).With("service", "docker-gateway")
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           docker.NewGateway(cfg.Socket, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	served := make(chan error, 1)
	go func() { served <- server.ListenAndServe() }()
	logger.Info("serving the read-only docker gateway", "socket", cfg.Socket)
	select {
	case err := <-served:
		return err
	case <-runCtx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
