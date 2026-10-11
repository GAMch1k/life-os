package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	// The runtime image has no zoneinfo; embed it so the owner's timezone
	// resolves.
	_ "time/tzdata"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/backup/internal/api"
	"github.com/GAMch1k/life-os/services/backup/internal/backup"
	"github.com/GAMch1k/life-os/services/backup/internal/cli"
	serviceconfig "github.com/GAMch1k/life-os/services/backup/internal/config"
)

// catchUpDelay is how long a catch-up run waits after the service starts,
// so the rest of a deployment can come up first.
const catchUpDelay = time.Minute

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "now":
			os.Exit(cli.Now(context.Background(), os.Args[2:], os.Stdout, os.Stderr))
		case "restore-check":
			os.Exit(cli.RestoreCheck(context.Background(), os.Args[2:], os.Stdout, os.Stderr))
		}
	}
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "service startup failed: %v\n", err)
		os.Exit(1)
	}
}

// mediaService is the service whose media are backed up: Inbox's, when
// its media directory is mounted.
func mediaService(dir string) string {
	if dir == "" {
		return ""
	}
	return backup.MediaService
}

func run(ctx context.Context) error {
	cfg, err := serviceconfig.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	logger := httpserver.NewJSONLogger(os.Stdout, cfg.ParsedLogLevel())
	store, err := backup.OpenStore(cfg.Dir)
	if err == nil {
		err = store.Writable()
	}
	if err != nil {
		return fmt.Errorf("open the backup directory as uid %d (LIFE_OS_BACKUP_RUNTIME_UID must own it): %w", os.Getuid(), err)
	}
	if cfg.Connection().Password == "" {
		logger.Warn("no database password (BACKUP_DATABASE_PASSWORD): backups fail until LIFE_OS_BACKUP_DB_PASSWORD is set")
	}
	registry := func() ([]backup.Database, error) { return backup.LoadDatabases(cfg.DatabasesFile) }
	if databases, err := registry(); err != nil {
		logger.Warn("the database registry cannot be read", "file", cfg.DatabasesFile, "error", err)
	} else {
		logger.Info("backing up the registry's databases", "file", cfg.DatabasesFile, "databases", len(databases))
	}

	runner := backup.NewRunner(backup.RunnerOptions{
		Store: store,
		Dumper: &backup.Dumper{
			Connection:   cfg.Connection(),
			PgDump:       cfg.PgDump,
			PgRestore:    cfg.PgRestore,
			MediaService: mediaService(cfg.InboxMediaDir),
		},
		MediaSource: cfg.InboxMediaDir,
		Restorer: &backup.Restorer{
			Connection: cfg.Connection(),
			PgRestore:  cfg.PgRestore,
		},
		Registry:  registry,
		Retention: cfg.Retention(),
		Location:  cfg.Location(),
		Logger:    logger,
	})
	if err := runner.Recover(); err != nil {
		return fmt.Errorf("prepare the backup directory: %w", err)
	}
	checkDay, checks := cfg.CheckDay()
	scheduler := backup.NewScheduler(backup.SchedulerOptions{
		Runner:          runner,
		Schedule:        cfg.Schedule(),
		CatchUpDelay:    catchUpDelay,
		RestoreChecks:   checks,
		RestoreCheckDay: checkDay,
		Logger:          logger,
	})
	logger.Info("backups scheduled", "time", cfg.Schedule().Time(), "timezone", cfg.Location().String(), "dir", cfg.Dir, "restore_check_day", strings.ToLower(cfg.RestoreCheckDay))

	app := httpserver.New(
		cfg.ServiceName,
		httpserver.WithLogger(logger),
		httpserver.WithReadinessCheck(func(ctx context.Context) error {
			return errors.Join(store.Writable(), scheduler.Ready(ctx))
		}),
	)
	api.Register(app, api.NewHandler(runner, scheduler, api.Settings{
		Schedule:        cfg.Schedule(),
		Retention:       cfg.Retention(),
		RestoreCheckDay: strings.ToLower(strings.TrimSpace(cfg.RestoreCheckDay)),
	}))

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	schedulerCtx, stopScheduler := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		scheduler.Run(schedulerCtx)
	}()
	// Shutdown hooks run in reverse order: the scheduler stops first, then
	// a run in progress.
	app.OnShutdown(runner.Close)
	app.OnShutdown(func(shutdown context.Context) error {
		stopScheduler()
		select {
		case <-stopped:
			return nil
		case <-shutdown.Done():
			return fmt.Errorf("the backup scheduler did not stop: %w", shutdown.Err())
		}
	})

	return app.RunContext(runCtx, fmt.Sprintf(":%d", cfg.Port))
}
