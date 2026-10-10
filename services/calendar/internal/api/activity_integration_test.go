package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// newActivityClient is an API client whose event changes are recorded by an
// in-process Activity stand-in.
func newActivityClient(t *testing.T) (apiClient, *activityRecorder) {
	t.Helper()
	c := newAPIClient(t)
	recorder := &activityRecorder{}
	server := httptest.NewServer(recorder)
	t.Cleanup(server.Close)
	c.handler.WithActivity(activity.NewEmitter(server.URL, "calendar", slog.New(slog.NewTextHandler(io.Discard, nil))))
	return c, recorder
}

// expectRecorded checks that exactly the given event types were recorded
// since the last check, and returns them.
func expectRecorded(t *testing.T, recorder *activityRecorder, seen *int, types ...string) []activity.Event {
	t.Helper()
	all := recorder.all()
	fresh := all[*seen:]
	*seen = len(all)
	got := make([]string, 0, len(fresh))
	for _, event := range fresh {
		got = append(got, event.Type)
	}
	if strings.Join(got, " ") != strings.Join(types, " ") {
		t.Fatalf("recorded %q, want %q", got, types)
	}
	return fresh
}

func TestEventChangesAreRecordedInActivity(t *testing.T) {
	c, recorder := newActivityClient(t)
	seen := 0
	cause := googleuuid.New()

	// The owner creates an event in the Dashboard.
	status, raw := c.do(http.MethodPost, "/v1/events", map[string]any{
		"title": "Dentist", "description": "Bring card", "allDay": false,
		"startAt": "2026-11-17T09:30:00+01:00", "endAt": "2026-11-17T10:15:00+01:00",
	}, activity.ActorHeader, "user", activity.CorrelationIDHeader, "corr-dashboard", activity.CausationIDHeader, cause.String())
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, raw)
	}
	created := expectRecorded(t, recorder, &seen, eventCreated)[0]
	id := created.Subject.ID
	if created.Actor != activity.User() || created.CorrelationID != "corr-dashboard" || created.CausationID == nil || *created.CausationID != cause ||
		created.Subject.Type != "calendar_event" || created.Source != "calendar" || created.Outcome != activity.Succeeded {
		t.Fatalf("created = %+v", created)
	}
	if created.Summary != "Event “Dentist” created for Tue 17 Nov 2026, 09:30 CET" || created.Data["startAt"] != "2026-11-17T08:30:00Z" ||
		created.Data["title"] != "Dentist" || created.Data["sourceKind"] != "manual" || created.Data["description"] != nil {
		t.Fatalf("created summary/data = %q %v", created.Summary, created.Data)
	}
	target := "/v1/events/" + id

	// The agent renames it, within its capture's workflow.
	status, raw = c.do(http.MethodPatch, target, map[string]any{"title": "Dentist (Dr. Weber)"},
		activity.ActorHeader, "agent:codex-inbox-processor", activity.CorrelationIDHeader, "corr-capture")
	if status != http.StatusOK {
		t.Fatalf("rename: %d %s", status, raw)
	}
	renamed := expectRecorded(t, recorder, &seen, eventUpdated)[0]
	if renamed.Actor != activity.Agent("codex-inbox-processor") || renamed.CorrelationID != "corr-capture" || renamed.Subject.ID != id ||
		renamed.Data["previousTitle"] != "Dentist" || renamed.Summary != "Event “Dentist (Dr. Weber)” updated: title" {
		t.Fatalf("renamed = %+v", renamed)
	}

	// The same values again change nothing and record nothing; a rejected
	// change records nothing either.
	c.event(http.MethodPatch, target, map[string]any{"title": "Dentist (Dr. Weber)"}, http.StatusOK)
	if status, raw := c.do(http.MethodPatch, target, map[string]any{"endAt": "2026-11-17T08:00:00Z"}); status != http.StatusBadRequest {
		t.Fatalf("invalid move: %d %s", status, raw)
	}
	expectRecorded(t, recorder, &seen)

	// A development tool moves it: one rescheduled event with the old time.
	c.event(http.MethodPatch, target, map[string]any{"startAt": "2026-11-19T08:00:00Z", "endAt": "2026-11-19T08:45:00Z"}, http.StatusOK)
	moved := expectRecorded(t, recorder, &seen, eventRescheduled)[0]
	previous, _ := moved.Data["previous"].(map[string]any)
	if moved.Actor != activity.Dev("test") || previous["startAt"] != "2026-11-17T08:30:00Z" || moved.Data["startAt"] != "2026-11-19T08:00:00Z" ||
		moved.Summary != "Event “Dentist (Dr. Weber)” moved to Thu 19 Nov 2026, 09:00 CET" {
		t.Fatalf("moved = %+v", moved)
	}

	// Cancel, restore, delete: one event each, with distinct ids.
	c.event(http.MethodPatch, target, map[string]any{"status": "cancelled"}, http.StatusOK)
	c.event(http.MethodPatch, target, map[string]any{"status": "cancelled"}, http.StatusOK)
	c.event(http.MethodPatch, target, map[string]any{"status": "active"}, http.StatusOK)
	if status, raw := c.do(http.MethodDelete, target, nil, activity.ActorHeader, "user"); status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", status, raw)
	}
	lifecycle := expectRecorded(t, recorder, &seen, eventCancelled, eventRestored, eventDeleted)
	if lifecycle[0].Data["status"] != "cancelled" || lifecycle[1].Data["status"] != "active" || lifecycle[2].Actor != activity.User() ||
		lifecycle[2].Summary != "Event “Dentist (Dr. Weber)” on Thu 19 Nov 2026, 09:00 CET deleted" {
		t.Fatalf("lifecycle = %+v", lifecycle)
	}
	ids := map[googleuuid.UUID]bool{}
	for _, event := range recorder.all() {
		ids[event.ID] = true
	}
	if len(ids) != len(recorder.all()) {
		t.Fatalf("event ids repeat: %+v", recorder.all())
	}
	if status, _ := c.do(http.MethodDelete, target, nil); status != http.StatusNotFound {
		t.Fatalf("second delete: %d", status)
	}
	expectRecorded(t, recorder, &seen)
}

