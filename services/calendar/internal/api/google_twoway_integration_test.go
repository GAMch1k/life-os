package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/google"
	"github.com/GAMch1k/life-os/services/calendar/internal/google/fakegoogle"
)

// These tests drive two-way Google sync (P11-03) against the fake Google:
// changes to synced events written to Google first with their etag,
// conflicts, read-only calendars, failures, and the export of Life OS
// events to the Life OS calendar with its outbox.

// syncedEvent is the synced Google event titled title.
func (c *googleClient) syncedEvent(title string) generated.CalendarEvent {
	c.t.Helper()
	for _, item := range c.googleEvents("2026-01-01T00:00:00Z", "2027-06-01T00:00:00Z", "includeCancelled", "true") {
		if item.Title == title {
			return item
		}
	}
	c.t.Fatalf("no synced event %q", title)
	return generated.CalendarEvent{}
}

func (c *googleClient) getEvent(id googleuuid.UUID) generated.CalendarEvent {
	c.t.Helper()
	var out generated.CalendarEvent
	c.call(http.MethodGet, "/v1/events/"+id.String(), nil, http.StatusOK, &out)
	return out
}

func (c *googleClient) patchEvent(id googleuuid.UUID, body map[string]any) generated.CalendarEvent {
	c.t.Helper()
	var out generated.CalendarEvent
	c.call(http.MethodPatch, "/v1/events/"+id.String(), body, http.StatusOK, &out)
	return out
}

func (c *googleClient) exportDue() {
	c.t.Helper()
	c.syncer.ExportDue(context.Background())
}

// writesSince are the fake's writes after the first n.
func (c *googleClient) writesSince(n int) []fakegoogle.Write {
	return c.fake.Writes()[n:]
}

func (c *googleClient) fakeEvent(calendarID, id string) google.Event {
	c.t.Helper()
	event, ok := c.fake.Event(calendarID, id)
	if !ok {
		c.t.Fatalf("Google has no event %s in %s", id, calendarID)
	}
	return event
}

