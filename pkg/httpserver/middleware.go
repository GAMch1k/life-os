package httpserver

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
	"unicode"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/diagnostics"
)

const (
	RequestIDHeader     = "X-Request-ID"
	CorrelationIDHeader = "X-Correlation-ID"
)

type contextKey uint8

const (
	requestIDKey contextKey = iota
	correlationIDKey
)

func newID() string { return uuid.NewString() }

func requestContext(generate func() string) fiber.Handler {
	return func(c fiber.Ctx) error {
		requestID := safeIncomingID(c.Get(RequestIDHeader))
		if requestID == "" {
			requestID = generate()
		}
		correlationID := safeIncomingID(c.Get(CorrelationIDHeader))
		if correlationID == "" {
			correlationID = requestID
		}

		c.Locals(requestIDKey, requestID)
		c.Locals(correlationIDKey, correlationID)
		c.Set(RequestIDHeader, requestID)
		c.Set(CorrelationIDHeader, correlationID)
		ctx := context.WithValue(c.Context(), requestIDKey, requestID)
		ctx = context.WithValue(ctx, correlationIDKey, correlationID)
		c.SetContext(ctx)
		return c.Next()
	}
}

func safeIncomingID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return ""
		}
	}
	return strings.Clone(value)
}

// RequestID returns the request identifier assigned by the runtime.
func RequestID(c fiber.Ctx) string {
	value, _ := c.Locals(requestIDKey).(string)
	return value
}

// CorrelationID returns the cross-service correlation identifier.
func CorrelationID(c fiber.Ctx) string {
	value, _ := c.Locals(correlationIDKey).(string)
	return value
}

// RequestIDFromContext gets a propagated request identifier.
func RequestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey).(string)
	return value
}

// CorrelationIDFromContext gets a propagated correlation identifier.
func CorrelationIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(correlationIDKey).(string)
	return value
}

// requestLogger logs every answer with ids, the route pattern, the status,
// and the error code, never what the request carried: a path's segments
// (which may be a search term), a query string, a body, or an error's
// message, which for a client's error (4xx) often quotes what was sent. A
// server error's cause (5xx) is logged: those are the service's own failures
// (the database, the network), and handlers keep content out of them.
// Server errors are also counted as diagnostics (pkg/diagnostics).
func requestLogger(logger *slog.Logger, recorder *diagnostics.Recorder) fiber.Handler {
	return func(c fiber.Ctx) error {
		started := time.Now()
		err := c.Next()
		status := c.Response().StatusCode()
		code := ""
		if err != nil {
			status, code, _ = publicError(err)
		}
		if c.Locals(panicKey) == true {
			code = string(diagnostics.Panic)
		}
		took := time.Since(started)
		route := c.Route().Path

		attributes := []any{
			"request_id", RequestID(c),
			"correlation_id", CorrelationID(c),
			"method", c.Method(),
			"route", route,
			"status", status,
			"duration_ms", took.Milliseconds(),
		}
		if code != "" {
			attributes = append(attributes, "code", code)
		}
		if err != nil && status >= 500 {
			attributes = append(attributes, "error", err)
		}
		recorder.Answer(status, code, route, RequestID(c), took)

		level := slog.LevelInfo
		if status >= 500 {
			level = slog.LevelError
		} else if status >= 400 {
			level = slog.LevelWarn
		}
		logger.Log(c.Context(), level, "http request", attributes...)
		return err
	}
}

// panicKey marks a request whose handler panicked, for its log line and
// diagnostics.
type panicMarker struct{}

var panicKey = panicMarker{}

// panicValue is what the log says a panic was: the runtime's own errors (a
// nil pointer, an index out of range) are facts about the code; anything
// else a handler panicked with is named by its type only, since it may hold
// what the request carried.
func panicValue(recovered any) string {
	if err, ok := recovered.(runtime.Error); ok {
		return err.Error()
	}
	return fmt.Sprintf("%T", recovered)
}

func recoverPanic(logger *slog.Logger) fiber.Handler {
	return func(c fiber.Ctx) (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				c.Locals(panicKey, true)
				logger.ErrorContext(c.Context(), "panic recovered",
					"request_id", RequestID(c),
					"correlation_id", CorrelationID(c),
					"route", c.Route().Path,
					"panic", panicValue(recovered),
					"stack", string(debug.Stack()),
				)
				err = NewError(fiber.StatusInternalServerError, "internal_error", "Internal Server Error")
			}
		}()
		return c.Next()
	}
}
