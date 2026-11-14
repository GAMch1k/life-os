package httpserver

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/identity"
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

func TestNoCORS(t *testing.T) {
	app := New("test")
	app.Post("/captures", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusCreated) })

	req := httptest.NewRequest(http.MethodOptions, "/captures", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, name := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Headers", "Access-Control-Allow-Methods"} {
		if got := resp.Header.Get(name); got != "" {
			t.Fatalf("%s = %q, want none", name, got)
		}
	}
}

func identityKey(t *testing.T, kid string) (*identity.KeySet, func(map[string]any) string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]string{
		"kty": "OKP", "crv": "Ed25519", "kid": kid, "x": base64.RawURLEncoding.EncodeToString(public),
	}}})
	set, err := identity.ParseKeySet(string(jwks))
	if err != nil {
		t.Fatal(err)
	}
	encode := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	sign := func(claims map[string]any) string {
		input := encode(map[string]string{"alg": "EdDSA", "typ": "lifeos-id+jwt", "kid": kid}) + "." + encode(claims)
		return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(input)))
	}
	return set, sign
}

func TestIdentityTokenIsVerifiedWhenPresent(t *testing.T) {
	keys, sign := identityKey(t, "auth-test")
	app := New("test", WithLogger(NewJSONLogger(io.Discard, nil)), WithTrustedKeys(keys))
	app.Get("/who", func(c fiber.Ctx) error {
		p, ok := identity.FromFiber(c)
		fromContext, _ := identity.FromContext(c.Context())
		if !ok {
			return c.SendString("nobody")
		}
		if fromContext.UserID != p.UserID {
			return c.SendString("context differs")
		}
		return c.SendString(p.UserID.String() + " " + p.Via)
	})
	now := time.Now()
	valid := sign(map[string]any{
		"iss": "lifeos-auth", "aud": "lifeos-services", "sub": "00000000-0000-4000-8000-000000000001",
		"sid": "s1", "via": "session", "role": "owner", "iat": now.Unix(), "nbf": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(), "jti": "j1",
	})
	expired := sign(map[string]any{
		"iss": "lifeos-auth", "aud": "lifeos-services", "sub": "00000000-0000-4000-8000-000000000001",
		"sid": "s1", "via": "session", "role": "owner", "exp": now.Add(-time.Hour).Unix(),
	})
	call := func(path, token string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set(identity.Header, token)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if status, body := call("/who", ""); status != 200 || body != "nobody" {
		t.Fatalf("without a token: %d %s", status, body)
	}
	if status, body := call("/who", valid); status != 200 || body != "00000000-0000-4000-8000-000000000001 session" {
		t.Fatalf("with a valid token: %d %s", status, body)
	}
	for _, bad := range []string{expired, "garbage", valid + "x"} {
		status, body := call("/who", bad)
		var envelope struct {
			Error struct{ Code string } `json:"error"`
		}
		_ = json.Unmarshal([]byte(body), &envelope)
		if status != 401 || envelope.Error.Code != "unauthenticated" {
			t.Fatalf("with a bad token: %d %s", status, body)
		}
	}
	if status, _ := call(HealthPath, "garbage"); status != 200 {
		t.Fatalf("/health with a bad token: %d", status)
	}
}

func TestIdentityWithoutKeysRefusesTokens(t *testing.T) {
	_, sign := identityKey(t, "auth-test")
	t.Setenv(identity.TrustedKeysEnv, "")
	app := New("test", WithLogger(NewJSONLogger(io.Discard, nil)))
	app.Get("/who", func(c fiber.Ctx) error { return c.SendString("ok") })
	now := time.Now()
	req := httptest.NewRequest(http.MethodGet, "/who", nil)
	req.Header.Set(identity.Header, sign(map[string]any{
		"iss": "lifeos-auth", "aud": "lifeos-services", "sub": "00000000-0000-4000-8000-000000000001",
		"via": "session", "exp": now.Add(time.Minute).Unix(),
	}))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("status = %d", resp.StatusCode)
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
