package httpserver

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
)

const (
	// HealthPath is the process liveness endpoint.
	HealthPath = "/health"
	// ReadyPath is the traffic readiness endpoint.
	ReadyPath = "/ready"

	defaultShutdownTimeout = 10 * time.Second
)

// ReadinessCheck verifies that a dependency needed to serve traffic is ready.
type ReadinessCheck func(context.Context) error

// ShutdownHook releases a resource during graceful shutdown.
type ShutdownHook func(context.Context) error

// App embeds Fiber so routes and all other normal Fiber APIs remain directly
// available to services.
type App struct {
	*fiber.App

	logger          *slog.Logger
	shutdownTimeout time.Duration
	readiness       ReadinessCheck
	ready           atomic.Bool

	hooksMu sync.Mutex
	hooks   []ShutdownHook
}

type settings struct {
	logger          *slog.Logger
	fiberConfig     fiber.Config
	readiness       ReadinessCheck
	shutdownTimeout time.Duration
	idGenerator     func() string
}

// Option customizes the runtime without hiding Fiber behind an abstraction.
type Option func(*settings)

// WithLogger uses logger for request, panic, and lifecycle events.
func WithLogger(logger *slog.Logger) Option {
	return func(s *settings) {
		if logger != nil {
			s.logger = logger
		}
	}
}

// WithFiberConfig applies a native Fiber configuration. The runtime owns the
// ErrorHandler field so every service retains the common error envelope.
func WithFiberConfig(config fiber.Config) Option {
	return func(s *settings) { s.fiberConfig = config }
}

// WithReadinessCheck adds a dependency check to /ready.
func WithReadinessCheck(check ReadinessCheck) Option {
	return func(s *settings) { s.readiness = check }
}

// WithShutdownTimeout limits graceful connection draining and shutdown hooks.
func WithShutdownTimeout(timeout time.Duration) Option {
	return func(s *settings) {
		if timeout > 0 {
			s.shutdownTimeout = timeout
		}
	}
}

// withIDGenerator is intentionally private; it makes identifier generation
// deterministic in tests without adding a production customization surface.
func withIDGenerator(generate func() string) Option {
	return func(s *settings) {
		if generate != nil {
			s.idGenerator = generate
		}
	}
}

// New creates a production-ready Fiber app. A complete service can be booted
// with New, normal Fiber route registration, and Run.
func New(service string, options ...Option) *App {
	if service == "" {
		service = "unknown"
	}

	s := settings{
		logger:          NewJSONLogger(os.Stdout, slog.LevelInfo),
		shutdownTimeout: defaultShutdownTimeout,
		idGenerator:     newID,
	}
	for _, option := range options {
		if option != nil {
			option(&s)
		}
	}

	logger := s.logger.With("service", service)
	s.fiberConfig.ErrorHandler = errorHandler
	fiberApp := fiber.New(s.fiberConfig)
	app := &App{
		App:             fiberApp,
		logger:          logger,
		shutdownTimeout: s.shutdownTimeout,
		readiness:       s.readiness,
	}
	app.ready.Store(true)

	fiberApp.Use(requestContext(s.idGenerator))
	// Services are consumed directly by the local dashboard during development.
	// They do not use cookie or authorization credentials, so permissive CORS is
	// safe here and keeps the service boundary visible to the browser. Browser
	// writes say who makes them in X-Life-OS-Actor (pkg/activity).
	fiberApp.Use(cors.New(cors.Config{
		AllowOrigins: []string{"*"},
		AllowHeaders: []string{"Content-Type", RequestIDHeader, CorrelationIDHeader, "X-Life-OS-Actor"},
	}))
	fiberApp.Use(requestLogger(logger))
	fiberApp.Use(recoverPanic(logger))
	app.registerHealthRoutes()

	return app
}

// NewJSONLogger creates the default structured logger used by services.
func NewJSONLogger(writer io.Writer, level slog.Leveler) *slog.Logger {
	if writer == nil {
		writer = io.Discard
	}
	if level == nil {
		level = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: level}))
}

// Logger returns the service-scoped structured logger.
func (a *App) Logger() *slog.Logger { return a.logger }

// SetReady controls the local readiness gate. Set it false before maintenance
// or draining; a configured dependency check is evaluated only while true.
func (a *App) SetReady(ready bool) { a.ready.Store(ready) }

func (a *App) registerHealthRoutes() {
	a.Get(HealthPath, func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})
	a.Get(ReadyPath, func(c fiber.Ctx) error {
		if !a.ready.Load() {
			return NewError(fiber.StatusServiceUnavailable, "not_ready", "Service Unavailable")
		}
		if a.readiness != nil {
			if err := a.readiness(c.Context()); err != nil {
				a.logger.WarnContext(c.Context(), "readiness check failed",
					"request_id", RequestID(c),
					"correlation_id", CorrelationID(c),
					"error", err,
				)
				return WrapError(err, fiber.StatusServiceUnavailable, "not_ready", "Service Unavailable")
			}
		}
		return c.JSON(fiber.Map{"status": "ready"})
	})
}
