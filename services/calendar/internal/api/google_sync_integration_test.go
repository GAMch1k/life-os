package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/google"
	"github.com/GAMch1k/life-os/services/calendar/internal/google/fakegoogle"
)

// These tests drive Google inbound sync against the fake Google: the
// worker's listings (full, incremental with sync tokens, 410, paging), the
// mapping into Calendar events, and what the API shows of it.

func gtimed(dateTime string) *google.EventTime {
	return &google.EventTime{DateTime: dateTime, TimeZone: "Europe/Berlin"}
}

func gday(date string) *google.EventTime { return &google.EventTime{Date: date} }

var (
	googleDentist = google.Event{
		ID: "dentist", Summary: "Dentist", Location: "Praxis Weber", Description: "Bring the card",
		Start: gtimed("2026-10-12T09:30:00+02:00"), End: gtimed("2026-10-12T10:15:00+02:00"),
		Reminders: &google.Reminders{Overrides: []google.Reminder{{Method: "popup", Minutes: 30}}},
	}
	googleTrip = google.Event{ID: "trip", Summary: "Trip to Hamburg", Start: gday("2026-10-20"), End: gday("2026-10-23"), Reminders: &google.Reminders{UseDefault: true}}
	// Weekly on Monday and Wednesday at 09:00 Berlin until the end of the
	// year, without 14 October.
	googleStandup = google.Event{
		ID: "standup", Summary: "Standup", Start: gtimed("2026-10-12T09:00:00+02:00"), End: gtimed("2026-10-12T09:15:00+02:00"),
		Recurrence: []string{"RRULE:FREQ=WEEKLY;UNTIL=20261231T225959Z;BYDAY=MO,WE", "EXDATE;TZID=Europe/Berlin:20261014T090000"},
		Reminders:  &google.Reminders{UseDefault: true},
	}
	googleStandupMoved = google.Event{
		ID: "standup_20261019T070000Z", RecurringEventID: "standup", OriginalStartTime: gtimed("2026-10-19T09:00:00+02:00"),
		Summary: "Standup (planning)", Start: gtimed("2026-10-19T10:00:00+02:00"), End: gtimed("2026-10-19T11:00:00+02:00"), Reminders: &google.Reminders{UseDefault: true},
	}
	googleStandupSkipped = google.Event{ID: "standup_20261021T070000Z", RecurringEventID: "standup", Status: "cancelled", OriginalStartTime: gtimed("2026-10-21T09:00:00+02:00")}
	// Monthly on the second Tuesday: outside Calendar's model, imported as
	// single occurrences.
	googleClub = google.Event{
		ID: "club", Summary: "Book club", Start: gtimed("2026-10-13T18:00:00+02:00"), End: gtimed("2026-10-13T20:00:00+02:00"),
		Recurrence: []string{"RRULE:FREQ=MONTHLY;BYDAY=2TU"}, Reminders: &google.Reminders{UseDefault: true},
	}
	googlePast = google.Event{ID: "past", Summary: "Spring party", Start: gtimed("2026-05-02T19:00:00+02:00"), End: gtimed("2026-05-02T23:00:00+02:00")}
)

// newSyncingClient is a connected Google with the owner's calendar holding
// events (its default reminder: 10 minutes).
func newSyncingClient(t *testing.T, events ...google.Event) *googleClient {
	t.Helper()
	c := newGoogleClient(t, ownerCalendar, familyCalendar)
	c.fake.SetCalendarDetails(ownerCalendar.ID, "Europe/Berlin", google.Reminder{Method: "popup", Minutes: 10})
	c.fake.PutEvent(ownerCalendar.ID, events...)
	c.connect()
	return c
}

func (c *googleClient) syncDue() {
	c.t.Helper()
	c.syncer.SyncDue(context.Background())
}

// googleEvents lists the synced events overlapping [from, to).
func (c *googleClient) googleEvents(from, to string, extra ...string) []generated.CalendarEvent {
	c.t.Helper()
	query := url.Values{"from": {from}, "to": {to}, "sourceKind": {"google"}, "limit": {"100"}}
	for index := 0; index+1 < len(extra); index += 2 {
		query.Set(extra[index], extra[index+1])
	}
	var out generated.EventList
	c.call(http.MethodGet, "/v1/events?"+query.Encode(), nil, http.StatusOK, &out)
	return out.Items
}

// titles are the events' titles with their start (date or time in UTC).
func titles(items []generated.CalendarEvent) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		when := ""
		if item.AllDay {
			when = item.StartDate.Format(time.DateOnly) + ".." + item.EndDate.Format(time.DateOnly)
		} else {
			when = item.StartAt.UTC().Format("01-02T15:04")
		}
		mark := ""
		if item.Status == generated.Cancelled {
			mark = " (cancelled)"
		}
		out = append(out, item.Title+" "+when+mark)
	}
	return out
}