func bodyKeys(body map[string]any) []string {
	out := make([]string, 0, len(body))
	for key := range body {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

func private(event google.Event, key string) string {
	if event.ExtendedProperties == nil {
		return ""
	}
	return event.ExtendedProperties.Private[key]
}

func TestGoogleTwoWayChangesASyncedEventInGoogleFirst(t *testing.T) {
	c := newSyncingClient(t, googleDentist)
	c.syncDue()
	seen := len(c.recorder.all())
	dentist := c.syncedEvent("Dentist")
	if !dentist.Editable || dentist.Google == nil || dentist.Google.LifeOs || dentist.Google.CalendarName != "Owner" {
		t.Fatalf("dentist = %+v", dentist)
	}
	stored := c.fakeEvent(ownerCalendar.ID, "dentist")

	// Renamed, moved, and given a priority: one PATCH with only those
	// fields and the stored etag; the event shows Google's answer.
	updated := c.patchEvent(dentist.Id, map[string]any{"title": "Zahnarzt", "startAt": "2026-10-12T10:00:00+02:00", "endAt": "2026-10-12T10:45:00+02:00", "priority": "high"})
	writes := c.fake.Writes()
	if len(writes) != 1 || writes[0].Method != http.MethodPatch || writes[0].EventID != "dentist" || writes[0].IfMatch != stored.ETag ||
		strings.Join(bodyKeys(writes[0].Body), ",") != "end,extendedProperties,start,summary" {
		t.Fatalf("writes = %+v", writes)
	}
	inGoogle := c.fakeEvent(ownerCalendar.ID, "dentist")
	if inGoogle.Summary != "Zahnarzt" || inGoogle.Location != "Praxis Weber" || inGoogle.Start.DateTime != "2026-10-12T10:00:00+02:00" || private(inGoogle, "lifeosPriority") != "high" {
		t.Fatalf("in Google = %+v", inGoogle)
	}
	if updated.Title != "Zahnarzt" || !updated.StartAt.Equal(time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)) || updated.Priority != generated.High ||
		updated.Google == nil || updated.Google.State != generated.EventGoogleLinkStateSynced {
		t.Fatalf("updated = %+v", updated)
	}
	event := expectRecorded(t, c.recorder, &seen, "calendar.event.rescheduled")[0]
	if event.Actor != activity.User() || event.Data["sourceKind"] != "google" {
		t.Fatalf("rescheduled = %+v", event)
	}

	// The next sync brings Calendar's own write back: recognized by its
	// etag, it changes nothing and records nothing.
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	expectRecorded(t, c.recorder, &seen)
	if again := c.getEvent(dentist.Id); !again.UpdatedAt.Equal(updated.UpdatedAt) || again.Title != "Zahnarzt" || again.Priority != generated.High {
		t.Fatalf("after the echo: %+v", again)
	}

	// Reminders become Google's notifications; one Google cannot hold is
	// refused before anything is written.
	reminded := c.patchEvent(dentist.Id, map[string]any{"reminders": []map[string]any{{"kind": "relative", "minutesBefore": 120}}, "useDefaultReminders": false})
	if remindersOf(reminded.Reminders) != "120" || reminded.UseDefaultReminders {
		t.Fatalf("reminders = %+v", reminded)
	}
	if got := c.fakeEvent(ownerCalendar.ID, "dentist").Reminders; got == nil || got.UseDefault || len(got.Overrides) != 1 || got.Overrides[0].Minutes != 120 {
		t.Fatalf("Google reminders = %+v", got)
	}
	expectRecorded(t, c.recorder, &seen, "calendar.event.updated")
	before := len(c.fake.Writes())
	c.failure(http.MethodPatch, "/v1/events/"+dentist.Id.String(), map[string]any{"reminders": []map[string]any{{"kind": "localTime", "daysBefore": 1, "localTime": "19:00"}}}, http.StatusBadRequest, "bad_request")
	c.failure(http.MethodPatch, "/v1/events/"+dentist.Id.String(), map[string]any{"endAt": "2026-10-12T07:00:00+02:00"}, http.StatusBadRequest, "bad_request")
	if len(c.fake.Writes()) != before {
		t.Fatal("a refused change was written to Google")
	}

	// Google unreachable: the change fails and nothing changes in Life OS.
	c.fake.Lock()
	c.fake.WriteStatus = http.StatusServiceUnavailable
	c.fake.Unlock()
	c.failure(http.MethodPatch, "/v1/events/"+dentist.Id.String(), map[string]any{"title": "Lost"}, http.StatusBadGateway, "google_unavailable")
	c.fake.Lock()
	c.fake.WriteStatus = 0
	c.fake.Unlock()
	if current := c.getEvent(dentist.Id); current.Title != "Zahnarzt" {
		t.Fatalf("after an outage: %q", current.Title)
	}
	expectRecorded(t, c.recorder, &seen)

	// Changed on the phone meanwhile: Life OS's change is not written; the
	// event shows Google's version and the conflict is recorded.
	c.fake.ChangeEvent(ownerCalendar.ID, "dentist", func(event *google.Event) { event.Summary = "Dentist (moved by phone)" })
	message := c.failure(http.MethodPatch, "/v1/events/"+dentist.Id.String(), map[string]any{"title": "Zahnarzt Dr. Weber"}, http.StatusConflict, "google_conflict")
	if message != "Changed in Google meanwhile; showing the latest version" {
		t.Fatalf("conflict message = %q", message)
	}
	if current := c.getEvent(dentist.Id); current.Title != "Dentist (moved by phone)" {
		t.Fatalf("after the conflict: %q", current.Title)
	}
	if got := c.fakeEvent(ownerCalendar.ID, "dentist").Summary; got != "Dentist (moved by phone)" {
		t.Fatalf("Google overwritten: %q", got)
	}
	event = expectRecorded(t, c.recorder, &seen, google.EventConflict)[0]
	if event.Actor != activity.User() || event.Outcome != activity.Failed || event.Subject.ID != dentist.Id.String() || event.Data["calendar"] != "Owner" {
		t.Fatalf("conflict = %+v", event)
	}

	// Cancelled: deleted in Google, kept as cancelled history; restored:
	// back in Google.
	cancelled := c.patchEvent(dentist.Id, map[string]any{"status": "cancelled"})
	if cancelled.Status != generated.Cancelled || !c.fakeEvent(ownerCalendar.ID, "dentist").Cancelled() {
		t.Fatalf("cancelled = %+v", cancelled)
	}
	expectRecorded(t, c.recorder, &seen, "calendar.event.cancelled")
	restored := c.patchEvent(dentist.Id, map[string]any{"status": "active"})
	if inGoogle = c.fakeEvent(ownerCalendar.ID, "dentist"); restored.Status != generated.Active || restored.RemovedFromSourceAt != nil || inGoogle.Cancelled() || inGoogle.Summary != "Dentist (moved by phone)" {
		t.Fatalf("restored = %+v, in Google %+v", restored, inGoogle)
	}
	expectRecorded(t, c.recorder, &seen, "calendar.event.restored")

	// Deleted: in Google, then in Life OS; the next sync brings nothing back.
	c.call(http.MethodDelete, "/v1/events/"+dentist.Id.String(), nil, http.StatusNoContent, nil)
	c.failure(http.MethodGet, "/v1/events/"+dentist.Id.String(), nil, http.StatusNotFound, "not_found")
	if !c.fakeEvent(ownerCalendar.ID, "dentist").Cancelled() {
		t.Fatal("not deleted in Google")
	}
	expectRecorded(t, c.recorder, &seen, "calendar.event.deleted")
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	if items := c.googleEvents("2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z", "includeCancelled", "true"); len(items) != 0 {
		t.Fatalf("after deleting: %q", titles(items))
	}

	// The connection expired: changes fail with a clear error.
	c.fake.PutEvent(ownerCalendar.ID, googleTrip)
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	trip := c.syncedEvent("Trip to Hamburg")
	c.fake.RevokeAll()
	c.restart(googleConfig())
	if message := c.failure(http.MethodPatch, "/v1/events/"+trip.Id.String(), map[string]any{"title": "Trip"}, http.StatusConflict, "google_expired"); !strings.Contains(message, "reconnect Google") {
		t.Fatalf("expired message = %q", message)
	}
	if current := c.getEvent(trip.Id); current.Title != "Trip to Hamburg" {
		t.Fatalf("after expiry: %q", current.Title)
	}
}

func TestGoogleTwoWayKeepsReadOnlyCalendarsReadOnly(t *testing.T) {
	c := newGoogleClient(t, ownerCalendar, familyCalendar, holidayCalendar)
	christmas := google.Event{ID: "christmas", Summary: "Christmas", Start: gday("2026-12-25"), End: gday("2026-12-26")}
	dinner := google.Event{ID: "dinner", Summary: "Family dinner", Start: gtimed("2026-10-18T19:00:00+02:00"), End: gtimed("2026-10-18T21:00:00+02:00")}
	c.fake.PutEvent(holidayCalendar.ID, christmas)
	c.fake.PutEvent(familyCalendar.ID, dinner)
	c.connect()
	for _, calendar := range c.calendars() {
		if !calendar.SyncEnabled {
			c.call(http.MethodPatch, "/v1/google/calendars/"+calendar.Id.String(), map[string]any{"syncEnabled": true}, http.StatusOK, nil)
		}
	}
	c.syncDue()

	// A calendar the account can only read: read-only with the reason;
	// only reminders and priority change, in Life OS alone.
	holiday := c.syncedEvent("Christmas")
	if holiday.Editable || holiday.ReadOnlyReason == nil || !strings.Contains(*holiday.ReadOnlyReason, "you can only read") ||
		holiday.Google == nil || holiday.Google.CalendarName != "Holidays in Germany" {
		t.Fatalf("holiday = %+v", holiday)
	}
	message := c.failure(http.MethodPatch, "/v1/events/"+holiday.Id.String(), map[string]any{"title": "Weihnachten"}, http.StatusConflict, "google_read_only")
	if !strings.Contains(message, "Holidays in Germany") {
		t.Fatalf("read-only message = %q", message)
	}
	c.failure(http.MethodDelete, "/v1/events/"+holiday.Id.String(), nil, http.StatusConflict, "google_read_only")
	reminded := c.patchEvent(holiday.Id, map[string]any{"reminders": []map[string]any{{"kind": "localTime", "daysBefore": 1, "localTime": "18:00"}}, "priority": "low"})
	if len(reminded.Reminders) != 1 || reminded.Priority != generated.Low {
		t.Fatalf("holiday reminders = %+v", reminded)
	}
	if writes := c.fake.Writes(); len(writes) != 0 {
		t.Fatalf("writes to Google = %+v", writes)
	}
	// The owner's own reminders stay when Google changes the event.
	c.fake.ChangeEvent(holidayCalendar.ID, "christmas", func(event *google.Event) { event.Summary = "Christmas Day" })
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	if current := c.syncedEvent("Christmas Day"); len(current.Reminders) != 1 || current.Priority != generated.Low {
		t.Fatalf("after Google's change: %+v", current)
	}

	// Google refuses a calendar that became read-only before Life OS read
	// the list again: nothing changes, and the answer says why.
	family := c.syncedEvent("Family dinner")
	if !family.Editable {
		t.Fatalf("family = %+v", family)
	}
	c.fake.Lock()
	for index := range c.fake.Calendars {
		if c.fake.Calendars[index].ID == familyCalendar.ID {
			c.fake.Calendars[index].AccessRole = "reader"
		}
	}
	c.fake.Unlock()
	if message := c.failure(http.MethodPatch, "/v1/events/"+family.Id.String(), map[string]any{"title": "Dinner"}, http.StatusConflict, "google_read_only"); !strings.Contains(message, "Family") {
		t.Fatalf("refused message = %q", message)
	}
	if current := c.getEvent(family.Id); current.Title != "Family dinner" {
		t.Fatalf("after the refusal: %q", current.Title)
	}
}

func TestGoogleTwoWayChangesSeries(t *testing.T) {
	c := newSyncingClient(t, googleStandup, googleStandupMoved)
	c.syncDue()
	seen := len(c.recorder.all())
	standup := c.syncedEvent("Standup")
	if !standup.Editable || standup.Occurrence == nil {
		t.Fatalf("standup = %+v", standup)
	}
	series := standup.Id
	master := c.fakeEvent(ownerCalendar.ID, "standup")

	// All events: the master changes; the moved occurrence keeps its own
	// title.
	c.patchEvent(series, map[string]any{"title": "Daily standup"})
	if writes := c.fake.Writes(); len(writes) != 1 || writes[0].EventID != "standup" || writes[0].IfMatch != master.ETag || strings.Join(bodyKeys(writes[0].Body), ",") != "summary" {
		t.Fatalf("writes = %+v", writes)
	}
	expectRecorded(t, c.recorder, &seen, "calendar.event.updated")
	week := c.googleEvents("2026-10-19T00:00:00Z", "2026-10-22T00:00:00Z")
	equal(t, "week", titles(week), []string{"Standup (planning) 10-19T08:00", "Daily standup 10-21T07:00"})

	// This event: one occurrence moved through its instance in Google (an
	// instance Life OS never stored: read from Google with its etag).
	written := len(c.fake.Writes())
	var moved generated.CalendarEvent
	c.call(http.MethodPatch, "/v1/events/"+series.String()+"/occurrences/2026-10-26T08:00:00Z", map[string]any{"startAt": "2026-10-26T10:00:00+01:00", "endAt": "2026-10-26T10:15:00+01:00"}, http.StatusOK, &moved)
	writes := c.writesSince(written)
	if len(writes) != 1 || writes[0].EventID != "standup_20261026T080000Z" || writes[0].IfMatch == "" || strings.Join(bodyKeys(writes[0].Body), ",") != "end,start" {
		t.Fatalf("occurrence writes = %+v", writes)
	}
	if moved.Occurrence == nil || moved.Occurrence.RecurrenceId != "2026-10-26T08:00:00Z" || !moved.Occurrence.Modified || !moved.StartAt.Equal(time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("moved = %+v", moved)
	}
	expectRecorded(t, c.recorder, &seen, "calendar.event.rescheduled")
	// An occurrence skipped: its instance is cancelled in Google.
	c.call(http.MethodPatch, "/v1/events/"+series.String()+"/occurrences/2026-10-28T08:00:00Z", map[string]any{"status": "cancelled"}, http.StatusOK, nil)
	if skipped := c.fakeEvent(ownerCalendar.ID, "standup_20261028T080000Z"); !skipped.Cancelled() {
		t.Fatalf("skipped in Google = %+v", skipped)
	}
	expectRecorded(t, c.recorder, &seen, "calendar.event.cancelled")
	// The changed instance Life OS stored is written with its stored etag.
	planning := c.fakeEvent(ownerCalendar.ID, "standup_20261019T070000Z")
	written = len(c.fake.Writes())
	c.call(http.MethodPatch, "/v1/events/"+series.String()+"/occurrences/2026-10-19T07:00:00Z", map[string]any{"location": "Room 4"}, http.StatusOK, nil)
	if writes = c.writesSince(written); len(writes) != 1 || writes[0].IfMatch != planning.ETag {
		t.Fatalf("stored instance writes = %+v", writes)
	}
	expectRecorded(t, c.recorder, &seen, "calendar.event.updated")
	week = c.googleEvents("2026-10-26T00:00:00Z", "2026-10-30T00:00:00Z", "includeCancelled", "true")
	equal(t, "moved week", titles(week), []string{"Daily standup 10-26T09:00", "Daily standup 10-28T08:00 (cancelled)"})

	// A new rule (Mondays only) keeps the skip Google holds as EXDATE.
	c.patchEvent(series, map[string]any{"recurrence": map[string]any{"frequency": "weekly", "interval": 1, "byWeekday": []string{"MO"}, "until": "2026-12-31T22:59:59Z"}})
	if got := c.fakeEvent(ownerCalendar.ID, "standup").Recurrence; strings.Join(got, " | ") != "RRULE:FREQ=WEEKLY;BYDAY=MO;UNTIL=20261231T225959Z | EXDATE;TZID=Europe/Berlin:20261014T090000" {
		t.Fatalf("recurrence = %q", got)
	}
	expectRecorded(t, c.recorder, &seen, "calendar.event.updated")

	// This and following: a new series in Google from 2 November, and the
	// old one ends with the occurrence before.
	var started generated.CalendarEvent
	c.call(http.MethodPost, "/v1/events/"+series.String()+"/occurrences/2026-11-02T08:00:00Z/split", map[string]any{"title": "Team standup"}, http.StatusCreated, &started)
	if started.Title != "Team standup" || started.Id == series || started.Recurrence == nil || started.Source.ExternalUid == nil || !started.Editable {
		t.Fatalf("started = %+v", started)
	}
	created := c.fakeEvent(ownerCalendar.ID, *started.Source.ExternalUid)
	if created.Summary != "Team standup" || created.Start.DateTime != "2026-11-02T09:00:00+01:00" || len(created.Recurrence) != 1 ||
		!strings.HasPrefix(created.Recurrence[0], "RRULE:FREQ=WEEKLY;") || !strings.HasSuffix(created.Recurrence[0], "UNTIL=20261231T225959Z") {
		t.Fatalf("new series in Google = %+v", created)
	}
	if got := c.fakeEvent(ownerCalendar.ID, "standup").Recurrence; !strings.Contains(got[0], "UNTIL=20261026T080000Z") {
		t.Fatalf("ended series = %q", got)
	}
	expectRecorded(t, c.recorder, &seen, "calendar.event.updated", "calendar.event.created")
	if ended := c.getEvent(series); ended.Recurrence == nil || ended.Recurrence.Until == nil || !ended.Recurrence.Until.Equal(time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("ended = %+v", ended.Recurrence)
	}

	// Delete this and following: the new series ends before 16 November;
	// on its first occurrence it is deleted.
	c.call(http.MethodPost, "/v1/events/"+started.Id.String()+"/occurrences/2026-11-16T08:00:00Z/truncate", nil, http.StatusNoContent, nil)
	if got := c.fakeEvent(ownerCalendar.ID, created.ID).Recurrence; !strings.Contains(got[0], "UNTIL=20261109T080000Z") {
		t.Fatalf("truncated = %q", got)
	}
	expectRecorded(t, c.recorder, &seen, "calendar.event.updated")
	c.call(http.MethodPost, "/v1/events/"+started.Id.String()+"/occurrences/2026-11-02T08:00:00Z/truncate", nil, http.StatusNoContent, nil)
	if !c.fakeEvent(ownerCalendar.ID, created.ID).Cancelled() {
		t.Fatal("the new series was not deleted in Google")
	}
	c.failure(http.MethodGet, "/v1/events/"+started.Id.String(), nil, http.StatusNotFound, "not_found")
	expectRecorded(t, c.recorder, &seen, "calendar.event.deleted")

	// None of it comes back as a change at the next sync.
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	expectRecorded(t, c.recorder, &seen)
}

// lifeOSCalendar is the Life OS calendar's Google id.
func (c *googleClient) lifeOSCalendar() string {
	c.t.Helper()
	connection := c.connection()
	if connection.LifeOsCalendar == nil {
		c.t.Fatal("no Life OS calendar")
	}
	return connection.LifeOsCalendar.GoogleCalendarId
}

func (c *googleClient) createEvent(body map[string]any) generated.CalendarEvent {
	c.t.Helper()
	var out generated.CalendarEvent
	c.call(http.MethodPost, "/v1/events", body, http.StatusCreated, &out)
	return out
}

func TestGoogleExportsLifeOSEventsToTheLifeOSCalendar(t *testing.T) {
	c := newSyncingClient(t)
	c.syncDue()
	seen := len(c.recorder.all())
	haircut := c.createEvent(map[string]any{
		"title": "Haircut", "allDay": false, "startAt": "2026-10-18T11:00:00+02:00", "endAt": "2026-10-18T12:00:00+02:00",
		"reminders": []map[string]any{{"kind": "relative", "minutesBefore": 30}}, "priority": "high",
	})
	if haircut.Google == nil || !haircut.Google.LifeOs || haircut.Google.State != generated.EventGoogleLinkStatePending || haircut.Google.CalendarName != "Life OS" || !haircut.Editable {
		t.Fatalf("created = %+v", haircut.Google)
	}
	expectRecorded(t, c.recorder, &seen, "calendar.event.created")

	// The worker creates the Life OS calendar (the owner's timezone, sync
	// on) and writes the event to it with its fixed id.
	c.exportDue()
	lifeOS := c.lifeOSCalendar()
	if zone := c.fake.CalendarTimeZone(lifeOS); zone != "Europe/Berlin" {
		t.Fatalf("calendar time zone = %q", zone)
	}
	copyID := google.ExportID(googleuuid.UUID(haircut.Id))
	copied := c.fakeEvent(lifeOS, copyID)
	if copied.Summary != "Haircut" || copied.Start.DateTime != "2026-10-18T11:00:00+02:00" || copied.Start.TimeZone != "Europe/Berlin" ||
		copied.Reminders == nil || copied.Reminders.UseDefault || len(copied.Reminders.Overrides) != 1 || copied.Reminders.Overrides[0].Minutes != 30 ||
		private(copied, "lifeosEventId") != haircut.Id.String() || private(copied, "lifeosPriority") != "high" {
		t.Fatalf("copy = %+v", copied)
	}
	event := expectRecorded(t, c.recorder, &seen, google.EventCalendarCreated)[0]
	if event.Actor != google.SyncActor || !strings.Contains(event.Summary, "Life OS") {
		t.Fatalf("calendar created = %+v", event)
	}
	connection := c.connection()
	var listed generated.GoogleCalendar
	for _, calendar := range c.calendars() {
		if calendar.LifeOs {
			listed = calendar
		}
	}
	if !connection.ExportEnabled || connection.Export.Pending != 0 || connection.Export.Failed != 0 || connection.LifeOsCalendar.CalendarId == nil ||
		listed.GoogleCalendarId != lifeOS || !listed.SyncEnabled || listed.Summary != "Life OS" {
		t.Fatalf("connection = %+v, listed = %+v", connection, listed)
	}
	if got := c.getEvent(haircut.Id); got.Google == nil || got.Google.State != generated.EventGoogleLinkStateSynced || got.Google.CalendarId == nil || *got.Google.CalendarId != listed.Id {
		t.Fatalf("exported = %+v", got.Google)
	}

	// A change is exported with the copy's etag.
	written := len(c.fake.Writes())
	c.patchEvent(haircut.Id, map[string]any{"title": "Haircut at Mia's"})
	expectRecorded(t, c.recorder, &seen, "calendar.event.updated")
	c.exportDue()
	writes := c.writesSince(written)
	if len(writes) != 1 || writes[0].Method != http.MethodPatch || writes[0].IfMatch != copied.ETag || c.fakeEvent(lifeOS, copyID).Summary != "Haircut at Mia's" {
		t.Fatalf("export writes = %+v", writes)
	}

	// The Life OS calendar syncs: its copies are not imported again.
	c.syncDue()
	for _, item := range c.googleEvents("2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z", "includeCancelled", "true") {
		t.Errorf("a copy was imported: %+v", item)
	}
	expectRecorded(t, c.recorder, &seen, google.EventSourceRefreshed)

	// Moved on the phone: the Life OS event follows (it stays a Life OS
	// event); the owner's reminders and priority stay.
	c.fake.ChangeEvent(lifeOS, copyID, func(event *google.Event) {
		event.Start, event.End = gtimed("2026-10-19T11:00:00+02:00"), gtimed("2026-10-19T12:00:00+02:00")
	})
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	followed := c.getEvent(haircut.Id)
	if followed.Source.Kind != generated.EventSourceKindManual || !followed.StartAt.Equal(time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)) ||
		remindersOf(followed.Reminders) != "30" || followed.Priority != generated.High || followed.Google.State != generated.EventGoogleLinkStateSynced {
		t.Fatalf("followed = %+v", followed)
	}
	event = expectRecorded(t, c.recorder, &seen, "calendar.event.rescheduled")[0]
	if event.Actor != google.SyncActor || event.Data["changedIn"] != "google" {
		t.Fatalf("followed event = %+v", event)
	}
	written = len(c.fake.Writes())
	c.exportDue()
	if writes := c.writesSince(written); len(writes) != 0 {
		t.Fatalf("a change from Google was written back: %+v", writes)
	}

	// Changed in Life OS and on the phone before the export: Google's
	// version is kept and the conflict recorded.
	c.patchEvent(haircut.Id, map[string]any{"location": "Salon Mia"})
	expectRecorded(t, c.recorder, &seen, "calendar.event.updated")
	c.fake.ChangeEvent(lifeOS, copyID, func(event *google.Event) { event.Summary = "Haircut (phone)" })
	c.exportDue()
	kept := c.getEvent(haircut.Id)
	if kept.Title != "Haircut (phone)" || kept.Location != nil || kept.Google.State != generated.EventGoogleLinkStateSynced {
		t.Fatalf("after the export conflict: %+v", kept)
	}
	if c.fakeEvent(lifeOS, copyID).Location != "" {
		t.Fatal("the conflicting change was written to Google")
	}
	recorded := expectRecorded(t, c.recorder, &seen, "calendar.event.updated", google.EventConflict)
	if recorded[1].Actor != google.SyncActor || recorded[1].Data["kind"] != "export" {
		t.Fatalf("export conflict = %+v", recorded[1])
	}

	// Deleted on the phone: the Life OS event is cancelled (kept as
	// history); restoring it in Life OS writes the copy again.
	c.fake.DeleteEvent(lifeOS, copyID)
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	if gone := c.getEvent(haircut.Id); gone.Status != generated.Cancelled || gone.Google != nil {
		t.Fatalf("deleted on the phone: %+v", gone)
	}
	expectRecorded(t, c.recorder, &seen, "calendar.event.cancelled")
	c.patchEvent(haircut.Id, map[string]any{"status": "active"})
	c.exportDue()
	if back := c.fakeEvent(lifeOS, copyID); back.Cancelled() || back.Summary != "Haircut (phone)" {
		t.Fatalf("restored copy = %+v", back)
	}
	expectRecorded(t, c.recorder, &seen, "calendar.event.restored")

	// Cancelled in Life OS: the copy is deleted; deleted in Life OS: the
	// copy goes too (the outbox remembers it after the event is gone).
	c.patchEvent(haircut.Id, map[string]any{"status": "cancelled"})
	c.exportDue()
	if !c.fakeEvent(lifeOS, copyID).Cancelled() || c.getEvent(haircut.Id).Google != nil {
		t.Fatal("the cancelled event's copy is still in Google")
	}
	c.patchEvent(haircut.Id, map[string]any{"status": "active"})
	c.exportDue()
	c.call(http.MethodDelete, "/v1/events/"+haircut.Id.String(), nil, http.StatusNoContent, nil)
	c.exportDue()
	var exports int
	if err := c.pool.QueryRow(context.Background(), `SELECT count(*) FROM google_exports`).Scan(&exports); err != nil || exports != 0 {
		t.Fatalf("exports left: %d %v", exports, err)
	}
	if !c.fakeEvent(lifeOS, copyID).Cancelled() {
		t.Fatal("the deleted event's copy is still in Google")
	}
	expectRecorded(t, c.recorder, &seen, "calendar.event.cancelled", "calendar.event.restored", "calendar.event.deleted")
}

