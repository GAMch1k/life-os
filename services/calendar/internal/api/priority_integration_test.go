package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
)

// storedPriority is the event's own priority as stored ("" for none).
func (c apiClient) storedPriority(id types.UUID) string {
	c.t.Helper()
	row, err := c.queries.GetEvent(context.Background(), pgUUID(id))
	if err != nil {
		c.t.Fatal(err)
	}
	if row.Priority == nil {
		return ""
	}
	return *row.Priority
}

// A Life OS event has medium priority unless it gets another; medium is no
// priority of its own.
func TestEventPriority(t *testing.T) {
	c := newAPIClient(t)
	plain := c.event(http.MethodPost, "/v1/events", map[string]any{
		"title": "Dentist", "allDay": false, "startAt": "2026-11-17T09:30:00+01:00", "endAt": "2026-11-17T10:15:00+01:00",
	}, http.StatusCreated)
	if plain.Priority != generated.Medium || plain.DefaultPriority != generated.Medium || c.storedPriority(plain.Id) != "" {
		t.Fatalf("created without a priority = %s/%s, stored %q", plain.Priority, plain.DefaultPriority, c.storedPriority(plain.Id))
	}
	urgent := c.event(http.MethodPost, "/v1/events", map[string]any{
		"title": "Visa appointment", "allDay": false, "startAt": "2026-11-18T09:00:00+01:00", "endAt": "2026-11-18T10:00:00+01:00", "priority": "high",
	}, http.StatusCreated)
	if urgent.Priority != generated.High || urgent.DefaultPriority != generated.Medium || c.storedPriority(urgent.Id) != "high" {
		t.Fatalf("created high = %s/%s", urgent.Priority, urgent.DefaultPriority)
	}
	if medium := c.event(http.MethodPost, "/v1/events", `{"title":"Call","allDay":true,"startDate":"2026-11-19","endDate":"2026-11-19","priority":"medium"}`, http.StatusCreated); medium.Priority != generated.Medium || c.storedPriority(medium.Id) != "" {
		t.Fatalf("created medium = %s, stored %q", medium.Priority, c.storedPriority(medium.Id))
	}

	target := "/v1/events/" + plain.Id.String()
	if low := c.event(http.MethodPatch, target, `{"priority":"low"}`, http.StatusOK); low.Priority != generated.Low || c.storedPriority(plain.Id) != "low" {
		t.Fatalf("patched low = %s", low.Priority)
	}
	// Other changes keep it.
	if renamed := c.event(http.MethodPatch, target, `{"title":"Dentist (Dr. Weber)"}`, http.StatusOK); renamed.Priority != generated.Low {
		t.Fatalf("renamed = %s", renamed.Priority)
	}
	// Listing and search carry it.
	listed := c.list("/v1/events?" + q("from", "2026-11-17T00:00:00Z", "to", "2026-11-18T22:00:00Z"))
	if len(listed.Items) != 2 || listed.Items[0].Priority != generated.Low || listed.Items[1].Priority != generated.High {
		t.Fatalf("listed = %+v", listed.Items)
	}
	if found := c.list("/v1/events/search?" + q("q", "visa")); len(found.Items) != 1 || found.Items[0].Priority != generated.High {
		t.Fatalf("search = %+v", found.Items)
	}
	// Back to the default: no priority of its own.
	if back := c.event(http.MethodPatch, target, `{"priority":"medium"}`, http.StatusOK); back.Priority != generated.Medium || c.storedPriority(plain.Id) != "" {
		t.Fatalf("back to medium = %s, stored %q", back.Priority, c.storedPriority(plain.Id))
	}

	for body, want := range map[string]string{
		`{"priority":"urgent"}`: "priority must be low, medium, or high",
		`{"priority":null}`:     "priority cannot be null",
	} {
		if _, message := c.failure(http.MethodPatch, target, body, http.StatusBadRequest); message != want {
			t.Errorf("PATCH %s: %q, want %q", body, message, want)
		}
	}
	if _, message := c.failure(http.MethodPost, "/v1/events", `{"title":"X","allDay":true,"startDate":"2026-11-19","endDate":"2026-11-19","priority":"urgent"}`, http.StatusBadRequest); message != "priority must be low, medium, or high" {
		t.Errorf("create urgent: %q", message)
	}
}

