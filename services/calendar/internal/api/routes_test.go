package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
)

// send makes one request to app and returns the status and error envelope.
func send(t *testing.T, app *httpserver.App, method, target, body, actor string) (int, string, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("content-type", "application/json")
	if actor != "" {
		req.Header.Set(activity.ActorHeader, actor)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&envelope)
	return resp.StatusCode, envelope.Error.Code, envelope.Error.Message
}

func newTestApp(handler *Handler) *httpserver.App {
	app := httpserver.New("calendar", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	Register(app, handler)
	return app
}

func TestWritesRequireAnActor(t *testing.T) {
	app := newTestApp(NewHandler(nil, ""))
	const event = "/v1/events/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10"
	for _, write := range []struct{ method, target, body string }{
		{http.MethodPost, "/v1/events", `{`},
		{http.MethodPatch, event, `{`},
		{http.MethodDelete, event, ""},
	} {
		for _, actor := range []string{"", "agent:", "owner"} {
			if status, code, _ := send(t, app, write.method, write.target, write.body, actor); status != http.StatusBadRequest || code != activity.ActorRequiredCode {
				t.Errorf("%s %s with actor %q: %d %q, want 400 %s", write.method, write.target, actor, status, code, activity.ActorRequiredCode)
			}
		}
	}
	// With an actor the request reaches the handler, which rejects the
	// incomplete body on its own terms.
	for _, method := range []string{http.MethodPost, http.MethodPatch} {
		target := "/v1/events"
		if method == http.MethodPatch {
			target = event
		}
		if status, code, _ := send(t, app, method, target, `{`, "dev:test"); status != http.StatusBadRequest || code == activity.ActorRequiredCode {
			t.Errorf("%s with a declared actor: %d %q, want the handler's own 400", method, status, code)
		}
	}
}

// Requests that are invalid on their face are refused before any query runs
// (the handler here has no database), with a message saying what to fix.
func TestInvalidRequestsAreRefusedWithAReason(t *testing.T) {
	app := newTestApp(NewHandler(nil, "Europe/Berlin"))
	for _, tc := range []struct{ method, target, body, want string }{
		{http.MethodGet, "/v1/events?from=2026-10-10T00:00:00Z&to=2026-10-10T00:00:00Z", "", "to must be after from"},
		{http.MethodGet, "/v1/events?from=2026-10-10T00:00:00Z&to=2026-10-11T00:00:00Z&timeZone=Mars/Olympus", "", "timeZone"},
		{http.MethodGet, "/v1/events?from=2026-10-10T00:00:00Z&to=2026-10-11T00:00:00Z&limit=101", "", "limit"},
		{http.MethodGet, "/v1/events?from=2026-10-10T00:00:00Z&to=2026-10-11T00:00:00Z&limit=0", "", "limit"},
		{http.MethodGet, "/v1/events?from=2026-10-10T00:00:00Z&to=2026-10-11T00:00:00Z&sourceKind=outlook", "", "sourceKind"},
		{http.MethodGet, "/v1/events?from=2026-10-10T00:00:00Z&to=2026-10-11T00:00:00Z&cursor=nonsense", "", "invalid cursor"},
		{http.MethodGet, "/v1/events/search?q=%20%20", "", "q must contain a word"},
		{http.MethodGet, "/v1/events/search?q=papier&from=2026-10-11T00:00:00Z&to=2026-10-10T00:00:00Z", "", "to must be after from"},
		{http.MethodGet, "/v1/events/search?q=papier&timeZone=Local", "", "timeZone"},
		{http.MethodGet, "/v1/events/search?q=papier&cursor=nonsense", "", "invalid cursor"},
		{http.MethodPost, "/v1/events", `{"title":" ","allDay":true,"startDate":"2026-12-24","endDate":"2026-12-24"}`, "title must not be empty"},
		{http.MethodPost, "/v1/events", `{"title":"Holiday","allDay":true,"startDate":"2026-12-24","endDate":"2026-12-24","timeZone":"Europe/Berlin"}`, "all-day events take no timeZone"},
		{http.MethodPost, "/v1/events", `{"title":"Pickup","allDay":false,"startAt":"2026-11-17T06:00:00+01:00","endAt":"2026-11-17T06:30:00+01:00","reminders":[{"kind":"localTime","daysBefore":1}]}`, "localTime reminder"},
		{http.MethodPatch, "/v1/events/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10", `{"title":null}`, "title cannot be null"},
		{http.MethodPatch, "/v1/events/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10", `{"id":"x"}`, "names no event field"},
	} {
		status, code, message := send(t, app, tc.method, tc.target, tc.body, "dev:test")
		if status != http.StatusBadRequest || code != "bad_request" || !strings.Contains(message, tc.want) {
			t.Errorf("%s %s %s: %d %q %q, want 400 bad_request mentioning %q", tc.method, tc.target, tc.body, status, code, message, tc.want)
		}
	}
}
