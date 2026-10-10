package google_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/google"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/testsupport"
)

// The inbound sync: listings (full, then incremental with sync tokens),
// the mapping into Calendar's events through the integration API, and the
// changes Google reports reaching Calendar.
func TestSyncImportsEventsIntoCalendarAndFollowsChanges(t *testing.T) {
	h := syncingHarness(t, googleDentist, googleTrip, googleStandup, googleStandupMoved, googleStandupSkipped, googleClub, googlePast)
	seen := 0
	if connected := newEvents(h, &seen, google.EventConnected); len(connected) != 1 {
		t.Fatalf("connected events = %+v", connected)
	}
	if state := calendarOf(h, testsupport.OwnerCalendar.ID).Sync.State; state != "pending" {
		t.Fatalf("before the first sync: %s", state)
	}

	// The first round reads Calendar's (empty) change feed and lists the
	// owner calendar in full, from a year back; the calendar becomes a
	// Calendar source.
	h.Run()
	requests := h.Fake.Requests(testsupport.OwnerCalendar.ID)
	if len(requests) != 1 || requests[0].Get("timeMin") != "2025-10-10T10:00:00Z" || requests[0].Has("syncToken") {
		t.Fatalf("first listing = %v", requests)
	}
	october := listEvents(h, "2026-10-10T00:00:00Z", "2026-11-01T00:00:00Z")
	want := []string{
		"Standup 10-12T07:00", "Dentist 10-12T07:30", "Book club 10-13T16:00", "Standup (planning) 10-19T08:00",
		"Trip to Hamburg 2026-10-20..2026-10-22", "Standup 10-26T08:00", "Standup 10-28T08:00",
	}
	if got := titles(october); strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Fatalf("October:\n got %q\nwant %q", got, want)
	}
	owner := calendarOf(h, testsupport.OwnerCalendar.ID)
	if owner.Sync.State != "synced" || owner.Sync.SourceID == nil || owner.Sync.LastSyncedAt == nil || !owner.Sync.LastSyncedAt.Equal(testsupport.FixedNow) ||
		owner.Sync.NextSyncAt == nil || !owner.Sync.NextSyncAt.Equal(testsupport.FixedNow.Add(2*time.Minute)) || owner.Sync.LastCounts == nil {
		t.Fatalf("owner sync = %+v", owner.Sync)
	}
	if family := calendarOf(h, testsupport.FamilyCalendar.ID); family.Sync.State != "off" || family.Sync.SourceID != nil {
		t.Fatalf("family sync = %+v", family.Sync)
	}
	// The source as Calendar's integration API has it.
	var source struct {
		ID, Provider, Name  string
		ReadOnly, InProcess bool
	}
	status, raw := h.Calendar.Do(http.MethodGet, "/v1/integrations/google-calendar/sources/owner%40example.com", nil, "dev:test")
	if status != http.StatusOK || jsonInto(raw, &source) != nil || source.ID != *owner.Sync.SourceID || source.Provider != "Google" || source.Name != "Owner" || source.ReadOnly || source.InProcess {
		t.Fatalf("source = %d %s", status, raw)
	}
	dentist := october[1]
	if dentist.Source.Kind != "external" || deref(dentist.Source.Provider) != "Google" || deref(dentist.Source.Name) != "Owner" ||
		deref(dentist.Source.SourceID) != *owner.Sync.SourceID || deref(dentist.Source.ExternalUID) != "dentist" ||
		deref(dentist.Location) != "Praxis Weber" || deref(dentist.TimeZone) != "Europe/Berlin" || !dentist.Editable || dentist.ReadOnlyReason != nil {
		t.Fatalf("dentist = %+v", dentist)
	}
	if remindersOf(dentist.Reminders) != "30" || dentist.UseDefaultReminders || remindersOf(dentist.DefaultReminders) != "10" || remindersOf(dentist.EffectiveReminders) != "30" {
		t.Fatalf("dentist reminders = %+v", dentist.Event)
	}
	standup := october[0]
	if standup.Recurrence == nil || standup.Recurrence.Frequency != "weekly" || standup.Occurrence == nil || !standup.UseDefaultReminders || remindersOf(standup.EffectiveReminders) != "10" {
		t.Fatalf("standup = %+v", standup)
	}
	if moved := october[3]; moved.Occurrence == nil || !moved.Occurrence.Modified || moved.Occurrence.RecurrenceID != "2026-10-19T07:00:00Z" {
		t.Fatalf("moved standup = %+v", moved)
	}
	// An occurrence of the book club (a rule Life OS cannot hold) is
	// read-only, with the reason, and Calendar refuses to change it.
	club := october[2]
	if club.Recurrence != nil || deref(club.Source.ExternalUID) != "club_20261013T160000Z" || club.Editable || club.ReadOnlyReason == nil ||
		!strings.Contains(*club.ReadOnlyReason, "can't edit") {
		t.Fatalf("club = %+v", club)
	}
	if status, raw := h.Calendar.Do(http.MethodPatch, "/v1/events/"+club.ID, map[string]any{"title": "Lesekreis"}, "user"); status != http.StatusConflict {
		t.Fatalf("changing the club = %d %s", status, raw)
	}
	if spring := listEvents(h, "2026-05-01T00:00:00Z", "2026-05-03T00:00:00Z"); len(spring) != 1 || spring[0].Title != "Spring party" {
		t.Fatalf("May = %q", titles(spring))
	}
	// The owner calendar's first sync (and the Life OS calendar's, created
	// for exports) are recorded.
	refreshed := ofSource(newEvents(h, &seen, google.EventSourceRefreshed), *owner.Sync.SourceID)
	if len(refreshed) != 1 || refreshed[0].Actor != activity.System("google-calendar") || refreshed[0].Subject.Type != "calendar_source" ||
		refreshed[0].Subject.ID != *owner.Sync.SourceID || refreshed[0].Data["sourceName"] != "Owner" || refreshed[0].Data["full"] != true ||
		counts(refreshed[0])["inserted"] < 10 {
		t.Fatalf("first sync events = %+v", refreshed)
	}
	if writes := h.Fake.Writes(); len(writes) != 1 || writes[0].Method != http.MethodPost || writes[0].EventID != "" {
		// Only the Life OS calendar is created.
		t.Fatalf("writes to Google = %+v", writes)
	}

	// Not due again yet; then an incremental sync that finds nothing
	// changes nothing and records nothing.
	h.Run()
	if len(h.Fake.Requests(testsupport.OwnerCalendar.ID)) != 1 {
		t.Fatal("synced again before the interval")
	}
	h.Clock.Advance(2 * time.Minute)
	h.Run()
	requests = h.Fake.Requests(testsupport.OwnerCalendar.ID)
	if len(requests) != 2 || requests[1].Get("syncToken") == "" || requests[1].Has("timeMin") {
		t.Fatalf("second listing = %v", requests)
	}
	if fresh := newEvents(h, &seen, google.EventSourceRefreshed); len(fresh) != 0 {
		t.Fatalf("an empty sync recorded %+v", fresh)
	}
	before := listEvents(h, "2026-10-10T00:00:00Z", "2026-11-01T00:00:00Z")
	if strings.Join(titles(before), "; ") != strings.Join(want, "; ") || before[1].UpdatedAt != dentist.UpdatedAt {
		t.Fatalf("after an empty sync: %q", titles(before))
	}

	// A full resync lists everything again and changes nothing in Calendar.
	h.Call(http.MethodPost, "/v1/calendars/"+owner.ID+"/sync?full=true", nil, http.StatusOK, nil)
	if got := titles(listEvents(h, "2026-10-10T00:00:00Z", "2026-11-01T00:00:00Z")); strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Fatalf("after a full resync: %q", got)
	}
	if fresh := ofSource(newEvents(h, &seen, google.EventSourceRefreshed), *owner.Sync.SourceID); len(fresh) != 0 {
		t.Fatalf("a full resync without changes recorded %+v", fresh)
	}

	// Changes in Google: the dentist moves (and gets other reminders), the
	// trip is deleted, a new event appears, a standup is skipped.
	moved := googleDentist
	moved.Start, moved.End = gtimed("2026-10-13T08:00:00+02:00"), gtimed("2026-10-13T08:45:00+02:00")
	moved.Reminders = &google.Reminders{Overrides: []google.Reminder{{Method: "popup", Minutes: 15}}}
	newcomer := google.Event{ID: "concert", Summary: "Concert", Start: gtimed("2026-10-24T20:00:00+02:00"), End: gtimed("2026-10-24T22:00:00+02:00")}
	skip := google.Event{ID: "standup_20261026T080000Z", RecurringEventID: "standup", Status: "cancelled", OriginalStartTime: gtimed("2026-10-26T09:00:00+01:00")}
	h.Fake.PutEvent(testsupport.OwnerCalendar.ID, moved, newcomer, skip)
	h.Fake.DeleteEvent(testsupport.OwnerCalendar.ID, "trip")
	h.Clock.Advance(2 * time.Minute)
	h.Run()
	after := listEvents(h, "2026-10-10T00:00:00Z", "2026-11-01T00:00:00Z", "includeCancelled", "true")
	want = []string{
		"Standup 10-12T07:00", "Dentist 10-13T06:00", "Book club 10-13T16:00", "Standup 10-14T07:00 (cancelled)", "Standup (planning) 10-19T08:00",
		"Trip to Hamburg 2026-10-20..2026-10-22 (cancelled)", "Standup 10-21T07:00 (cancelled)", "Concert 10-24T18:00",
		"Standup 10-26T08:00 (cancelled)", "Standup 10-28T08:00",
	}
	if got := titles(after); strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Fatalf("after changes:\n got %q\nwant %q", got, want)
	}
	if trip := after[5]; trip.RemovedFromSourceAt == nil || trip.CancelledAt == nil {
		t.Fatalf("trip = %+v", trip)
	}
	if movedDentist := after[1]; movedDentist.ID != dentist.ID || remindersOf(movedDentist.Reminders) != "15" {
		t.Fatalf("dentist after the move = %+v", movedDentist)
	}
	refreshed = ofSource(newEvents(h, &seen, google.EventSourceRefreshed), *owner.Sync.SourceID)
	if len(refreshed) != 1 || counts(refreshed[0])["inserted"] != 1 || counts(refreshed[0])["updated"] != 2 || counts(refreshed[0])["removed"] != 1 || refreshed[0].Data["full"] != false {
		t.Fatalf("change counts = %+v", refreshed)
	}

	// The trip comes back in Google: restored.
	h.Fake.PutEvent(testsupport.OwnerCalendar.ID, googleTrip)
	h.Clock.Advance(2 * time.Minute)
	h.Run()
	if trip := listEvents(h, "2026-10-20T00:00:00Z", "2026-10-21T00:00:00Z"); len(trip) != 1 || trip[0].Status != "active" || trip[0].RemovedFromSourceAt != nil {
		t.Fatalf("restored trip = %+v", trip)
	}

	// The series is deleted in Google: removed with its occurrences.
	h.Fake.DeleteEvent(testsupport.OwnerCalendar.ID, "standup")
	h.Clock.Advance(2 * time.Minute)
	h.Run()
	for _, item := range listEvents(h, "2026-10-10T00:00:00Z", "2026-11-01T00:00:00Z") {
		if strings.HasPrefix(item.Title, "Standup") {
			t.Fatalf("a deleted series is still listed: %q", item.Title)
		}
	}
}

