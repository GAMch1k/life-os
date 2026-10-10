package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
)

const googleCalendarPath = "/v1/google/calendars/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10"

// Every Google write says who makes it.
func TestGoogleWritesRequireAnActor(t *testing.T) {
	app := newTestApp(NewHandler(nil, ""))
	for _, write := range []struct{ method, target, body string }{
		{http.MethodPost, "/v1/google/connection/authorize", ""},
		{http.MethodPost, "/v1/google/connection", `{"code":"c","state":"s"}`},
		{http.MethodDelete, "/v1/google/connection", ""},
		{http.MethodPost, "/v1/google/calendars/refresh", ""},
		{http.MethodPatch, googleCalendarPath, `{"syncEnabled":true}`},
	} {
		for _, actor := range []string{"", "agent:", "owner"} {
			if status, code, _ := send(t, app, write.method, write.target, write.body, actor); status != http.StatusBadRequest || code != activity.ActorRequiredCode {
				t.Errorf("%s %s with actor %q: %d %q, want 400 %s", write.method, write.target, actor, status, code, activity.ActorRequiredCode)
			}
		}
	}
}

// Without the Google service (no database here), Google reads as not
// configured and nothing crashes.
func TestGoogleWithoutServiceIsNotConfigured(t *testing.T) {
	app := newTestApp(NewHandler(nil, ""))
	req := httptest.NewRequest(http.MethodGet, "/v1/google/connection", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	var connection generated.GoogleConnection
	if err := json.Unmarshal(raw, &connection); err != nil || resp.StatusCode != http.StatusOK || connection.Configured ||
		connection.Status != "not_connected" || connection.Scopes == nil || connection.MissingScopes == nil {
		t.Fatalf("GET connection: %d %s", resp.StatusCode, raw)
	}
	for _, tc := range []struct {
		method, target, body string
		status               int
		code                 string
	}{
		{http.MethodPost, "/v1/google/connection/authorize", "", http.StatusServiceUnavailable, "google_not_configured"},
		{http.MethodPost, "/v1/google/connection", `{"code":"c","state":"s"}`, http.StatusServiceUnavailable, "google_not_configured"},
		{http.MethodPost, "/v1/google/connection", `{"code":"c"}`, http.StatusBadRequest, "bad_request"},
		{http.MethodPost, "/v1/google/connection", `{`, http.StatusBadRequest, "bad_request"},
		{http.MethodPost, "/v1/google/calendars/refresh", "", http.StatusServiceUnavailable, "google_not_configured"},
		{http.MethodDelete, "/v1/google/connection", "", http.StatusNoContent, ""},
		{http.MethodPatch, googleCalendarPath, `{"syncEnabled":true}`, http.StatusNotFound, "not_found"},
		{http.MethodPatch, googleCalendarPath, `{"syncEnabled":"yes"}`, http.StatusBadRequest, "bad_request"},
		{http.MethodPatch, googleCalendarPath, `{}`, http.StatusBadRequest, "bad_request"},
		{http.MethodGet, "/v1/google/calendars", "", http.StatusOK, ""},
	} {
		if status, code, _ := send(t, app, tc.method, tc.target, tc.body, "dev:test"); status != tc.status || code != tc.code {
			t.Errorf("%s %s %s: %d %q, want %d %q", tc.method, tc.target, tc.body, status, code, tc.status, tc.code)
		}
	}
}
