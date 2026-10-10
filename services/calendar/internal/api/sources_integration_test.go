package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
)

// activityRecorder is an Activity service that keeps what it receives.
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

// After the handover (P11-08) Calendar refreshes no feed: /v1/sources serves
// a feed's owner settings (name, enabled, default reminders and priority),
// its URL, filter, preset, and refresh state are calendar-feeds', and feeds
// are added and refreshed there.
func TestSourcesServeTheOwnersSettingsOfFeeds(t *testing.T) {
	c := newIntegrationClient(t)
	registered := c.register(feedsActor, "calendar-feeds", "3c1d2e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f", map[string]any{
		"name": "ZAW", "provider": "ZAW", "readOnly": true,
		"defaultReminders": []map[string]any{{"kind": "localTime", "daysBefore": 1, "localTime": "19:00"}},
	}, http.StatusCreated)
	path := "/v1/sources/" + registered.Id.String()

	var listed generated.SourceList
	c.expect("user", http.MethodGet, "/v1/sources", nil, http.StatusOK, &listed)
	if len(listed.Items) != 1 {
		t.Fatalf("listed %d sources", len(listed.Items))
	}
	source := listed.Items[0]
	if source.Kind != generated.CalendarSourceKindExternal || source.Name != "ZAW" || source.Url != "" || source.Preset != nil || !source.Enabled ||
		len(source.Filter.Include) != 0 || source.Refresh.LastOutcome != nil || source.Refresh.NextRefreshAt != nil || source.Refresh.Refreshing ||
		source.Provider == nil || *source.Provider != "ZAW" || source.Integration == nil || *source.Integration != "calendar-feeds" ||
		source.DefaultReminders == nil || len(*source.DefaultReminders) != 1 {
		t.Fatalf("source = %+v", source)
	}

	// The owner's settings change here.
	var updated generated.CalendarSource
	c.expect("user", http.MethodPatch, path, `{"name":"Müllabfuhr","enabled":false,"defaultPriority":"high","defaultReminders":null}`, http.StatusOK, &updated)
	if updated.Name != "Müllabfuhr" || updated.Enabled || updated.DefaultPriority != generated.High || updated.DefaultReminders != nil {
		t.Fatalf("updated = %+v", updated)
	}
	// The feed's URL and filter are calendar-feeds'; feeds are added and
	// refreshed there.
	for _, body := range []string{`{"url":"https://feeds.example.test/zaw.ics"}`, `{"filter":{"include":["Papier"],"exclude":[]},"name":"x"}`} {
		c.refused("user", http.MethodPatch, path, body, http.StatusConflict, "managed_by_integration")
	}
	for _, request := range []struct{ method, target string }{{http.MethodPost, "/v1/sources"}, {http.MethodPost, path + "/refresh"}} {
		if status, raw := c.as("user", request.method, request.target, `{"name":"ZAW","url":"https://feeds.example.test/zaw.ics"}`); status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: %d %s", request.method, request.target, status, raw)
		}
	}
	for body, want := range map[string]string{
		`{"name":null}`:                              "name cannot be null",
		`{"name":"  "}`:                              "name must be",
		`{"defaultPriority":"urgent"}`:               "defaultPriority must be low, medium, or high",
		`{"defaultReminders":[{"kind":"relative"}]}`: "relative reminder",
		`{"etag":"x"}`:                               "names no source field",
	} {
		if message := c.refused("user", http.MethodPatch, path, body, http.StatusBadRequest, "bad_request"); !strings.Contains(message, want) {
			t.Errorf("PATCH %s: %q, want %q", body, message, want)
		}
	}

	// Deleting the source (as calendar-feeds does for an unsubscribed feed)
	// removes it.
	c.expect("user", http.MethodDelete, path, nil, http.StatusNoContent, nil)
	c.refused("user", http.MethodGet, path, nil, http.StatusNotFound, "not_found")
}