// Calendars the account can only read are read-only sources: their events
// carry the reason and allow only reminders and priority.
func TestReadOnlyCalendarsAreReadOnlySources(t *testing.T) {
	h := testsupport.New(t, testsupport.OwnerCalendar, testsupport.HolidayCalendar)
	h.Fake.PutEvent(testsupport.HolidayCalendar.ID, google.Event{ID: "unity", Summary: "Tag der Deutschen Einheit", Start: gday("2026-10-03"), End: gday("2026-10-04")})
	h.Connect()
	var holidays googleCalendar
	holidays = calendarOf(h, testsupport.HolidayCalendar.ID)
	h.Call(http.MethodPatch, "/v1/calendars/"+holidays.ID, map[string]any{"syncEnabled": true}, http.StatusOK, nil)
	h.Run()
	events := listEvents(h, "2026-10-01T00:00:00Z", "2026-10-05T00:00:00Z")
	if len(events) != 1 || events[0].Editable || events[0].Source.ReadOnly == nil || !*events[0].Source.ReadOnly || events[0].ReadOnlyReason == nil ||
		!strings.Contains(*events[0].ReadOnlyReason, "you can only read") {
		t.Fatalf("holiday = %+v", events)
	}
	unity := events[0]
	if status, raw := h.Calendar.Do(http.MethodPatch, "/v1/events/"+unity.ID, map[string]any{"title": "Einheit"}, "user"); status != http.StatusConflict {
		t.Fatalf("changing a read-only event = %d %s", status, raw)
	}
	// Its priority stays changeable, kept in Life OS across syncs.
	h.Calendar.Call(http.MethodPatch, "/v1/events/"+unity.ID, map[string]any{"priority": "high"}, http.StatusOK, nil)
	h.Fake.ChangeEvent(testsupport.HolidayCalendar.ID, "unity", func(event *google.Event) { event.Description = "Feiertag" })
	h.Clock.Advance(2 * time.Minute)
	h.Run()
	if after := listEvents(h, "2026-10-01T00:00:00Z", "2026-10-05T00:00:00Z"); len(after) != 1 || after[0].Priority != "high" || deref(after[0].Description) != "Feiertag" {
		t.Fatalf("after a sync = %+v", after)
	}
	if writes := writesTo(h, testsupport.HolidayCalendar.ID); len(writes) != 0 {
		t.Fatalf("writes to a read-only calendar = %+v", writes)
	}
}

