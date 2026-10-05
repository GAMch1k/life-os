package httpserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/gofiber/fiber/v3"
)

// OnShutdown registers a cleanup hook. Hooks run in reverse registration order
// after Fiber has stopped accepting connections and drained active requests.
func (a *App) OnShutdown(hook ShutdownHook) {
	if hook == nil {
		return
	}
	a.hooksMu.Lock()
	defer a.hooksMu.Unlock()
	a.hooks = append(a.hooks, hook)
}

// Run serves until SIGINT or SIGTERM, then performs a graceful shutdown.
func (a *App) Run(address string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.RunContext(ctx, address)
}

// RunContext serves until ctx is canceled or Fiber stops. It is useful for
// supervisors and tests which already own the process lifecycle.
func (a *App) RunContext(ctx context.Context, address string) error {
	if ctx == nil {
		return errors.New("httpserver: run context must not be nil")
	}

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- a.App.Listen(address, fiber.ListenConfig{DisableStartupMessage: true})
	}()

	select {
	case err := <-serveResult:
		return err
	case <-ctx.Done():
	}

	a.ready.Store(false)
	a.logger.Info("graceful shutdown started")
	shutdownErr := a.App.ShutdownWithTimeout(a.shutdownTimeout)
	hooksErr := a.runShutdownHooks()
	serveErr := <-serveResult
	err := errors.Join(shutdownErr, hooksErr, serveErr)
	if err != nil {
		a.logger.Error("graceful shutdown failed", "error", err)
		return fmt.Errorf("httpserver: graceful shutdown: %w", err)
	}
	a.logger.Info("graceful shutdown complete")
	return nil
}

func (a *App) runShutdownHooks() error {
	a.hooksMu.Lock()
	hooks := slices.Clone(a.hooks)
	a.hooksMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
	defer cancel()

	var result error
	for i := len(hooks) - 1; i >= 0; i-- {
		if err := hooks[i](ctx); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}