func TestGoogleExportsSeriesWithTheirOccurrenceChanges(t *testing.T) {
	c := newSyncingClient(t)
	c.syncDue()
	gym := c.createEvent(map[string]any{
		"title": "Gym", "allDay": false, "startAt": "2026-10-12T07:00:00+02:00", "endAt": "2026-10-12T08:00:00+02:00", "timeZone": "Europe/Berlin",
		"recurrence": map[string]any{"frequency": "weekly", "interval": 1, "byWeekday": []string{"MO", "TH"}, "count": 8},
	})
	c.call(http.MethodPatch, "/v1/events/"+gym.Id.String()+"/occurrences/2026-10-15T05:00:00Z", map[string]any{"status": "cancelled"}, http.StatusOK, nil)
	c.call(http.MethodPatch, "/v1/events/"+gym.Id.String()+"/occurrences/2026-10-19T05:00:00Z", map[string]any{"title": "Gym with Max", "startAt": "2026-10-19T08:00:00+02:00", "endAt": "2026-10-19T09:00:00+02:00"}, http.StatusOK, nil)
	c.exportDue()
	lifeOS := c.lifeOSCalendar()
	copyID := google.ExportID(googleuuid.UUID(gym.Id))
	master := c.fakeEvent(lifeOS, copyID)
	if strings.Join(master.Recurrence, " | ") != "RRULE:FREQ=WEEKLY;BYDAY=MO,TH;COUNT=8 | EXDATE:20261015T050000Z" {
		t.Fatalf("recurrence = %q", master.Recurrence)
	}
	instance := c.fakeEvent(lifeOS, copyID+"_20261019T050000Z")
	if instance.Summary != "Gym with Max" || instance.Start.DateTime != "2026-10-19T08:00:00+02:00" || instance.RecurringEventID != copyID {
		t.Fatalf("instance = %+v", instance)
	}
	// The Life OS calendar's sync recognizes all of it as Calendar's own
	// writes: the series is not changed back.
	seen := len(c.recorder.all())
	c.syncDue()
	expectRecorded(t, c.recorder, &seen, google.EventSourceRefreshed)
	week := c.googleEvents("2026-10-12T00:00:00Z", "2026-10-26T00:00:00Z")
	if len(week) != 0 {
		t.Fatalf("copies imported: %q", titles(week))
	}
	var listed generated.EventList
	c.call(http.MethodGet, "/v1/events?from=2026-10-12T00:00:00Z&to=2026-10-26T00:00:00Z&sourceKind=manual", nil, http.StatusOK, &listed)
	equal(t, "gym", titles(listed.Items), []string{"Gym 10-12T05:00", "Gym with Max 10-19T06:00", "Gym 10-22T05:00"})

	// An occurrence moved back to the series: its instance is put back.
	c.call(http.MethodPatch, "/v1/events/"+gym.Id.String()+"/occurrences/2026-10-19T05:00:00Z", map[string]any{"title": "Gym", "startAt": "2026-10-19T07:00:00+02:00", "endAt": "2026-10-19T08:00:00+02:00"}, http.StatusOK, nil)
	c.exportDue()
	if instance = c.fakeEvent(lifeOS, copyID+"_20261019T050000Z"); instance.Summary != "Gym" || instance.Start.DateTime != "2026-10-19T07:00:00+02:00" {
		t.Fatalf("instance put back = %+v", instance)
	}
	// An occurrence moved on the phone follows in Life OS.
	c.fake.ChangeEvent(lifeOS, copyID+"_20261019T050000Z", func(event *google.Event) {
		event.Start, event.End = gtimed("2026-10-19T18:00:00+02:00"), gtimed("2026-10-19T19:00:00+02:00")
	})
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	c.call(http.MethodGet, "/v1/events?from=2026-10-19T00:00:00Z&to=2026-10-20T00:00:00Z&sourceKind=manual", nil, http.StatusOK, &listed)
	equal(t, "moved on the phone", titles(listed.Items), []string{"Gym 10-19T16:00"})
}