func (c *googleClient) googleCalendar(id string) generated.GoogleCalendar {
	c.t.Helper()
	for _, calendar := range c.calendars() {
		if calendar.GoogleCalendarId == id {
			return calendar
		}
	}
	c.t.Fatalf("calendar %s not listed", id)
	return generated.GoogleCalendar{}
}

func (c *googleClient) countSourceEvents(sourceID googleuuid.UUID) int {
	c.t.Helper()
	var n int
	if err := c.pool.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE source_id = $1`, sourceID).Scan(&n); err != nil {
		c.t.Fatal(err)
	}
	return n
}

func syncCounts(event activity.Event) map[string]float64 {
	raw, _ := json.Marshal(event.Data["counts"])
	var out map[string]float64
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestGoogleSyncImportsEventsAndFollowsChanges(t *testing.T) {
	c := newSyncingClient(t, googleDentist, googleTrip, googleStandup, googleStandupMoved, googleStandupSkipped, googleClub, googlePast)
	seen := 0
	expectRecorded(t, c.recorder, &seen, google.EventConnected)
	if state := c.googleCalendar(ownerCalendar.ID).Sync.State; state != generated.GoogleCalendarSyncStatePending {
		t.Fatalf("before the first sync: %s", state)
	}

	// The first sync lists the calendar in full, from a year back.
	c.syncDue()
	requests := c.fake.Requests(ownerCalendar.ID)
	if len(requests) != 1 || requests[0].Get("timeMin") != "2025-10-10T10:00:00Z" || requests[0].Has("syncToken") ||
		requests[0].Get("singleEvents") != "false" || requests[0].Get("showDeleted") != "true" {
		t.Fatalf("first listing = %v", requests)
	}
	october := c.googleEvents("2026-10-10T00:00:00Z", "2026-11-01T00:00:00Z")
	want := []string{
		"Standup 10-12T07:00", "Dentist 10-12T07:30", "Book club 10-13T16:00", "Standup (planning) 10-19T08:00",
		"Trip to Hamburg 2026-10-20..2026-10-22", "Standup 10-26T08:00", "Standup 10-28T08:00",
	}
	if got := titles(october); strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Fatalf("October:\n got %q\nwant %q", got, want)
	}
	owner := c.googleCalendar(ownerCalendar.ID)
	if owner.Sync.State != generated.GoogleCalendarSyncStateSynced || owner.Sync.SourceId == nil || owner.Sync.LastSyncedAt == nil || !owner.Sync.LastSyncedAt.Equal(fixedNow) ||
		owner.Sync.LastError != nil || owner.Sync.LastCounts == nil || owner.Sync.NextSyncAt == nil || !owner.Sync.NextSyncAt.Equal(fixedNow.Add(2*time.Minute)) {
		t.Fatalf("owner sync = %+v", owner.Sync)
	}
	if family := c.googleCalendar(familyCalendar.ID); family.Sync.State != generated.GoogleCalendarSyncStateOff || family.Sync.SourceId != nil {
		t.Fatalf("family sync = %+v", family.Sync)
	}
	dentist := october[1]
	if dentist.Source.Kind != generated.EventSourceKindGoogle || *dentist.Source.Name != "Owner" || *dentist.Source.SourceId != *owner.Sync.SourceId ||
		*dentist.Source.ExternalUid != "dentist" || *dentist.Location != "Praxis Weber" || *dentist.TimeZone != "Europe/Berlin" {
		t.Fatalf("dentist = %+v", dentist)
	}
	if remindersOf(dentist.Reminders) != "30" || dentist.UseDefaultReminders || remindersOf(dentist.DefaultReminders) != "10" || remindersOf(dentist.EffectiveReminders) != "30" {
		t.Fatalf("dentist reminders = %+v", dentist)
	}
	standup := october[0]
	if standup.Recurrence == nil || standup.Recurrence.Frequency != generated.Weekly || standup.Occurrence == nil || !standup.UseDefaultReminders ||
		remindersOf(standup.EffectiveReminders) != "10" {
		t.Fatalf("standup = %+v", standup)
	}
	if moved := october[3]; moved.Occurrence == nil || !moved.Occurrence.Modified || moved.Occurrence.RecurrenceId != "2026-10-19T07:00:00Z" {
		t.Fatalf("moved standup = %+v", moved)
	}
	if club := october[2]; club.Recurrence != nil || *club.Source.ExternalUid != "club_20261013T160000Z" {
		t.Fatalf("club = %+v", club)
	}
	// The past event inside the window is imported too.
	if spring := c.googleEvents("2026-05-01T00:00:00Z", "2026-05-03T00:00:00Z"); len(spring) != 1 || spring[0].Title != "Spring party" {
		t.Fatalf("May = %q", titles(spring))
	}
	event := expectRecorded(t, c.recorder, &seen, google.EventSourceRefreshed)[0]
	if event.Actor != activity.System("google-calendar") || event.Subject.Type != "calendar_source" || event.Subject.ID != owner.Sync.SourceId.String() ||
		event.Data["sourceName"] != "Owner" || event.Data["trigger"] != "scheduled" || event.Data["full"] != true ||
		!strings.HasPrefix(event.Summary, "Synced Owner from Google: ") || syncCounts(event)["inserted"] < 10 {
		t.Fatalf("first sync event = %+v", event)
	}

	// Not due again yet; then an incremental sync that finds nothing
	// changes nothing and records nothing.
	c.syncDue()
	if len(c.fake.Requests(ownerCalendar.ID)) != 1 {
		t.Fatal("synced again before the interval")
	}
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	requests = c.fake.Requests(ownerCalendar.ID)
	if len(requests) != 2 || requests[1].Get("syncToken") == "" || requests[1].Has("timeMin") {
		t.Fatalf("second listing = %v", requests)
	}
	expectRecorded(t, c.recorder, &seen)
	before := c.googleEvents("2026-10-10T00:00:00Z", "2026-11-01T00:00:00Z")
	if strings.Join(titles(before), "; ") != strings.Join(want, "; ") || before[1].UpdatedAt != dentist.UpdatedAt {
		t.Fatalf("after an empty sync: %q", titles(before))
	}

	// Single events and series of a calendar the account may edit are
	// editable (written to Google, see google_twoway_integration_test.go);
	// an occurrence of the book club (a rule Life OS cannot hold) is
	// read-only, with the reason, and changing or deleting it is refused.
	if !dentist.Editable || dentist.ReadOnlyReason != nil || dentist.Google == nil || dentist.Google.CalendarName != "Owner" || dentist.Google.State != generated.EventGoogleLinkStateSynced {
		t.Fatalf("dentist = %+v", dentist)
	}
	var club generated.CalendarEvent
	for _, item := range before {
		if item.Title == "Book club" {
			club = item
		}
	}
	if club.Editable || club.ReadOnlyReason == nil || !strings.Contains(*club.ReadOnlyReason, "can't edit") {
		t.Fatalf("club = %+v", club)
	}
	c.failure(http.MethodPatch, "/v1/events/"+club.Id.String(), map[string]any{"title": "Lesekreis"}, http.StatusConflict, "google_read_only")
	c.failure(http.MethodDelete, "/v1/events/"+club.Id.String(), nil, http.StatusConflict, "google_read_only")
	if writes := c.fake.Writes(); len(writes) != 0 {
		t.Fatalf("writes to Google = %+v", writes)
	}
	// Synced events are found by search like any other.
	var found generated.EventList
	c.call(http.MethodGet, "/v1/events/search?q=praxis", nil, http.StatusOK, &found)
	if len(found.Items) != 1 || found.Items[0].Id != dentist.Id {
		t.Fatalf("search = %q", titles(found.Items))
	}

	// Changes in Google: the dentist moves (and gets other reminders), the
	// trip is deleted, a new event appears, a standup is skipped.
	moved := googleDentist
	moved.Start, moved.End = gtimed("2026-10-13T08:00:00+02:00"), gtimed("2026-10-13T08:45:00+02:00")
	moved.Reminders = &google.Reminders{Overrides: []google.Reminder{{Method: "popup", Minutes: 15}}}
	newcomer := google.Event{ID: "concert", Summary: "Concert", Start: gtimed("2026-10-24T20:00:00+02:00"), End: gtimed("2026-10-24T22:00:00+02:00")}
	skip := google.Event{ID: "standup_20261026T080000Z", RecurringEventID: "standup", Status: "cancelled", OriginalStartTime: gtimed("2026-10-26T09:00:00+01:00")}
	c.fake.PutEvent(ownerCalendar.ID, moved, newcomer, skip)
	c.fake.DeleteEvent(ownerCalendar.ID, "trip")
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	after := c.googleEvents("2026-10-10T00:00:00Z", "2026-11-01T00:00:00Z", "includeCancelled", "true")
	want = []string{
		"Standup 10-12T07:00", "Dentist 10-13T06:00", "Book club 10-13T16:00", "Standup 10-14T07:00 (cancelled)", "Standup (planning) 10-19T08:00",
		"Trip to Hamburg 2026-10-20..2026-10-22 (cancelled)", "Standup 10-21T07:00 (cancelled)", "Concert 10-24T18:00",
		"Standup 10-26T08:00 (cancelled)", "Standup 10-28T08:00",
	}
	if got := titles(after); strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Fatalf("after changes:\n got %q\nwant %q", got, want)
	}
	// The trip left Google: cancelled and marked removed, kept as history.
	if trip := after[5]; trip.RemovedFromSourceAt == nil || trip.CancelledAt == nil {
		t.Fatalf("trip = %+v", trip)
	}
	// The event follows Google's new reminders.
	if movedDentist := after[1]; movedDentist.Id != dentist.Id || remindersOf(movedDentist.Reminders) != "15" {
		t.Fatalf("dentist after the move = %+v", movedDentist)
	}
	event = expectRecorded(t, c.recorder, &seen, google.EventSourceRefreshed)[0]
	if got := syncCounts(event); got["inserted"] != 1 || got["updated"] != 2 || got["removed"] != 1 || got["restored"] != 0 || event.Data["full"] != false {
		t.Fatalf("change counts = %v", got)
	}

	// The trip comes back in Google: restored.
	c.fake.PutEvent(ownerCalendar.ID, googleTrip)
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	if trip := c.googleEvents("2026-10-20T00:00:00Z", "2026-10-21T00:00:00Z"); len(trip) != 1 || trip[0].Status != generated.Active || trip[0].RemovedFromSourceAt != nil {
		t.Fatalf("restored trip = %+v", trip)
	}
	if got := syncCounts(expectRecorded(t, c.recorder, &seen, google.EventSourceRefreshed)[0]); got["restored"] != 1 {
		t.Fatalf("restore counts = %v", got)
	}

	// The series is deleted in Google: removed with its occurrences.
	c.fake.DeleteEvent(ownerCalendar.ID, "standup")
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	for _, item := range c.googleEvents("2026-10-10T00:00:00Z", "2026-11-01T00:00:00Z") {
		if strings.HasPrefix(item.Title, "Standup") {
			t.Fatalf("a deleted series is still listed: %q", item.Title)
		}
	}
	expectRecorded(t, c.recorder, &seen, google.EventSourceRefreshed)

	// Feeds stay apart: synced calendars are not listed or editable as
	// feeds.
	var feeds generated.SourceList
	c.call(http.MethodGet, "/v1/sources", nil, http.StatusOK, &feeds)
	if len(feeds.Items) != 0 {
		t.Fatalf("feeds = %+v", feeds.Items)
	}
	sourceID := owner.Sync.SourceId.String()
	c.failure(http.MethodGet, "/v1/sources/"+sourceID, nil, http.StatusNotFound, "not_found")
	c.failure(http.MethodPatch, "/v1/sources/"+sourceID, map[string]any{"name": "X"}, http.StatusNotFound, "not_found")
	c.failure(http.MethodPost, "/v1/sources/"+sourceID+"/refresh", nil, http.StatusNotFound, "not_found")
	c.failure(http.MethodDelete, "/v1/sources/"+sourceID, nil, http.StatusNotFound, "not_found")
}

func remindersOf(list []generated.Reminder) string {
	var out []string
	for _, reminder := range list {
		if reminder.MinutesBefore != nil {
			out = append(out, fmt.Sprint(*reminder.MinutesBefore))
		}
	}
	return strings.Join(out, ",")
}

func TestGoogleSyncPagesAndListsInFullAfterAnExpiredToken(t *testing.T) {
	c := newSyncingClient(t, googleDentist, googleTrip, googleStandup, googleStandupMoved, googleClub, googlePast)
	c.fake.Lock()
	c.fake.EventsPageSize = 2
	c.fake.Unlock()
	seen := 0
	c.syncDue()
	if requests := c.fake.Requests(ownerCalendar.ID); len(requests) != 3 || requests[1].Get("pageToken") != "events-2" || requests[2].Get("timeMin") == "" {
		t.Fatalf("paged listing = %v", requests)
	}
	first := c.googleEvents("2026-05-01T00:00:00Z", "2027-01-01T00:00:00Z", "includeCancelled", "true")
	expectRecorded(t, c.recorder, &seen, google.EventConnected, google.EventSourceRefreshed)
	storedEvents := func() int {
		var n int
		if err := c.pool.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE source_kind = 'google'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	stored := storedEvents()

	// Google expires the sync token while an event is deleted and gone
	// for good: the 410 brings a full listing, which removes it and
	// duplicates nothing.
	c.fake.ExpireSyncTokens(ownerCalendar.ID)
	c.fake.ForgetEvent(ownerCalendar.ID, "trip")
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	requests := c.fake.Requests(ownerCalendar.ID)[3:]
	if len(requests) != 4 || requests[0].Get("syncToken") == "" || requests[1].Get("timeMin") != "2025-10-10T10:02:00Z" || requests[1].Has("syncToken") {
		t.Fatalf("listings after the 410 = %v", requests)
	}
	second := c.googleEvents("2026-05-01T00:00:00Z", "2027-01-01T00:00:00Z", "includeCancelled", "true")
	if len(second) != len(first) {
		t.Fatalf("after the full listing: %q, before %q", titles(second), titles(first))
	}
	ids := map[string]bool{}
	for _, item := range first {
		ids[item.Id.String()+"@"+occurrenceID(item)] = true
	}
	for _, item := range second {
		if !ids[item.Id.String()+"@"+occurrenceID(item)] {
			t.Fatalf("a new id after the full listing: %+v", item)
		}
		if item.Title == "Trip to Hamburg" && item.RemovedFromSourceAt == nil {
			t.Fatalf("the forgotten trip is still active: %+v", item)
		}
	}
	if got := syncCounts(expectRecorded(t, c.recorder, &seen, google.EventSourceRefreshed)[0]); got["removed"] != 1 || got["inserted"] != 0 || got["updated"] != 0 {
		t.Fatalf("full listing counts = %v", got)
	}
	if again := storedEvents(); again != stored {
		t.Fatalf("stored events = %d, before %d", again, stored)
	}
}

func occurrenceID(item generated.CalendarEvent) string {
	if item.Occurrence == nil {
		return ""
	}
	return item.Occurrence.RecurrenceId
}

func TestGoogleSyncFailuresKeepEventsAndBackOff(t *testing.T) {
	c := newSyncingClient(t, googleDentist, googleTrip)
	seen := 0
	c.syncDue()
	expectRecorded(t, c.recorder, &seen, google.EventConnected, google.EventSourceRefreshed)
	synced := c.googleEvents("2026-10-10T00:00:00Z", "2026-11-01T00:00:00Z")

	// Google fails: the events stay, the failure is recorded once, and the
	// calendar is retried after 2, then 4 minutes.
	c.fake.Lock()
	c.fake.EventsStatus = http.StatusInternalServerError
	c.fake.Unlock()
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	owner := c.googleCalendar(ownerCalendar.ID)
	if owner.Sync.State != generated.GoogleCalendarSyncStateFailing || owner.Sync.ConsecutiveFailures != 1 || owner.Sync.LastError == nil ||
		!strings.Contains(*owner.Sync.LastError, "Google is unavailable") || !owner.Sync.NextSyncAt.Equal(fixedNow.Add(4*time.Minute)) ||
		!owner.Sync.LastSyncedAt.Equal(fixedNow) {
		t.Fatalf("after a failure: %+v", owner.Sync)
	}
	event := expectRecorded(t, c.recorder, &seen, google.EventSourceRefreshFailed)[0]
	if event.Actor != activity.System("google-calendar") || event.Outcome != activity.Failed || !strings.HasPrefix(event.Summary, "Syncing Owner from Google failed: ") {
		t.Fatalf("failure event = %+v", event)
	}
	if after := c.googleEvents("2026-10-10T00:00:00Z", "2026-11-01T00:00:00Z"); strings.Join(titles(after), ";") != strings.Join(titles(synced), ";") {
		t.Fatalf("events after a failure: %q", titles(after))
	}
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	if owner = c.googleCalendar(ownerCalendar.ID); owner.Sync.ConsecutiveFailures != 2 || !owner.Sync.NextSyncAt.Equal(fixedNow.Add(8*time.Minute)) {
		t.Fatalf("after a second failure: %+v", owner.Sync)
	}
	expectRecorded(t, c.recorder, &seen) // the same failure again records nothing

	// Google recovers: the next sync succeeds and says so once.
	c.fake.Lock()
	c.fake.EventsStatus = 0
	c.fake.Unlock()
	c.clock.Advance(4 * time.Minute)
	c.syncDue()
	if owner = c.googleCalendar(ownerCalendar.ID); owner.Sync.State != generated.GoogleCalendarSyncStateSynced || owner.Sync.ConsecutiveFailures != 0 || owner.Sync.LastError != nil {
		t.Fatalf("after recovering: %+v", owner.Sync)
	}
	expectRecorded(t, c.recorder, &seen, google.EventSourceRefreshed)

	// The owner revokes Life OS at Google: the next token refresh fails,
	// the connection expires (recorded once), the events stay, and the
	// syncer stops asking Google.
	c.fake.Lock()
	c.fake.RefreshError = "invalid_grant"
	c.fake.Unlock()
	c.clock.Advance(2 * time.Hour)
	c.syncDue()
	if connection := c.connection(); connection.Status != "expired" {
		t.Fatalf("connection = %+v", connection)
	}
	if owner = c.googleCalendar(ownerCalendar.ID); owner.Sync.State != generated.GoogleCalendarSyncStateFailing || !strings.Contains(*owner.Sync.LastError, "reconnect Google") {
		t.Fatalf("after expiry: %+v", owner.Sync)
	}
	expectRecorded(t, c.recorder, &seen, google.EventExpired)
	if after := c.googleEvents("2026-10-10T00:00:00Z", "2026-11-01T00:00:00Z"); len(after) != len(synced) {
		t.Fatalf("events after expiry: %q", titles(after))
	}
	requests := len(c.fake.Requests(ownerCalendar.ID))
	_, refreshes, _ := c.fake.Counts()
	c.clock.Advance(2 * time.Hour)
	c.syncDue()
	if _, again, _ := c.fake.Counts(); len(c.fake.Requests(ownerCalendar.ID)) != requests || again != refreshes {
		t.Fatal("the syncer kept asking Google with an expired connection")
	}

	// Reconnecting resumes syncing right away, with the stored token.
	c.fake.Lock()
	c.fake.RefreshError = ""
	c.fake.Unlock()
	c.connect()
	c.syncDue()
	last := c.fake.Requests(ownerCalendar.ID)
	if len(last) != requests+1 || last[len(last)-1].Get("syncToken") == "" {
		t.Fatalf("after reconnecting: %v", last[requests:])
	}
	if owner = c.googleCalendar(ownerCalendar.ID); owner.Sync.State != generated.GoogleCalendarSyncStateSynced {
		t.Fatalf("after reconnecting: %+v", owner.Sync)
	}
}

func TestGoogleSyncSwitchedOffOrDisconnectedRemovesEvents(t *testing.T) {
	c := newSyncingClient(t, googleDentist, googleStandup)
	c.fake.PutEvent(familyCalendar.ID, google.Event{ID: "dinner", Summary: "Family dinner", Start: gtimed("2026-10-18T18:00:00+02:00"), End: gtimed("2026-10-18T20:00:00+02:00")},
		google.Event{ID: "picnic", Summary: "Picnic", Start: gday("2026-10-17"), End: gday("2026-10-18")})
	seen := 0
	c.syncDue()
	expectRecorded(t, c.recorder, &seen, google.EventConnected, google.EventSourceRefreshed)

	// The owner ticks the family calendar: it syncs at once.
	family := c.googleCalendar(familyCalendar.ID)
	var chosen generated.GoogleCalendar
	c.call(http.MethodPatch, "/v1/google/calendars/"+family.Id.String(), map[string]any{"syncEnabled": true}, http.StatusOK, &chosen)
	if chosen.Sync.State != generated.GoogleCalendarSyncStatePending || !chosen.Sync.NextSyncAt.Equal(fixedNow) {
		t.Fatalf("chosen = %+v", chosen.Sync)
	}
	c.syncDue()
	family = c.googleCalendar(familyCalendar.ID)
	if family.Sync.State != generated.GoogleCalendarSyncStateSynced || family.Sync.SourceId == nil || c.countSourceEvents(*family.Sync.SourceId) != 2 {
		t.Fatalf("family = %+v", family.Sync)
	}
	if dinner := c.googleEvents("2026-10-18T00:00:00Z", "2026-10-19T00:00:00Z"); len(dinner) != 1 || *dinner[0].Source.Name != "Family" {
		t.Fatalf("dinner = %+v", dinner)
	}
	expectRecorded(t, c.recorder, &seen, google.EventSourceRefreshed)

	// Unticking it removes its events from Life OS (they stay in Google).
	c.call(http.MethodPatch, "/v1/google/calendars/"+family.Id.String(), map[string]any{"syncEnabled": false}, http.StatusOK, &chosen)
	if chosen.Sync.State != generated.GoogleCalendarSyncStateOff || chosen.Sync.SourceId != nil || chosen.Sync.LastSyncedAt != nil || c.countSourceEvents(*family.Sync.SourceId) != 0 {
		t.Fatalf("unticked = %+v", chosen.Sync)
	}
	removed := expectRecorded(t, c.recorder, &seen, google.EventSourceRemoved)[0]
	if removed.Actor != activity.User() || removed.Data["reason"] != "sync_disabled" || removed.Data["removedEvents"] != float64(2) ||
		removed.Summary != "Family no longer syncs from Google: 2 events removed from Life OS" || removed.Subject.ID != family.Sync.SourceId.String() {
		t.Fatalf("removed = %+v", removed)
	}
	if dinner := c.googleEvents("2026-10-18T00:00:00Z", "2026-10-19T00:00:00Z", "includeCancelled", "true"); len(dinner) != 0 {
		t.Fatalf("family events after unticking: %q", titles(dinner))
	}
	var stored int
	if err := c.pool.QueryRow(context.Background(), `SELECT count(*) FROM google_events WHERE calendar_id = $1`, family.Id).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("stored Google events = %d, %v", stored, err)
	}
	// Ticking it again lists it in full again.
	c.call(http.MethodPatch, "/v1/google/calendars/"+family.Id.String(), map[string]any{"syncEnabled": true}, http.StatusOK, nil)
	c.syncDue()
	if requests := c.fake.Requests(familyCalendar.ID); len(requests) != 2 || requests[1].Has("syncToken") || requests[1].Get("timeMin") == "" {
		t.Fatalf("family listings = %v", requests)
	}
	expectRecorded(t, c.recorder, &seen, google.EventSourceRefreshed)

	// Disconnecting removes every synced event.
	c.call(http.MethodDelete, "/v1/google/connection", nil, http.StatusNoContent, nil)
	events := expectRecorded(t, c.recorder, &seen, google.EventDisconnected, google.EventSourceRemoved, google.EventSourceRemoved)
	reasons := []string{events[1].Summary, events[2].Summary}
	sort.Strings(reasons)
	if strings.Join(reasons, " | ") != "Google Calendar disconnected: Family's 2 events removed from Life OS | Google Calendar disconnected: Owner's 2 events removed from Life OS" {
		t.Fatalf("disconnect removals = %q", reasons)
	}
	var left int
	if err := c.pool.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE source_kind = 'google'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("synced events after disconnecting = %d, %v", left, err)
	}
	var sources int
	if err := c.pool.QueryRow(context.Background(), `SELECT count(*) FROM calendar_sources`).Scan(&sources); err != nil || sources != 0 {
		t.Fatalf("sources after disconnecting = %d, %v", sources, err)
	}
}

func TestGoogleSyncResumesAfterRestartAndRunsOncePerCalendar(t *testing.T) {
	c := newSyncingClient(t, googleDentist, googleTrip)
	c.syncDue()
	owner := c.googleCalendar(ownerCalendar.ID)

	// A restart resumes with the stored sync token.
	c.restart(googleConfig())
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	requests := c.fake.Requests(ownerCalendar.ID)
	if len(requests) != 2 || requests[1].Get("syncToken") == "" {
		t.Fatalf("after a restart: %v", requests)
	}

	// "Sync now", and a full resync, through the API.
	var synced generated.GoogleCalendar
	c.call(http.MethodPost, "/v1/google/calendars/"+owner.Id.String()+"/sync", nil, http.StatusOK, &synced)
	c.call(http.MethodPost, "/v1/google/calendars/"+owner.Id.String()+"/sync?full=true", nil, http.StatusOK, &synced)
	requests = c.fake.Requests(ownerCalendar.ID)
	if len(requests) != 4 || requests[2].Get("syncToken") == "" || requests[3].Has("syncToken") || synced.Sync.State != generated.GoogleCalendarSyncStateSynced {
		t.Fatalf("manual syncs = %v, %+v", requests[2:], synced.Sync)
	}
	family := c.googleCalendar(familyCalendar.ID)
	c.failure(http.MethodPost, "/v1/google/calendars/"+family.Id.String()+"/sync", nil, http.StatusConflict, "google_sync_disabled")
	c.failure(http.MethodPost, "/v1/google/calendars/"+googleuuid.NewString()+"/sync", nil, http.StatusNotFound, "not_found")

	// A sync running elsewhere (its lease) keeps others out; within the
	// process concurrent syncs of a calendar share one listing.
	if _, err := c.pool.Exec(context.Background(), `UPDATE google_calendars SET syncing_until = now() + interval '1 hour' WHERE id = $1`, owner.Id); err != nil {
		t.Fatal(err)
	}
	c.failure(http.MethodPost, "/v1/google/calendars/"+owner.Id.String()+"/sync", nil, http.StatusConflict, "sync_in_progress")
	other := google.NewSyncer(c.service, time.UTC, time.Minute, nil)
	if _, err := other.Sync(context.Background(), googleuuid.UUID(owner.Id), google.SyncTrigger{}); !errors.Is(err, google.ErrSyncInProgress) {
		t.Fatalf("a second process: %v", err)
	}
	if _, err := c.pool.Exec(context.Background(), `UPDATE google_calendars SET syncing_until = NULL WHERE id = $1`, owner.Id); err != nil {
		t.Fatal(err)
	}
	before := len(c.fake.Requests(ownerCalendar.ID))
	var group sync.WaitGroup
	results := make([]error, 4)
	for index := range results {
		group.Add(1)
		go func() {
			defer group.Done()
			_, results[index] = c.syncer.Sync(context.Background(), googleuuid.UUID(owner.Id), google.SyncTrigger{Manual: true})
		}()
	}
	group.Wait()
	listings := len(c.fake.Requests(ownerCalendar.ID)) - before
	for _, err := range results {
		if err != nil && !errors.Is(err, google.ErrSyncInProgress) {
			t.Fatalf("concurrent sync: %v", err)
		}
	}
	if listings < 1 || listings > 2 {
		t.Fatalf("%d listings for concurrent syncs", listings)
	}
	var stored int
	if err := c.pool.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE source_kind = 'google'`).Scan(&stored); err != nil || stored != 2 {
		t.Fatalf("stored events = %d, %v", stored, err)
	}
}

