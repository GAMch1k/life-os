package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
)

// fiberTestConfig leaves a database round trip more than Fiber's 1s default.
var fiberTestConfig = fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true}

func newTestApp(handler *Handler) *httpserver.App {
	app := httpserver.New("notifications", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	Register(app, handler)
	return app
}

type errorEnvelope struct {
	Error struct{ Code, Message string } `json:"error"`
}

func send(t *testing.T, app *httpserver.App, method, target, body, actor string) (int, errorEnvelope) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	if actor != "" {
		req.Header.Set(activity.ActorHeader, actor)
	}
	resp, err := app.Test(req, fiberTestConfig)
	if err != nil {
		t.Fatal(err)
	}
	var envelope errorEnvelope
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &envelope)
	return resp.StatusCode, envelope
}

func TestWritesRequireAnActor(t *testing.T) {
	app := newTestApp(NewHandler(nil, nil))
	const id = "/v1/notifications/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10"
	for _, write := range []struct{ target, body string }{
		{"/v1/notifications", `{"title":"x","importance":"low","category":"test.manual","dedupeKey":"k"}`},
		{"/v1/notifications/read-all", ``},
		{id + "/read", ``},
	} {
		for _, actor := range []string{"", "owner", "system:", "robot:x"} {
			status, envelope := send(t, app, http.MethodPost, write.target, write.body, actor)
			if status != http.StatusBadRequest || envelope.Error.Code != activity.ActorRequiredCode {
				t.Errorf("POST %s as %q = %d %+v, want 400 actor_required", write.target, actor, status, envelope)
			}
		}
	}
}

func TestInvalidRequestsFailBeforeTheDatabase(t *testing.T) {
	app := newTestApp(NewHandler(nil, nil))
	for _, request := range []struct{ method, target, body, want string }{
		{http.MethodPost, "/v1/notifications", `not json`, "JSON"},
		{http.MethodPost, "/v1/notifications", `{"title":"","importance":"low","category":"test.manual","dedupeKey":"k"}`, "title"},
		{http.MethodPost, "/v1/notifications", `{"title":"x","importance":"loud","category":"test.manual","dedupeKey":"k"}`, "importance"},
		{http.MethodPost, "/v1/notifications", `{"title":"x","importance":"low","category":"test.manual"}`, "dedupeKey"},
		{http.MethodPost, "/v1/notifications", `{"title":"x","importance":"low","category":"test.manual","dedupeKey":"k","link":"https://example.com"}`, "link"},
		{http.MethodPost, "/v1/notifications/read-all", `[1]`, "body"},
		{http.MethodGet, "/v1/notifications?limit=101", ``, "limit"},
		{http.MethodGet, "/v1/notifications?limit=0", ``, "limit"},
		{http.MethodGet, "/v1/notifications?importance=urgent", ``, "importance"},
		{http.MethodGet, "/v1/notifications?category=Calendar", ``, "category"},
		{http.MethodGet, "/v1/notifications?category=calendar.", ``, "category"},
		{http.MethodGet, "/v1/notifications?source=Calendar%20Service", ``, "source"},
		{http.MethodGet, "/v1/notifications?cursor=garbage", ``, "cursor"},
	} {
		status, envelope := send(t, app, request.method, request.target, request.body, "dev:test")
		if status != http.StatusBadRequest || envelope.Error.Code != "bad_request" || !strings.Contains(envelope.Error.Message, request.want) {
			t.Errorf("%s %s %s = %d %+v, want 400 bad_request about %q", request.method, request.target, request.body, status, envelope, request.want)
		}
	}
}

func TestMalformedNotificationIDIsABadRequest(t *testing.T) {
	app := newTestApp(NewHandler(nil, nil))
	if status, _ := send(t, app, http.MethodGet, "/v1/notifications/not-a-uuid", "", ""); status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
}
