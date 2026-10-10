package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/testdb"
)

var fiberTestConfig = fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true}

// fixedNow is 00:30 on 11 October 2026 in Berlin, still 10 October in UTC,
// so a default booking date shows whose "today" it is.
var fixedNow = time.Date(2026, time.October, 10, 22, 30, 0, 0, time.UTC)

type apiClient struct {
	t        *testing.T
	app      *httpserver.App
	handler  *Handler
	queries  *store.Queries
	recorder *activityRecorder
}

// newAPIClient serves the Finance API on a fresh migrated schema, with the
// owner in Berlin, the Cash account ensured as at startup, and every
// Activity event kept by an in-process stand-in.
func newAPIClient(t *testing.T) apiClient {
	t.Helper()
	pool := testdb.Open(t, "")
	recorder := &activityRecorder{}
	server := httptest.NewServer(recorder)
	t.Cleanup(server.Close)
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(pool, Options{Location: berlin, DefaultCurrency: "EUR",
		Activity: activity.NewEmitter(server.URL, "finance", slog.New(slog.NewTextHandler(io.Discard, nil)))})
	handler.now = func() time.Time { return fixedNow }
	app := httpserver.New("finance", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	Register(app, handler)
	c := apiClient{t: t, app: app, handler: handler, queries: store.New(pool), recorder: recorder}
	if _, err := handler.EnsureCashAccount(t.Context()); err != nil {
		t.Fatal(err)
	}
	return c
}

// do sends a request (writes as dev:test unless headers name another actor)
// and returns status and body.
func (c apiClient) do(method, target string, body any, headers ...string) (int, []byte) {
	c.t.Helper()
	var reader io.Reader
	contentType := "application/json"
	switch value := body.(type) {
	case nil:
	case csvBody:
		reader, contentType = strings.NewReader(string(value)), "text/csv"
	case string:
		reader = strings.NewReader(value)
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("content-type", contentType)
	req.Header.Set(activity.ActorHeader, "dev:test")
	for index := 0; index+1 < len(headers); index += 2 {
		req.Header.Set(headers[index], headers[index+1])
	}
	resp, err := c.app.Test(req, fiberTestConfig)
	if err != nil {
		c.t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp.StatusCode, raw
}

type csvBody string

// expect sends a request, checks the status, and decodes the body into out.
func expect[T any](c apiClient, method, target string, body any, wantStatus int, headers ...string) T {
	c.t.Helper()
	status, raw := c.do(method, target, body, headers...)
	if status != wantStatus {
		c.t.Fatalf("%s %s: %d %s, want %d", method, target, status, raw, wantStatus)
	}
	var out T
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			c.t.Fatalf("%s %s: %v in %s", method, target, err, raw)
		}
	}
	return out
}

// expectError checks a refusal's status and error code.
func (c apiClient) expectError(method, target string, body any, wantStatus int, wantCode string) {
	c.t.Helper()
	status, raw := c.do(method, target, body)
	var envelope generated.ErrorResponse
	_ = json.Unmarshal(raw, &envelope)
	if status != wantStatus || envelope.Error.Code != wantCode {
		c.t.Fatalf("%s %s: %d %s, want %d %s", method, target, status, raw, wantStatus, wantCode)
	}
}

func (c apiClient) cash() generated.Account {
	c.t.Helper()
	for _, account := range expect[generated.AccountList](c, http.MethodGet, "/v1/accounts", nil, http.StatusOK).Items {
		if account.BuiltIn {
			return account
		}
	}
	c.t.Fatal("no built-in Cash account")
	return generated.Account{}
}

func (c apiClient) account(name, kind, currency string, opening int64) generated.Account {
	c.t.Helper()
	return expect[generated.Account](c, http.MethodPost, "/v1/accounts", map[string]any{"name": name, "kind": kind, "currency": currency, "openingBalanceMinor": opening}, http.StatusCreated)
}

func (c apiClient) category(name, kind string) generated.Category {
	c.t.Helper()
	return expect[generated.Category](c, http.MethodPost, "/v1/categories", map[string]any{"name": name, "kind": kind}, http.StatusCreated)
}

func (c apiClient) rule(categoryID, field, operator, text string) generated.CategoryRule {
	c.t.Helper()
	return expect[generated.CategoryRule](c, http.MethodPost, "/v1/categories/rules", map[string]any{"categoryId": categoryID, "match": map[string]any{"field": field, "operator": operator, "text": text}}, http.StatusCreated)
}

func (c apiClient) spend(body map[string]any) generated.Transaction {
	c.t.Helper()
	return expect[generated.Transaction](c, http.MethodPost, "/v1/transactions", body, http.StatusCreated)
}

func (c apiClient) list(query string) generated.TransactionList {
	c.t.Helper()
	return expect[generated.TransactionList](c, http.MethodGet, "/v1/transactions?"+query, nil, http.StatusOK)
}

func eur(amount int64) map[string]any { return map[string]any{"amount": amount, "currency": "EUR"} }

// activityRecorder is an in-process Activity service.
type activityRecorder struct {
	mu     sync.Mutex
	events []activity.Event
}

func (a *activityRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var event activity.Event
	_ = json.NewDecoder(r.Body).Decode(&event)
	a.mu.Lock()
	a.events = append(a.events, event)
	a.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

func (a *activityRecorder) all() []activity.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]activity.Event(nil), a.events...)
}

// since returns the events recorded after the first seen ones and moves
// seen past them.
func (a *activityRecorder) since(seen *int) []activity.Event {
	all := a.all()
	fresh := all[*seen:]
	*seen = len(all)
	return fresh
}

func eventTypes(events []activity.Event) string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Type)
	}
	return strings.Join(names, " ")
}