// A series has a priority; This event gives one occurrence its own, All
// events changes the others, and This and following gives the new series
// the body's.
func TestRecurringEventPriority(t *testing.T) {
	c := newAPIClient(t)
	gym := gymSeries(c, map[string]any{"priority": "high"})
	if gym.Priority != generated.High {
		t.Fatalf("series = %s", gym.Priority)
	}
	window := "/v1/events?" + q("from", "2026-10-04T22:00:00Z", "to", "2026-10-25T22:00:00Z")
	priorities := func() []string {
		var out []string
		for _, item := range c.list(window).Items {
			label := string(item.Priority)
			if item.Occurrence.Modified {
				label += " modified"
			}
			out = append(out, label)
		}
		return out
	}
	equal(t, "series", priorities(), []string{"high", "high", "high"})

	// This event: the 17 October occurrence is low.
	occurrence := occurrencePath(gym.Id, "2026-10-17T05:00:00Z", "")
	low := c.event(http.MethodPatch, occurrence, `{"priority":"low"}`, http.StatusOK)
	if low.Priority != generated.Low || !low.Occurrence.Modified || low.DefaultPriority != generated.Medium {
		t.Fatalf("occurrence low = %+v", low)
	}
	equal(t, "one low", priorities(), []string{"high", "low modified", "high"})
	if _, message := c.failure(http.MethodPatch, occurrence, `{"priority":"urgent"}`, http.StatusBadRequest); message != "priority must be low, medium, or high" {
		t.Errorf("occurrence urgent: %q", message)
	}

	// All events: medium (the default); the low occurrence keeps its own.
	series := c.event(http.MethodPatch, "/v1/events/"+gym.Id.String(), `{"priority":"medium"}`, http.StatusOK)
	if series.Priority != generated.Medium || c.storedPriority(gym.Id) != "" {
		t.Fatalf("all events medium = %s, stored %q", series.Priority, c.storedPriority(gym.Id))
	}
	equal(t, "all medium", priorities(), []string{"medium", "low modified", "medium"})

	// Set back to the series' priority, the occurrence follows it again.
	back := c.event(http.MethodPatch, occurrence, `{"priority":"medium"}`, http.StatusOK)
	if back.Priority != generated.Medium || back.Occurrence.Modified {
		t.Fatalf("occurrence back = %+v", back)
	}
	equal(t, "no exceptions", c.exceptionKeys(gym.Id), []string{})

	// This and following: the new series has the body's priority, and the
	// split occurrence loses its own.
	c.event(http.MethodPatch, occurrence, `{"priority":"low","title":"Gym with Anna"}`, http.StatusOK)
	split := c.event(http.MethodPost, occurrencePath(gym.Id, "2026-10-17T05:00:00Z", "/split"), `{"priority":"high"}`, http.StatusCreated)
	if split.Priority != generated.High || c.storedPriority(split.Id) != "high" {
		t.Fatalf("split = %s", split.Priority)
	}
	first := c.event(http.MethodGet, occurrencePath(split.Id, "2026-10-17T05:00:00Z", ""), nil, http.StatusOK)
	if first.Priority != generated.High || first.Title != "Gym with Anna" || !first.Occurrence.Modified {
		t.Fatalf("split occurrence = %+v", first)
	}
}

