package google_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/services/google-calendar/internal/google"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/testsupport"
)

// Two-way sync: the owner's edits of events of Google calendars the
// account may edit, made in Life OS (Calendar), reach Google through
// Calendar's change feed, only what changed and with the stored etag; a
// concurrent change in Google wins.

func findEvent(h *testsupport.Harness, title string) calendarEvent {
	h.T.Helper()
	for _, item := range listEvents(h, "2025-11-01T00:00:00Z", "2027-06-01T00:00:00Z", "includeCancelled", "true") {
		if item.Title == title {
			return item
		}
	}
	h.T.Fatalf("no event %q", title)
	return calendarEvent{}
}

func getEvent(h *testsupport.Harness, id string) calendarEvent {
	h.T.Helper()
	var out calendarEvent
	h.Calendar.Call(http.MethodGet, "/v1/events/"+id, nil, http.StatusOK, &out)
	return out
}

func patchEvent(h *testsupport.Harness, id string, body map[string]any) calendarEvent {
	h.T.Helper()
	var out calendarEvent
	h.Calendar.Call(http.MethodPatch, "/v1/events/"+id, body, http.StatusOK, &out)
	return out
}

func fakeEvent(h *testsupport.Harness, calendarID, id string) google.Event {
	h.T.Helper()
	event, ok := h.Fake.Event(calendarID, id)
	if !ok {
		h.T.Fatalf("Google has no event %s", id)
	}
	return event
}

