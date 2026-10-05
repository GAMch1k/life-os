package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestRequestContextAndStructuredLog(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	app := New("test-service",
		WithLogger(NewJSONLogger(&logs, slog.LevelDebug)),
		withIDGenerator(func() string { return "generated-request" }),
	)
	app.Get("/items/:id", func(c fiber.Ctx) error {
		if got := RequestIDFromContext(c.Context()); got != "incoming-request" {
			t.Errorf("context request id = %q", got)
		}
		if got := CorrelationIDFromContext(c.Context()); got != "incoming-correlation" {
			t.Errorf("context correlation id = %q", got)
		}
		return c.SendStatus(fiber.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/items/42", nil)
	req.Header.Set(RequestIDHeader, "incoming-request")
	req.Header.Set(CorrelationIDHeader, "incoming-correlation")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get(RequestIDHeader); got != "incoming-request" {
		t.Fatalf("response request id = %q", got)
	}
	if got := resp.Header.Get(CorrelationIDHeader); got != "incoming-correlation" {
		t.Fatalf("response correlation id = %q", got)
	}

	entry := decodeLastLog(t, logs.String())
	assertLogValue(t, entry, "msg", "http request")
	assertLogValue(t, entry, "service", "test-service")
	assertLogValue(t, entry, "request_id", "incoming-request")
	assertLogValue(t, entry, "correlation_id", "incoming-correlation")
	assertLogValue(t, entry, "method", http.MethodGet)
	assertLogValue(t, entry, "path", "/items/42")
	assertLogValue(t, entry, "status", float64(fiber.StatusNoContent))
}

func TestInvalidIncomingIDsAreNotReflected(t *testing.T) {
	t.Parallel()

	app := New("test", withIDGenerator(func() string { return "safe-id" }))
	req := httptest.NewRequest(http.MethodGet, HealthPath, nil)
	req.Header.Set(RequestIDHeader, strings.Repeat("x", 129))
	req.Header.Set(CorrelationIDHeader, "contains whitespace")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get(RequestIDHeader); got != "safe-id" {
		t.Fatalf("response request id = %q", got)
	}
	if got := resp.Header.Get(CorrelationIDHeader); got != "safe-id" {
		t.Fatalf("response correlation id = %q", got)
	}
}

func TestCommonErrorEnvelopeHidesInternalErrors(t *testing.T) {
	t.Parallel()

	app := New("test", withIDGenerator(func() string { return "request-123" }))
	app.Get("/safe", func(fiber.Ctx) error {
		return WrapError(errors.New("database password leaked"), fiber.StatusConflict, "duplicate", "Already exists")
	})
	app.Get("/unsafe", func(fiber.Ctx) error {
		return errors.New("database password leaked")
	})

	assertEnvelope(t, app, "/safe", fiber.StatusConflict, "duplicate", "Already exists", "request-123")
	assertEnvelope(t, app, "/unsafe", fiber.StatusInternalServerError, "internal_error", "Internal Server Error", "request-123")
	assertEnvelope(t, app, "/missing", fiber.StatusNotFound, "not_found", "Not Found", "request-123")
}

func TestPanicIsControlledAndLogged(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	app := New("panic-service",
		WithLogger(NewJSONLogger(&logs, slog.LevelDebug)),
		withIDGenerator(func() string { return "panic-request" }),
	)
	app.Get("/panic", func(fiber.Ctx) error { panic("boom") })

	assertEnvelope(t, app, "/panic", fiber.StatusInternalServerError, "internal_error", "Internal Server Error", "panic-request")
	if !strings.Contains(logs.String(), `"msg":"panic recovered"`) ||
		!strings.Contains(logs.String(), `"service":"panic-service"`) ||
		!strings.Contains(logs.String(), `"request_id":"panic-request"`) ||
		!strings.Contains(logs.String(), `"panic":"boom"`) {
		t.Fatalf("panic log missing required fields:\n%s", logs.String())
	}
}

func TestHealthAndReadiness(t *testing.T) {
	t.Parallel()

	readyErr := errors.New("dependency unavailable")
	checkErr := error(nil)
	app := New("test", WithReadinessCheck(func(context.Context) error { return checkErr }))

	assertStatusBody(t, app, HealthPath, fiber.StatusOK, `{"status":"ok"}`)
	assertStatusBody(t, app, ReadyPath, fiber.StatusOK, `{"status":"ready"}`)

	checkErr = readyErr
	assertEnvelopeCode(t, app, ReadyPath, fiber.StatusServiceUnavailable, "not_ready")

	checkErr = nil
	app.SetReady(false)
	assertEnvelopeCode(t, app, ReadyPath, fiber.StatusServiceUnavailable, "not_ready")
	// Liveness is independent from readiness and remains healthy while draining.
	assertStatusBody(t, app, HealthPath, fiber.StatusOK, `{"status":"ok"}`)
}

func TestWithFiberConfigKeepsNativeConfiguration(t *testing.T) {
	t.Parallel()

	app := New("test", WithFiberConfig(fiber.Config{CaseSensitive: true}))
	app.Get("/Exact", func(c fiber.Ctx) error { return c.SendString("ok") })
	assertStatusBody(t, app, "/Exact", fiber.StatusOK, "ok")
	assertEnvelopeCode(t, app, "/exact", fiber.StatusNotFound, "not_found")
}

func assertEnvelope(t *testing.T, app *App, path string, status int, code, message, requestID string) {
	t.Helper()
	resp := request(t, app, path)
	defer resp.Body.Close()
	if resp.StatusCode != status {
		t.Fatalf("%s status = %d, want %d", path, resp.StatusCode, status)
	}
	var envelope errorEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != code || envelope.Error.Message != message || envelope.RequestID != requestID {
		t.Fatalf("%s envelope = %+v", path, envelope)
	}
}

func assertEnvelopeCode(t *testing.T, app *App, path string, status int, code string) {
	t.Helper()
	resp := request(t, app, path)
	defer resp.Body.Close()
	if resp.StatusCode != status {
		t.Fatalf("%s status = %d, want %d", path, resp.StatusCode, status)
	}
	var envelope errorEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != code {
		t.Fatalf("%s code = %q, want %q", path, envelope.Error.Code, code)
	}
}

func assertStatusBody(t *testing.T, app *App, path string, status int, want string) {
	t.Helper()
	resp := request(t, app, path)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status || string(body) != want {
		t.Fatalf("%s = (%d, %q), want (%d, %q)", path, resp.StatusCode, body, status, want)
	}
}

func request(t *testing.T, app *App, path string) *http.Response {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeLastLog(t *testing.T, value string) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(value), "\n")
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil {
		t.Fatalf("decode log: %v\n%s", err, value)
	}
	return entry
}

func assertLogValue(t *testing.T, entry map[string]any, key string, want any) {
	t.Helper()
	if got := entry[key]; got != want {
		t.Errorf("log[%q] = %#v, want %#v", key, got, want)
	}
}