// Switching a calendar off removes its source and events from Calendar;
// disconnecting removes every synced calendar's.
func TestSwitchingOffAndDisconnectingRemoveTheSources(t *testing.T) {
	h := syncingHarness(t, googleDentist)
	family := calendarOf(h, testsupport.FamilyCalendar.ID)
	h.Fake.PutEvent(testsupport.FamilyCalendar.ID, google.Event{ID: "dinner", Summary: "Family dinner", Start: gtimed("2026-10-11T18:00:00+02:00"), End: gtimed("2026-10-11T20:00:00+02:00")})
	h.Call(http.MethodPatch, "/v1/calendars/"+family.ID, map[string]any{"syncEnabled": true}, http.StatusOK, nil)
	h.Run()
	if got := titles(listEvents(h, "2026-10-10T00:00:00Z", "2026-10-20T00:00:00Z")); strings.Join(got, "; ") != "Family dinner 10-11T16:00; Dentist 10-12T07:30" {
		t.Fatalf("synced = %q", got)
	}
	seen := len(h.Recorder.Events())
	h.Call(http.MethodPatch, "/v1/calendars/"+family.ID, map[string]any{"syncEnabled": false}, http.StatusOK, nil)
	if got := titles(listEvents(h, "2026-10-10T00:00:00Z", "2026-10-20T00:00:00Z", "includeCancelled", "true")); strings.Join(got, "; ") != "Dentist 10-12T07:30" {
		t.Fatalf("after switching Family off = %q", got)
	}
	removed := newEvents(h, &seen, google.EventSourceRemoved)
	if len(removed) != 1 || removed[0].Data["reason"] != "sync_disabled" || removed[0].Data["sourceName"] != "Family" || removed[0].Data["removedEvents"] != float64(1) ||
		removed[0].Actor != activity.User() {
		t.Fatalf("removed = %+v", removed)
	}
	h.Call(http.MethodDelete, "/v1/connection", nil, http.StatusNoContent, nil)
	if got := listEvents(h, "2026-10-10T00:00:00Z", "2026-10-20T00:00:00Z", "includeCancelled", "true"); len(got) != 0 {
		t.Fatalf("after disconnecting = %q", titles(got))
	}
	if removed := newEvents(h, &seen, google.EventSourceRemoved); len(removed) != 2 || removed[0].Data["reason"] != "disconnected" {
		// The owner calendar and the Life OS calendar.
		t.Fatalf("removed on disconnect = %+v", removed)
	}
}