func TestGoogleExportRetriesWithBackoff(t *testing.T) {
	c := newSyncingClient(t)
	c.syncDue()
	c.exportDue() // the Life OS calendar
	seen := len(c.recorder.all())
	c.fake.Lock()
	c.fake.WriteStatus = http.StatusServiceUnavailable
	c.fake.Unlock()
	walk := c.createEvent(map[string]any{"title": "Walk", "allDay": true, "startDate": "2026-10-20", "endDate": "2026-10-20"})
	c.exportDue()
	failed := c.getEvent(walk.Id)
	if failed.Google == nil || failed.Google.State != generated.EventGoogleLinkStateFailed || failed.Google.Error == nil || !strings.Contains(*failed.Google.Error, "unavailable") {
		t.Fatalf("failed = %+v", failed.Google)
	}
	if export := c.connection().Export; export.Failed != 1 || export.LastError == nil {
		t.Fatalf("export state = %+v", export)
	}
	event := expectRecorded(t, c.recorder, &seen, "calendar.event.created", google.EventExportFailed)[1]
	if event.Outcome != activity.Failed || event.Subject.ID != walk.Id.String() || !strings.Contains(event.Summary, "Walk") {
		t.Fatalf("export failed = %+v", event)
	}
	// Not retried before the backoff; retried after it, recording nothing
	// new for the same failure.
	written := len(c.fake.Writes())
	c.exportDue()
	if len(c.fake.Writes()) != written {
		t.Fatal("retried before the backoff")
	}
	c.clock.Advance(2 * time.Minute)
	c.exportDue()
	if len(c.fake.Writes()) != written+1 {
		t.Fatal("not retried after the backoff")
	}
	expectRecorded(t, c.recorder, &seen)
	// Google is back: exported at the next attempt (4 minutes later).
	c.fake.Lock()
	c.fake.WriteStatus = 0
	c.fake.Unlock()
	c.clock.Advance(3 * time.Minute)
	c.exportDue()
	if got := c.getEvent(walk.Id).Google; got.State != generated.EventGoogleLinkStateFailed || len(c.fake.Writes()) != written+1 {
		t.Fatalf("before the second backoff = %+v", got)
	}
	c.clock.Advance(time.Minute)
	c.exportDue()
	if got := c.getEvent(walk.Id).Google; got.State != generated.EventGoogleLinkStateSynced {
		t.Fatalf("after recovery = %+v", got)
	}
	if export := c.connection().Export; export.Failed != 0 || export.Pending != 0 || export.LastExportedAt == nil {
		t.Fatalf("export state after recovery = %+v", export)
	}
	copied := c.fakeEvent(c.lifeOSCalendar(), google.ExportID(googleuuid.UUID(walk.Id)))
	if copied.Start.Date != "2026-10-20" || copied.End.Date != "2026-10-21" {
		t.Fatalf("all-day copy = %+v", copied)
	}

	// The connection expired: exports wait (nothing is lost) and go out
	// once Google is connected again.
	c.fake.RevokeAll()
	c.restart(googleConfig())
	run := c.createEvent(map[string]any{"title": "Run", "allDay": true, "startDate": "2026-10-21", "endDate": "2026-10-21"})
	c.exportDue()
	if got := c.getEvent(run.Id).Google; got == nil || got.State == generated.EventGoogleLinkStateSynced {
		t.Fatalf("exported while expired = %+v", got)
	}
	if c.connection().Status != "expired" {
		t.Fatal("the connection did not expire")
	}
	c.connect()
	c.exportDue()
	if got := c.getEvent(run.Id).Google; got.State != generated.EventGoogleLinkStateSynced {
		t.Fatalf("after reconnecting = %+v", got)
	}
}