// A source's default priority applies to its events without one of their
// own; the owner's priority on an imported event survives syncs and later
// changes of the default. The source is a feed of calendar-feeds, synced as
// it does (snapshots with window upcomingOnly, no priorities of its own).
func TestSourcePriority(t *testing.T) {
	c := newIntegrationClient(t)
	const feed = "6a7b8c9d-0e1f-4a2b-8c3d-4e5f6a7b8c9d"
	plain := c.register(feedsActor, "calendar-feeds", "other-feed", map[string]any{"name": "Other", "provider": "ICS", "readOnly": true}, http.StatusCreated)
	if plain.DefaultPriority != generated.Medium {
		t.Fatalf("source without a default priority = %s", plain.DefaultPriority)
	}
	created := c.register(feedsActor, "calendar-feeds", feed, map[string]any{"name": "ZAW", "provider": "ZAW", "readOnly": true, "defaultPriority": "high"}, http.StatusCreated)
	if created.DefaultPriority != generated.High {
		t.Fatalf("created = %s", created.DefaultPriority)
	}
	pickup := func(uid, day string) map[string]any {
		return map[string]any{"externalUid": uid, "title": "Bioabfall", "allDay": false, "startAt": day + "T04:00:00Z", "endAt": day + "T05:00:00Z", "timeZone": "Europe/Berlin"}
	}
	snapshot := func(items ...map[string]any) generated.SyncIntegrationSourceResult {
		return c.sync(feedsActor, "calendar-feeds", feed, map[string]any{"mode": "snapshot", "items": items, "window": map[string]any{"upcomingOnly": true}})
	}
	snapshot(pickup("bio-1@mymuell", "2026-10-15"), pickup("bio-2@mymuell", "2026-10-29"))
	id := created.Id.String()
	events := c.events("sourceId", id)
	for _, item := range events {
		if item.Priority != generated.High || item.DefaultPriority != generated.High || item.Source.Kind != generated.EventSourceKindExternal {
			t.Fatalf("imported %s = %s/%s", item.Title, item.Priority, item.DefaultPriority)
		}
	}

	// The owner makes one pickup low; it stays low through a sync that
	// moves it.
	paper := events[0]
	paperURL := "/v1/events/" + paper.Id.String()
	if low := c.event(http.MethodPatch, paperURL, `{"priority":"low"}`, http.StatusOK); low.Priority != generated.Low || low.DefaultPriority != generated.High {
		t.Fatalf("own priority = %s/%s", low.Priority, low.DefaultPriority)
	}
	if got := snapshot(pickup("bio-1@mymuell", "2026-10-16"), pickup("bio-2@mymuell", "2026-10-29")); got.Updated != 1 {
		t.Fatalf("sync counts = %+v", got)
	}
	moved := c.event(http.MethodGet, paperURL, nil, http.StatusOK)
	if moved.StartAt.Format("2006-01-02") != "2026-10-16" || moved.Priority != generated.Low {
		t.Fatalf("after the sync = %s %s", moved.StartAt, moved.Priority)
	}

	// The source's default becomes medium: the other pickup follows it, the
	// low one keeps its own.
	var updated generated.CalendarSource
	c.expect("user", http.MethodPatch, "/v1/sources/"+id, `{"defaultPriority":"medium"}`, http.StatusOK, &updated)
	if updated.DefaultPriority != generated.Medium {
		t.Fatalf("updated = %s", updated.DefaultPriority)
	}
	for _, item := range c.events("sourceId", id) {
		want := generated.Medium
		if item.Id == paper.Id {
			want = generated.Low
		}
		if item.Priority != want || item.DefaultPriority != generated.Medium {
			t.Fatalf("%s %s = %s/%s, want %s", item.Title, item.StartAt, item.Priority, item.DefaultPriority, want)
		}
	}
	// Set to the source's default, the pickup follows the default again.
	c.event(http.MethodPatch, paperURL, `{"priority":"medium"}`, http.StatusOK)
	c.expect("user", http.MethodPatch, "/v1/sources/"+id, `{"defaultPriority":"high"}`, http.StatusOK, nil)
	if again := c.event(http.MethodGet, paperURL, nil, http.StatusOK); again.Priority != generated.High {
		t.Fatalf("following the default again = %s", again.Priority)
	}

	// Imported events stay read-only beyond reminders and priority.
	if status, raw := c.do(http.MethodPatch, paperURL, `{"priority":"low","title":"Mine"}`); status != http.StatusConflict {
		t.Errorf("priority with a title: %d %s", status, raw)
	}
	for body, want := range map[string]string{
		`{"defaultPriority":"urgent"}`: "defaultPriority must be low, medium, or high",
		`{"defaultPriority":null}`:     "defaultPriority cannot be null",
	} {
		if message := c.refused("user", http.MethodPatch, "/v1/sources/"+id, body, http.StatusBadRequest, "bad_request"); message != want {
			t.Errorf("PATCH %s: %q, want %q", body, message, want)
		}
	}
}

// A priority change is an ordinary update in Activity.
func TestPriorityChangesAreRecordedInActivity(t *testing.T) {
	c, recorder := newActivityClient(t)
	seen := 0
	created := c.event(http.MethodPost, "/v1/events", map[string]any{
		"title": "Dentist", "allDay": false, "startAt": "2026-11-17T09:30:00+01:00", "endAt": "2026-11-17T10:15:00+01:00",
	}, http.StatusCreated)
	expectRecorded(t, recorder, &seen, eventCreated)
	target := "/v1/events/" + created.Id.String()
	if status, raw := c.do(http.MethodPatch, target, `{"priority":"high"}`, activity.ActorHeader, "user"); status != http.StatusOK {
		t.Fatalf("priority: %d %s", status, raw)
	}
	updated := expectRecorded(t, recorder, &seen, eventUpdated)[0]
	changes, _ := updated.Data["changes"].([]any)
	if len(changes) != 1 || changes[0] != "priority" || updated.Summary != "Event “Dentist” updated: priority" {
		t.Fatalf("updated = %+v", updated)
	}
	// Setting the priority it has records nothing.
	c.event(http.MethodPatch, target, `{"priority":"high"}`, http.StatusOK)
	expectRecorded(t, recorder, &seen)

	// So is an occurrence's own priority, and setting medium on a series
	// without one of its own changes nothing.
	gym := gymSeries(c, nil)
	expectRecorded(t, recorder, &seen, eventCreated)
	c.event(http.MethodPatch, occurrencePath(gym.Id, "2026-10-17T05:00:00Z", ""), `{"priority":"medium"}`, http.StatusOK)
	expectRecorded(t, recorder, &seen)
	c.event(http.MethodPatch, occurrencePath(gym.Id, "2026-10-17T05:00:00Z", ""), `{"priority":"low"}`, http.StatusOK)
	occurrence := expectRecorded(t, recorder, &seen, eventUpdated)[0]
	if changes, _ := occurrence.Data["changes"].([]any); len(changes) != 1 || changes[0] != "priority" || occurrence.Data["recurrenceId"] != "2026-10-17T05:00:00Z" {
		t.Fatalf("occurrence = %+v", occurrence)
	}
}