func TestGoogleSyncCalendarNoLongerListedRemovesItsEvents(t *testing.T) {
	c := newSyncingClient(t, googleDentist)
	c.fake.PutEvent(familyCalendar.ID, google.Event{ID: "dinner", Summary: "Family dinner", Start: gtimed("2026-10-18T18:00:00+02:00"), End: gtimed("2026-10-18T20:00:00+02:00")})
	family := c.googleCalendar(familyCalendar.ID)
	c.call(http.MethodPatch, "/v1/google/calendars/"+family.Id.String(), map[string]any{"syncEnabled": true}, http.StatusOK, nil)
	c.syncDue()
	seen := len(c.recorder.all())

	// A list without the primary calendar is a fault at Google: refused,
	// nothing removed.
	c.fake.Lock()
	c.fake.Calendars = []fakegoogle.Calendar{familyCalendar}
	c.fake.Unlock()
	c.failure(http.MethodPost, "/v1/google/calendars/refresh", nil, http.StatusBadGateway, "google_rejected")
	if events := c.googleEvents("2026-10-10T00:00:00Z", "2026-11-01T00:00:00Z"); len(events) != 2 {
		t.Fatalf("after a list without the primary: %q", titles(events))
	}

	// The family calendar is gone from Google: dropped with its events.
	c.fake.Lock()
	c.fake.Calendars = []fakegoogle.Calendar{ownerCalendar}
	c.fake.Unlock()
	c.call(http.MethodPost, "/v1/google/calendars/refresh", nil, http.StatusOK, nil)
	removed := expectRecorded(t, c.recorder, &seen, google.EventSourceRemoved)[0]
	if removed.Data["reason"] != "calendar_unlisted" || removed.Summary != "Google no longer lists Family: its 1 event removed from Life OS" {
		t.Fatalf("removed = %+v", removed)
	}
	if events := c.googleEvents("2026-10-10T00:00:00Z", "2026-11-01T00:00:00Z", "includeCancelled", "true"); len(events) != 1 || events[0].Title != "Dentist" {
		t.Fatalf("after the family calendar left: %q", titles(events))
	}
}