func TestGoogleLifeOSCalendarDeletedInGoogleIsCreatedAgain(t *testing.T) {
	c := newSyncingClient(t)
	c.syncDue()
	walk := c.createEvent(map[string]any{"title": "Walk", "allDay": true, "startDate": "2026-10-20", "endDate": "2026-10-20"})
	c.exportDue()
	first := c.lifeOSCalendar()
	seen := len(c.recorder.all())
	// The owner deletes the calendar in Google.
	c.fake.Lock()
	c.fake.Calendars = slices.DeleteFunc(c.fake.Calendars, func(calendar fakegoogle.Calendar) bool { return calendar.ID == first })
	c.fake.Unlock()
	c.patchEvent(walk.Id, map[string]any{"title": "Long walk"})
	c.exportDue()
	connection := c.connection()
	second := connection.LifeOsCalendar.GoogleCalendarId
	if second == first || connection.LifeOsCalendar.Note == nil || !strings.Contains(*connection.LifeOsCalendar.Note, "deleted in Google") {
		t.Fatalf("Life OS calendar = %+v", connection.LifeOsCalendar)
	}
	if copied := c.fakeEvent(second, google.ExportID(googleuuid.UUID(walk.Id))); copied.Summary != "Long walk" {
		t.Fatalf("copy in the new calendar = %+v", copied)
	}
	if got := c.getEvent(walk.Id).Google; got.State != generated.EventGoogleLinkStateSynced {
		t.Fatalf("export = %+v", got)
	}
	recorded := expectRecorded(t, c.recorder, &seen, "calendar.event.updated", google.EventCalendarCreated)
	if recorded[1].Data["recreated"] != true || !strings.Contains(recorded[1].Summary, "created it again") {
		t.Fatalf("recreated = %+v", recorded[1])
	}
}