func bodyKeys(body map[string]any) []string {
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func private(event google.Event, key string) string {
	if event.ExtendedProperties == nil {
		return ""
	}
	return event.ExtendedProperties.Private[key]
}

func TestEditsMadeInLifeOSReachGoogle(t *testing.T) {
	h := syncingHarness(t, googleDentist)
	h.Run()
	owner := testsupport.OwnerCalendar.ID
	dentist := findEvent(h, "Dentist")
	stored := fakeEvent(h, owner, "dentist")
	written := len(writesTo(h, owner))

	// Renamed, moved, and given a priority in Life OS: one PATCH with only
	// those fields and the stored etag.
	updated := patchEvent(h, dentist.ID, map[string]any{"title": "Zahnarzt", "startAt": "2026-10-12T10:00:00+02:00", "endAt": "2026-10-12T10:45:00+02:00", "priority": "high"})
	h.Run()
	writes := writesTo(h, owner)[written:]
	if len(writes) != 1 || writes[0].Method != http.MethodPatch || writes[0].EventID != "dentist" || writes[0].IfMatch != stored.ETag ||
		strings.Join(bodyKeys(writes[0].Body), ",") != "end,extendedProperties,start,summary" {
		t.Fatalf("writes = %+v", writes)
	}
	inGoogle := fakeEvent(h, owner, "dentist")
	if inGoogle.Summary != "Zahnarzt" || inGoogle.Location != "Praxis Weber" || inGoogle.Start.DateTime != "2026-10-12T10:00:00+02:00" || private(inGoogle, "lifeosPriority") != "high" {
		t.Fatalf("in Google = %+v", inGoogle)
	}
	// Google's answer came back to Calendar without changing anything.
	if again := getEvent(h, dentist.ID); again.Title != "Zahnarzt" || again.Priority != "high" || !again.StartAt.Equal(time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)) || !again.UpdatedAt.Equal(updated.UpdatedAt) {
		t.Fatalf("after the write: %+v", again)
	}
	// The next listing brings the write back: recognized by its etag.
	h.Clock.Advance(2 * time.Minute)
	h.Run()
	if again := getEvent(h, dentist.ID); !again.UpdatedAt.Equal(updated.UpdatedAt) {
		t.Fatalf("after the echo: %+v", again)
	}
	if len(writesTo(h, owner)) != written+1 {
		t.Fatalf("the echo was written again: %+v", writesTo(h, owner)[written:])
	}

	// Reminders become Google's notifications; one Google cannot hold stays
	// in Life OS only.
	patchEvent(h, dentist.ID, map[string]any{"reminders": []map[string]any{{"kind": "relative", "minutesBefore": 120}}, "useDefaultReminders": false})
	h.Run()
	if got := fakeEvent(h, owner, "dentist").Reminders; got == nil || got.UseDefault || len(got.Overrides) != 1 || got.Overrides[0].Minutes != 120 {
		t.Fatalf("Google reminders = %+v", got)
	}
	patchEvent(h, dentist.ID, map[string]any{"reminders": []map[string]any{{"kind": "localTime", "daysBefore": 1, "localTime": "19:00"}}})
	h.Run()
	if got := fakeEvent(h, owner, "dentist").Reminders; got == nil || len(got.Overrides) != 1 || got.Overrides[0].Minutes != 120 {
		t.Fatalf("Google reminders after a local-time reminder = %+v", got)
	}
	if again := getEvent(h, dentist.ID); len(again.Reminders) != 1 || again.Reminders[0].Kind != "localTime" {
		t.Fatalf("Life OS reminders = %+v", again.Reminders)
	}

	// Google unreachable: the edit waits (and the calendar does not sync
	// over it) until Google answers again.
	h.Fake.Lock()
	h.Fake.WriteStatus = http.StatusServiceUnavailable
	h.Fake.Unlock()
	patchEvent(h, dentist.ID, map[string]any{"title": "Zahnarzt Dr. Weber"})
	h.Clock.Advance(2 * time.Minute)
	h.Run()
	if again := getEvent(h, dentist.ID); again.Title != "Zahnarzt Dr. Weber" || fakeEvent(h, owner, "dentist").Summary != "Zahnarzt" {
		t.Fatalf("during the outage: %q, Google %q", again.Title, fakeEvent(h, owner, "dentist").Summary)
	}
	var connection struct {
		Changes struct{ PendingEdits, FailedEdits int } `json:"changes"`
	}
	h.Call(http.MethodGet, "/v1/connection", nil, http.StatusOK, &connection)
	if connection.Changes.PendingEdits != 1 || connection.Changes.FailedEdits != 1 {
		t.Fatalf("connection during the outage = %+v", connection)
	}
	h.Fake.Lock()
	h.Fake.WriteStatus = 0
	h.Fake.Unlock()
	h.Clock.Advance(time.Minute)
	h.Run()
	if got := fakeEvent(h, owner, "dentist").Summary; got != "Zahnarzt Dr. Weber" {
		t.Fatalf("after the outage Google has %q", got)
	}

	// Changed on the phone meanwhile: Life OS's edit is not written; Google's
	// version comes back to Life OS and the conflict is recorded.
	seen := len(h.Recorder.Events())
	h.Fake.ChangeEvent(owner, "dentist", func(event *google.Event) { event.Summary = "Dentist (moved by phone)" })
	patchEvent(h, dentist.ID, map[string]any{"title": "Zahnarzt Weber"})
	h.Run()
	if got := fakeEvent(h, owner, "dentist").Summary; got != "Dentist (moved by phone)" {
		t.Fatalf("Google overwritten: %q", got)
	}
	if current := getEvent(h, dentist.ID); current.Title != "Dentist (moved by phone)" {
		t.Fatalf("after the conflict: %q", current.Title)
	}
	if conflicts := newEvents(h, &seen, google.EventConflict); len(conflicts) != 1 || conflicts[0].Subject.ID != dentist.ID || conflicts[0].Data["calendar"] != "Owner" {
		t.Fatalf("conflicts = %+v", conflicts)
	}

	// Cancelled: deleted in Google, kept as cancelled history; restored:
	// back in Google.
	patchEvent(h, dentist.ID, map[string]any{"status": "cancelled"})
	h.Run()
	if !fakeEvent(h, owner, "dentist").Cancelled() {
		t.Fatal("a cancelled event is still in Google")
	}
	if current := getEvent(h, dentist.ID); current.Status != "cancelled" {
		t.Fatalf("cancelled = %+v", current)
	}
	patchEvent(h, dentist.ID, map[string]any{"status": "active"})
	h.Run()
	if inGoogle := fakeEvent(h, owner, "dentist"); inGoogle.Cancelled() || inGoogle.Summary != "Dentist (moved by phone)" {
		t.Fatalf("restored in Google = %+v", inGoogle)
	}
	if current := getEvent(h, dentist.ID); current.Status != "active" || current.RemovedFromSourceAt != nil {
		t.Fatalf("restored = %+v", current)
	}

	// Deleted in Life OS: deleted in Google; the next sync brings nothing
	// back.
	h.Calendar.Call(http.MethodDelete, "/v1/events/"+dentist.ID, nil, http.StatusNoContent, nil)
	h.Run()
	if !fakeEvent(h, owner, "dentist").Cancelled() {
		t.Fatal("not deleted in Google")
	}
	h.Clock.Advance(2 * time.Minute)
	h.Run()
	if items := listEvents(h, "2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z", "includeCancelled", "true"); len(items) != 0 {
		t.Fatalf("after deleting: %q", titles(items))
	}
}

