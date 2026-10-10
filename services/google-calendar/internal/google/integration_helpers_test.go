package google_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/calendar"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/google"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/google/fakegoogle"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/testsupport"
)

// These tests drive the integration against the fake Google and the real
// Calendar service (see testsupport), on GOOGLE_CALENDAR_TEST_DATABASE_URL.

func TestMain(m *testing.M) {
	code := m.Run()
	testsupport.Cleanup()
	os.Exit(code)
}

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

// calendarEvent is an event as Calendar lists it.
type calendarEvent struct {
	calendar.Event
	Occurrence *struct {
		SeriesID     string `json:"seriesId"`
		RecurrenceID string `json:"recurrenceId"`
		Modified     bool   `json:"modified"`
	} `json:"occurrence"`
	CancelledAt *time.Time `json:"cancelledAt"`
}

// harness is a connected fake account whose owner calendar holds events
// (its default reminder: 10 minutes).
func syncingHarness(t *testing.T, events ...google.Event) *testsupport.Harness {
	t.Helper()
	h := testsupport.New(t, testsupport.OwnerCalendar, testsupport.FamilyCalendar)
	h.Fake.SetCalendarDetails(testsupport.OwnerCalendar.ID, "Europe/Berlin", google.Reminder{Method: "popup", Minutes: 10})
	h.Fake.PutEvent(testsupport.OwnerCalendar.ID, events...)
	h.Connect()
	return h
}

// listEvents lists Calendar's events overlapping [from, to).
func listEvents(h *testsupport.Harness, from, to string, extra ...string) []calendarEvent {
	h.T.Helper()
	query := url.Values{"from": {from}, "to": {to}, "limit": {"100"}}
	for index := 0; index+1 < len(extra); index += 2 {
		query.Set(extra[index], extra[index+1])
	}
	var out struct {
		Items []calendarEvent `json:"items"`
	}
	h.Calendar.Call(http.MethodGet, "/v1/events?"+query.Encode(), nil, http.StatusOK, &out)
	return out.Items
}

// titles are the events' titles with their start (date or time in UTC).
func titles(items []calendarEvent) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		when := ""
		if item.AllDay {
			when = deref(item.StartDate) + ".." + deref(item.EndDate)
		} else {
			when = item.StartAt.UTC().Format("01-02T15:04")
		}
		mark := ""
		if item.Status == "cancelled" {
			mark = " (cancelled)"
		}
		out = append(out, item.Title+" "+when+mark)
	}
	return out
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func remindersOf(list []calendar.Reminder) string {
	var out []string
	for _, reminder := range list {
		if reminder.MinutesBefore != nil {
			out = append(out, itoa(*reminder.MinutesBefore))
		}
	}
	return strings.Join(out, ",")
}

func itoa(value int) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// googleCalendar is a calendar of the service's list.
type googleCalendar struct {
	ID               string `json:"id"`
	GoogleCalendarID string `json:"googleCalendarId"`
	SyncEnabled      bool   `json:"syncEnabled"`
	LifeOS           bool   `json:"lifeOs"`
	Sync             struct {
		State        string     `json:"state"`
		SourceID     *string    `json:"sourceId"`
		LastSyncedAt *time.Time `json:"lastSyncedAt"`
		NextSyncAt   *time.Time `json:"nextSyncAt"`
		LastError    *string    `json:"lastError"`
		LastCounts   *struct {
			Inserted, Updated, Removed, Restored, Imported int
		} `json:"lastCounts"`
		ConsecutiveFailures int `json:"consecutiveFailures"`
	} `json:"sync"`
}

func calendarOf(h *testsupport.Harness, googleID string) googleCalendar {
	h.T.Helper()
	var list struct {
		Items []googleCalendar `json:"items"`
	}
	h.Call(http.MethodGet, "/v1/calendars", nil, http.StatusOK, &list)
	for _, item := range list.Items {
		if item.GoogleCalendarID == googleID {
			return item
		}
	}
	h.T.Fatalf("no calendar %s in %+v", googleID, list.Items)
	return googleCalendar{}
}

// counts are a sync event's counts.
func counts(event activity.Event) map[string]float64 {
	out := map[string]float64{}
	raw, _ := json.Marshal(event.Data["counts"])
	_ = json.Unmarshal(raw, &out)
	return out
}

// newEvents returns the events recorded since *seen of the given types
// (all when none), moving *seen on.
func newEvents(h *testsupport.Harness, seen *int, types ...string) []activity.Event {
	all := h.Recorder.Events()
	fresh := all[*seen:]
	*seen = len(all)
	if len(types) == 0 {
		return fresh
	}
	var out []activity.Event
	for _, event := range fresh {
		for _, eventType := range types {
			if event.Type == eventType {
				out = append(out, event)
			}
		}
	}
	return out
}

func writesTo(h *testsupport.Harness, calendarID string) []fakegoogle.Write {
	var out []fakegoogle.Write
	for _, write := range h.Fake.Writes() {
		if write.Calendar == calendarID {
			out = append(out, write)
		}
	}
	return out
}

func jsonInto(raw []byte, out any) error { return json.Unmarshal(raw, out) }

// ofSource are the events about one Calendar source.
func ofSource(events []activity.Event, sourceID string) []activity.Event {
	var out []activity.Event
	for _, event := range events {
		if event.Subject.ID == sourceID {
			out = append(out, event)
		}
	}
	return out
}
