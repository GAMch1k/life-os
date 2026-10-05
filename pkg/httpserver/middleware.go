package httpserver

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"
	"unicode"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
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

func requestLogger(logger *slog.Logger) fiber.Handler {
	return func(c fiber.Ctx) error {
		started := time.Now()
		err := c.Next()
		status := c.Response().StatusCode()
		if err != nil {
			status, _, _ = publicError(err)
		}

		attributes := []any{
			"request_id", RequestID(c),
			"correlation_id", CorrelationID(c),
			"method", c.Method(),
			"path", c.Path(),
			"status", status,
			"duration_ms", time.Since(started).Milliseconds(),
		}
		if err != nil {
			attributes = append(attributes, "error", err)
		}

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

func recoverPanic(logger *slog.Logger) fiber.Handler {
	return func(c fiber.Ctx) (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(c.Context(), "panic recovered",
					"request_id", RequestID(c),
					"correlation_id", CorrelationID(c),
					"panic", fmt.Sprint(recovered),
					"stack", string(debug.Stack()),
				)
				err = NewError(fiber.StatusInternalServerError, "internal_error", "Internal Server Error")
			}
		}()
		return c.Next()
	}
}