func TestSeriesEditedInLifeOSReachGoogle(t *testing.T) {
	h := syncingHarness(t, googleStandup, googleStandupMoved)
	h.Run()
	owner := testsupport.OwnerCalendar.ID
	standup := findEvent(h, "Standup")
	series := standup.ID
	master := fakeEvent(h, owner, "standup")
	written := len(writesTo(h, owner))

	// All events: the master changes; the moved occurrence keeps its own
	// title.
	patchEvent(h, series, map[string]any{"title": "Daily standup"})
	h.Run()
	if writes := writesTo(h, owner)[written:]; len(writes) != 1 || writes[0].EventID != "standup" || writes[0].IfMatch != master.ETag || strings.Join(bodyKeys(writes[0].Body), ",") != "summary" {
		t.Fatalf("writes = %+v", writes)
	}
	week := listEvents(h, "2026-10-19T00:00:00Z", "2026-10-22T00:00:00Z")
	if got := strings.Join(titles(week), "; "); got != "Standup (planning) 10-19T08:00; Daily standup 10-21T07:00" {
		t.Fatalf("week = %q", got)
	}

	// This event: one occurrence moved, written to its instance in Google
	// (read from Google with its etag).
	written = len(writesTo(h, owner))
	h.Calendar.Call(http.MethodPatch, "/v1/events/"+series+"/occurrences/2026-10-26T08:00:00Z", map[string]any{"startAt": "2026-10-26T10:00:00+01:00", "endAt": "2026-10-26T10:15:00+01:00"}, http.StatusOK, nil)
	h.Run()
	writes := writesTo(h, owner)[written:]
	if len(writes) != 1 || writes[0].EventID != "standup_20261026T080000Z" || writes[0].IfMatch == "" || strings.Join(bodyKeys(writes[0].Body), ",") != "end,start" {
		t.Fatalf("occurrence writes = %+v", writes)
	}
	// Skipped: its instance is cancelled in Google.
	h.Calendar.Call(http.MethodPatch, "/v1/events/"+series+"/occurrences/2026-10-28T08:00:00Z", map[string]any{"status": "cancelled"}, http.StatusOK, nil)
	h.Run()
	if skipped := fakeEvent(h, owner, "standup_20261028T080000Z"); !skipped.Cancelled() {
		t.Fatalf("skipped in Google = %+v", skipped)
	}
	week = listEvents(h, "2026-10-26T00:00:00Z", "2026-10-30T00:00:00Z", "includeCancelled", "true")
	if got := strings.Join(titles(week), "; "); got != "Daily standup 10-26T09:00; Daily standup 10-28T08:00 (cancelled)" {
		t.Fatalf("moved week = %q", got)
	}

	// This and following: a new series in Google from 2 November, and the
	// old one ends with the occurrence before (Wednesday 28 October). The new series keeps its
	// Life OS id and takes Google's id as its uid.
	var started calendarEvent
	h.Calendar.Call(http.MethodPost, "/v1/events/"+series+"/occurrences/2026-11-02T08:00:00Z/split", map[string]any{"title": "Team standup"}, http.StatusCreated, &started)
	if !strings.HasPrefix(deref(started.Source.ExternalUID), "lifeos:") || !started.Editable {
		t.Fatalf("started = %+v", started)
	}
	h.Run()
	rekeyed := getEvent(h, started.ID)
	uid := deref(rekeyed.Source.ExternalUID)
	if strings.HasPrefix(uid, "lifeos:") || rekeyed.Title != "Team standup" {
		t.Fatalf("after the split's sync: %+v", rekeyed)
	}
	created := fakeEvent(h, owner, uid)
	if created.Summary != "Team standup" || created.Start.DateTime != "2026-11-02T09:00:00+01:00" || len(created.Recurrence) != 1 ||
		!strings.HasPrefix(created.Recurrence[0], "RRULE:FREQ=WEEKLY;") {
		t.Fatalf("new series in Google = %+v", created)
	}
	if got := fakeEvent(h, owner, "standup").Recurrence; !strings.Contains(got[0], "UNTIL=20261028T080000Z") {
		t.Fatalf("ended series = %q", got)
	}
	if count := len(listEvents(h, "2026-11-02T00:00:00Z", "2026-11-03T00:00:00Z")); count != 1 {
		t.Fatalf("events on 2 November = %d", count)
	}

	// Delete this and following: the new series ends before 16 November;
	// on its first occurrence it is deleted.
	h.Calendar.Call(http.MethodPost, "/v1/events/"+started.ID+"/occurrences/2026-11-16T08:00:00Z/truncate", nil, http.StatusNoContent, nil)
	h.Run()
	if got := fakeEvent(h, owner, uid).Recurrence; !strings.Contains(got[0], "UNTIL=20261111T080000Z") {
		t.Fatalf("truncated = %q", got)
	}
	h.Calendar.Call(http.MethodPost, "/v1/events/"+started.ID+"/occurrences/2026-11-02T08:00:00Z/truncate", nil, http.StatusNoContent, nil)
	h.Run()
	if !fakeEvent(h, owner, uid).Cancelled() {
		t.Fatal("the new series was not deleted in Google")
	}
	h.Clock.Advance(2 * time.Minute)
	h.Run()
	if status, _ := h.Calendar.Do(http.MethodGet, "/v1/events/"+started.ID, nil, "user"); status != http.StatusNotFound {
		t.Fatalf("the deleted series is back: %d", status)
	}
}