func TestGoogleExportToggle(t *testing.T) {
	c := newGoogleClient(t, ownerCalendar)
	c.fake.SetCalendarDetails(ownerCalendar.ID, "Europe/Berlin")
	upcoming := c.createEvent(map[string]any{"title": "Dentist", "allDay": false, "startAt": "2026-10-20T09:00:00+02:00", "endAt": "2026-10-20T10:00:00+02:00"})
	past := c.createEvent(map[string]any{"title": "Old party", "allDay": false, "startAt": "2026-09-20T19:00:00+02:00", "endAt": "2026-09-20T23:00:00+02:00"})
	if upcoming.Google != nil {
		t.Fatalf("exported before connecting: %+v", upcoming.Google)
	}
	// Connecting exports what is still ahead.
	c.connect()
	c.exportDue()
	lifeOS := c.lifeOSCalendar()
	if events := c.fake.Events(lifeOS); len(events) != 1 || events[0].Summary != "Dentist" || c.getEvent(past.Id).Google != nil {
		t.Fatalf("exported = %+v", events)
	}
	// Switched off: nothing more is written.
	var connection generated.GoogleConnection
	c.call(http.MethodPatch, "/v1/google/connection", map[string]any{"exportEnabled": false}, http.StatusOK, &connection)
	if connection.ExportEnabled {
		t.Fatalf("connection = %+v", connection)
	}
	later := c.createEvent(map[string]any{"title": "Concert", "allDay": false, "startAt": "2026-10-24T20:00:00+02:00", "endAt": "2026-10-24T22:00:00+02:00"})
	c.exportDue()
	if events := c.fake.Events(lifeOS); len(events) != 1 || later.Google != nil {
		t.Fatalf("exported while off: %+v", events)
	}
	// Switched on again: the upcoming events are exported.
	c.call(http.MethodPatch, "/v1/google/connection", map[string]any{"exportEnabled": true}, http.StatusOK, &connection)
	c.exportDue()
	if events := c.fake.Events(lifeOS); len(events) != 2 {
		t.Fatalf("exported after switching on: %+v", events)
	}
	c.failure(http.MethodPatch, "/v1/google/connection", map[string]any{}, http.StatusBadRequest, "bad_request")
	c.call(http.MethodDelete, "/v1/google/connection", nil, http.StatusNoContent, nil)
	c.failure(http.MethodPatch, "/v1/google/connection", map[string]any{"exportEnabled": true}, http.StatusConflict, "google_not_connected")
}

