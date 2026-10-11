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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
	"github.com/GAMch1k/life-os/services/food/internal/testdb"
)

var fiberTestConfig = fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true}

// fixedNow is 00:30 on 11 October 2026 in Berlin, still 10 October in UTC,
// so expiry counts show whose "today" they use.
var fixedNow = time.Date(2026, time.October, 10, 22, 30, 0, 0, time.UTC)

type apiClient struct {
	t        *testing.T
	app      *httpserver.App
	handler  *Handler
	pool     *pgxpool.Pool
	recorder *activityRecorder
}

// newAPIClient serves the Food API on a fresh migrated schema, with the owner
// in Berlin and every Activity event kept by an in-process stand-in.
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
	handler := NewHandler(pool, Options{Location: berlin,
		Activity: activity.NewEmitter(server.URL, "food", slog.New(slog.NewTextHandler(io.Discard, nil)))})
	handler.now = func() time.Time { return fixedNow }
	app := httpserver.New("food", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	Register(app, handler)
	return apiClient{t: t, app: app, handler: handler, pool: pool, recorder: recorder}
}

// do sends a request (writes as dev:test unless headers name another actor)
// and returns status and body.
func (c apiClient) do(method, target string, body any, headers ...string) (int, []byte) {
	c.t.Helper()
	var reader io.Reader
	switch value := body.(type) {
	case nil:
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
	req.Header.Set("content-type", "application/json")
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
func (c apiClient) expectError(method, target string, body any, wantStatus int, wantCode string, headers ...string) {
	c.t.Helper()
	status, raw := c.do(method, target, body, headers...)
	var envelope generated.ErrorResponse
	_ = json.Unmarshal(raw, &envelope)
	if status != wantStatus || envelope.Error.Code != wantCode {
		c.t.Fatalf("%s %s: %d %s, want %d %s", method, target, status, raw, wantStatus, wantCode)
	}
}

// add adds shopping items and returns their results.
func (c apiClient) add(items ...map[string]any) []generated.ShoppingItemResult {
	c.t.Helper()
	return expect[generated.AddShoppingItemsResponse](c, http.MethodPost, "/v1/shopping-items", map[string]any{"items": items}, http.StatusOK).Items
}

func (c apiClient) list(query string) generated.ShoppingItemList {
	c.t.Helper()
	return expect[generated.ShoppingItemList](c, http.MethodGet, "/v1/shopping-items?"+query, nil, http.StatusOK)
}

func (c apiClient) pantry(query string) generated.PantryItemList {
	c.t.Helper()
	return expect[generated.PantryItemList](c, http.MethodGet, "/v1/pantry-items?"+query, nil, http.StatusOK)
}

func names(items []generated.ShoppingItem) string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Name)
	}
	return strings.Join(out, ", ")
}

func pantryNames(items []generated.PantryItem) string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Name)
	}
	return strings.Join(out, ", ")
}

// count is the number of rows in a table of the test schema.
func (c apiClient) count(table, where string) int {
	c.t.Helper()
	var n int
	if err := c.pool.QueryRow(c.t.Context(), "SELECT count(*) FROM "+table+" WHERE "+where).Scan(&n); err != nil {
		c.t.Fatal(err)
	}
	return n
}

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
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return strings.Join(types, " ")
}