// Life OS events are exported to the Life OS calendar from Calendar's change
// feed; a change made to a copy in Google comes back to Life OS.
func TestLifeOSEventsAreExportedToTheLifeOSCalendar(t *testing.T) {
	h := syncingHarness(t)
	h.Run()
	var haircut calendarEvent
	h.Calendar.Call(http.MethodPost, "/v1/events", map[string]any{"title": "Haircut", "allDay": false, "startAt": "2026-10-18T11:00:00+02:00", "endAt": "2026-10-18T12:00:00+02:00", "priority": "high"}, http.StatusCreated, &haircut)
	h.Run()
	var connection struct {
		LifeOsCalendar *struct {
			GoogleCalendarID string `json:"googleCalendarId"`
		} `json:"lifeOsCalendar"`
		Export struct{ Pending, Failed int } `json:"export"`
	}
	h.Call(http.MethodGet, "/v1/connection", nil, http.StatusOK, &connection)
	if connection.LifeOsCalendar == nil || connection.Export.Pending != 0 || connection.Export.Failed != 0 {
		t.Fatalf("connection = %+v", connection)
	}
	lifeOS := connection.LifeOsCalendar.GoogleCalendarID
	copyID := google.ExportID(googleuuid.MustParse(haircut.ID))
	exported := fakeEvent(h, lifeOS, copyID)
	if exported.Summary != "Haircut" || private(exported, "lifeosEventId") != haircut.ID || private(exported, "lifeosPriority") != "high" {
		t.Fatalf("copy = %+v", exported)
	}
	var exports struct {
		Items []struct {
			EventID, CalendarName, State string
			LifeOs                       bool
		} `json:"items"`
	}
	h.Call(http.MethodGet, "/v1/exports?eventId="+haircut.ID, nil, http.StatusOK, &exports)
	if len(exports.Items) != 1 || exports.Items[0].EventID != haircut.ID || exports.Items[0].State != "synced" || !exports.Items[0].LifeOs || exports.Items[0].CalendarName != "Life OS" {
		t.Fatalf("exports = %+v", exports)
	}
	// The copies are not imported as Google events.
	if items := listEvents(h, "2026-10-18T00:00:00Z", "2026-10-19T00:00:00Z"); len(items) != 1 || items[0].Source.Kind != "manual" {
		t.Fatalf("listed = %+v", items)
	}

	// Changed in Life OS: the copy follows.
	patchEvent(h, haircut.ID, map[string]any{"location": "Salon Kamm"})
	h.Run()
	if got := fakeEvent(h, lifeOS, copyID); got.Location != "Salon Kamm" {
		t.Fatalf("copy after the change = %+v", got)
	}

	// Moved on the phone: the Life OS event follows (through Calendar's
	// events API, as the integration).
	h.Fake.ChangeEvent(lifeOS, copyID, func(event *google.Event) {
		event.Start, event.End = gtimed("2026-10-19T11:00:00+02:00"), gtimed("2026-10-19T12:00:00+02:00")
	})
	h.Clock.Advance(2 * time.Minute)
	h.Run()
	if moved := getEvent(h, haircut.ID); !moved.StartAt.Equal(time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)) || deref(moved.Location) != "Salon Kamm" || moved.Priority != "high" {
		t.Fatalf("after the change in Google = %+v", moved)
	}
	written := len(writesTo(h, lifeOS))
	h.Run()
	if again := writesTo(h, lifeOS)[written:]; len(again) != 0 {
		t.Fatalf("the change from Google was exported back: %+v", again)
	}

	// Deleted in Google: cancelled in Life OS, kept as history.
	h.Fake.DeleteEvent(lifeOS, copyID)
	h.Clock.Advance(2 * time.Minute)
	h.Run()
	if cancelled := getEvent(h, haircut.ID); cancelled.Status != "cancelled" {
		t.Fatalf("after the deletion in Google = %+v", cancelled)
	}

	// Restored in Life OS: exported again; deleted in Life OS: the copy goes.
	patchEvent(h, haircut.ID, map[string]any{"status": "active"})
	h.Run()
	if again := fakeEvent(h, lifeOS, copyID); again.Cancelled() {
		t.Fatalf("restored copy = %+v", again)
	}
	h.Calendar.Call(http.MethodDelete, "/v1/events/"+haircut.ID, nil, http.StatusNoContent, nil)
	h.Run()
	if gone := fakeEvent(h, lifeOS, copyID); !gone.Cancelled() {
		t.Fatalf("copy after the deletion = %+v", gone)
	}
	h.Call(http.MethodGet, "/v1/exports?eventId="+haircut.ID, nil, http.StatusOK, &exports)
	if len(exports.Items) != 0 {
		t.Fatalf("exports after the deletion = %+v", exports)
	}
}