func TestGooglePriorityRoundTrips(t *testing.T) {
	marked := googleDentist
	marked.ExtendedProperties = &google.ExtendedProperties{Private: map[string]string{"lifeosPriority": "high"}}
	c := newSyncingClient(t, marked, googleTrip)
	c.syncDue()
	// Read from Google's property; absent follows the source's default.
	if dentist := c.syncedEvent("Dentist"); dentist.Priority != generated.High {
		t.Fatalf("dentist priority = %q", dentist.Priority)
	}
	trip := c.syncedEvent("Trip to Hamburg")
	if trip.Priority != generated.Medium {
		t.Fatalf("trip priority = %q", trip.Priority)
	}
	// Written to it; the source's default is written as none.
	c.patchEvent(trip.Id, map[string]any{"priority": "low"})
	if got := private(c.fakeEvent(ownerCalendar.ID, "trip"), "lifeosPriority"); got != "low" {
		t.Fatalf("trip in Google = %q", got)
	}
	if again := c.patchEvent(trip.Id, map[string]any{"priority": "medium"}); again.Priority != generated.Medium {
		t.Fatalf("trip = %q", again.Priority)
	}
	if got := private(c.fakeEvent(ownerCalendar.ID, "trip"), "lifeosPriority"); got != "" {
		t.Fatalf("trip in Google after the default = %q", got)
	}
	// Changed in Google: followed.
	c.fake.ChangeEvent(ownerCalendar.ID, "dentist", func(event *google.Event) {
		event.ExtendedProperties = &google.ExtendedProperties{Private: map[string]string{"lifeosPriority": "low"}}
	})
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	if dentist := c.syncedEvent("Dentist"); dentist.Priority != generated.Low {
		t.Fatalf("dentist priority after Google's change = %q", dentist.Priority)
	}
	var raw json.RawMessage
	c.call(http.MethodGet, "/v1/google/connection", nil, http.StatusOK, &raw)
}