func TestIdempotentCreateReplaysRecordTheSameEvent(t *testing.T) {
	c, recorder := newActivityClient(t)
	seen := 0
	body := map[string]any{"title": "Standup", "allDay": false, "startAt": "2026-10-12T07:00:00Z", "endAt": "2026-10-12T07:15:00Z",
		"recurrence": map[string]any{"frequency": "weekly", "interval": 1}}
	for range 2 {
		if status, raw := c.do(http.MethodPost, "/v1/events", body, "Idempotency-Key", "capture-1"); status != http.StatusCreated {
			t.Fatalf("create: %d %s", status, raw)
		}
	}
	replays := expectRecorded(t, recorder, &seen, eventCreated, eventCreated)
	if replays[0].ID != replays[1].ID || replays[0].Data["recurrence"] != "weekly" {
		t.Fatalf("replays = %+v", replays)
	}
	// A reused key with another payload changes nothing and records nothing.
	body["title"] = "Other"
	if status, _ := c.do(http.MethodPost, "/v1/events", body, "Idempotency-Key", "capture-1"); status != http.StatusConflict {
		t.Fatalf("reused key: %d", status)
	}
	expectRecorded(t, recorder, &seen)
}

func TestImportedEventChangesAreRecordedOnlyWhenAllowed(t *testing.T) {
	c, recorder := newActivityClient(t)
	seen := 0
	source := c.newSource("zaw")
	imported := c.seed(importedParams(source, "uid-1", "Restmüll", "2026-10-20T05:00:00Z"))
	target := "/v1/events/" + googleuuid.UUID(imported.ID.Bytes).String()

	// Its source owns it: edits and deletion are refused and not recorded.
	if status, _ := c.do(http.MethodPatch, target, map[string]any{"title": "Trash"}); status != http.StatusConflict {
		t.Fatalf("edit imported: %d", status)
	}
	if status, _ := c.do(http.MethodDelete, target, nil); status != http.StatusConflict {
		t.Fatalf("delete imported: %d", status)
	}
	expectRecorded(t, recorder, &seen)

	// Its reminders are the owner's to change.
	status, raw := c.do(http.MethodPatch, target, map[string]any{"reminders": []map[string]any{{"kind": "localTime", "daysBefore": 1, "localTime": "19:00"}}},
		activity.ActorHeader, "user")
	if status != http.StatusOK {
		t.Fatalf("reminders: %d %s", status, raw)
	}
	updated := expectRecorded(t, recorder, &seen, eventUpdated)[0]
	changes, _ := updated.Data["changes"].([]any)
	if updated.Actor != activity.User() || len(changes) != 1 || changes[0] != "reminders" || updated.Data["sourceKind"] != "ics" ||
		updated.Data["sourceId"] != source.String() {
		t.Fatalf("updated = %+v", updated)
	}
}