func TestGoogleSyncThatCannotBeSavedBacksOff(t *testing.T) {
	c := newSyncingClient(t, googleDentist)
	c.syncDue()
	seen := len(c.recorder.all())
	// A stored Google event Calendar cannot read: every derivation fails.
	if _, err := c.pool.Exec(context.Background(), `INSERT INTO google_events (calendar_id, event_id, data) SELECT id, 'broken', '{"id":"broken","start":"x"}' FROM google_calendars WHERE google_calendar_id = $1`, ownerCalendar.ID); err != nil {
		t.Fatal(err)
	}
	c.fake.PutEvent(ownerCalendar.ID, googleTrip)
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	owner := c.googleCalendar(ownerCalendar.ID)
	if owner.Sync.State != generated.GoogleCalendarSyncStateFailing || owner.Sync.LastError == nil || *owner.Sync.LastError != "the events read from Google could not be saved in Calendar" ||
		!owner.Sync.NextSyncAt.Equal(fixedNow.Add(4*time.Minute)) {
		t.Fatalf("after a failed save: %+v", owner.Sync)
	}
	expectRecorded(t, c.recorder, &seen, google.EventSourceRefreshFailed)
	// Not retried before the backoff; the same changes are listed again
	// once the fault is gone.
	requests := len(c.fake.Requests(ownerCalendar.ID))
	c.syncDue()
	if len(c.fake.Requests(ownerCalendar.ID)) != requests {
		t.Fatal("retried before the backoff")
	}
	if _, err := c.pool.Exec(context.Background(), `DELETE FROM google_events WHERE event_id = 'broken'`); err != nil {
		t.Fatal(err)
	}
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	if trip := c.googleEvents("2026-10-20T00:00:00Z", "2026-10-21T00:00:00Z"); len(trip) != 1 {
		t.Fatalf("after recovering: %q", titles(trip))
	}
	expectRecorded(t, c.recorder, &seen, google.EventSourceRefreshed)
}
