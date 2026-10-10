package google

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/services/google-calendar/internal/sources"
)

func ptr[T any](value T) *T { return &value }

func TestRRuleAndExDate(t *testing.T) {
	until := time.Date(2026, 12, 31, 22, 59, 59, 0, time.UTC)
	for _, test := range []struct {
		rule   sources.Recurrence
		allDay bool
		want   string
	}{
		{sources.Recurrence{Frequency: "daily", Interval: 1}, false, "RRULE:FREQ=DAILY"},
		{sources.Recurrence{Frequency: "weekly", Interval: 2, ByWeekday: []string{"MO", "WE"}, Count: 10}, false, "RRULE:FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE;COUNT=10"},
		{sources.Recurrence{Frequency: "monthly", Interval: 1, Until: &until}, false, "RRULE:FREQ=MONTHLY;UNTIL=20261231T225959Z"},
		// An all-day series' until is a date: until's date in the owner's
		// timezone (23:59:59 UTC is already 1 January in Berlin).
		{sources.Recurrence{Frequency: "yearly", Interval: 1, Until: ptr(time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC))}, true, "RRULE:FREQ=YEARLY;UNTIL=20270101"},
	} {
		if got := RRule(test.rule, test.allDay, berlin); got != test.want {
			t.Errorf("RRule(%+v) = %q, want %q", test.rule, got, test.want)
		}
	}
	if line, ok := ExDate("2026-10-14T07:00:00Z", false); !ok || line != "EXDATE:20261014T070000Z" {
		t.Fatalf("timed EXDATE = %q", line)
	}
	if line, ok := ExDate("2026-10-24", true); !ok || line != "EXDATE;VALUE=DATE:20261024" {
		t.Fatalf("all-day EXDATE = %q", line)
	}
	if _, ok := ExDate("soon", false); ok {
		t.Fatal("an unreadable key made an EXDATE")
	}
	if got := instanceID("standup", "2026-10-19T07:00:00Z", false); got != "standup_20261019T070000Z" {
		t.Fatalf("instance id = %q", got)
	}
	if got := instanceID("trip", "2026-10-20", true); got != "trip_20261020" {
		t.Fatalf("all-day instance id = %q", got)
	}
}

func dentistFields() Fields {
	return Fields{
		Title: "Dentist", Description: ptr("Bring the card"), Location: ptr("Praxis Weber"),
		StartAt: time.Date(2026, 10, 12, 7, 30, 0, 0, time.UTC), EndAt: time.Date(2026, 10, 12, 8, 15, 0, 0, time.UTC),
		Reminders: []sources.Reminder{sources.Relative(30)}, UseDefaultReminders: true,
	}
}

func TestEventPatchWritesOnlyTheNamedFields(t *testing.T) {
	f := dentistFields()
	body, err := EventPatch(f, map[string]bool{"title": true}, berlin, nil)
	if err != nil || !reflect.DeepEqual(body, map[string]any{"summary": "Dentist"}) {
		t.Fatalf("title patch = %v %v", body, err)
	}
	// Clearing notes and place writes empty values; a move writes both
	// ends in the event's zone (the owner's when it has none).
	f.Description, f.Location = nil, nil
	body, err = EventPatch(f, map[string]bool{"description": true, "location": true, "startAt": true, "endAt": true}, berlin, nil)
	if err != nil || body["description"] != "" || body["location"] != "" {
		t.Fatalf("clear patch = %v %v", body, err)
	}
	start := body["start"].(map[string]any)
	if start["dateTime"] != "2026-10-12T09:30:00+02:00" || start["timeZone"] != "Europe/Berlin" || start["date"] != nil {
		t.Fatalf("start = %v", start)
	}
	if _, ok := body["recurrence"]; ok {
		t.Fatalf("a single event got a recurrence: %v", body)
	}
	// Switching to all-day clears the times (null) and writes dates, the
	// end exclusive.
	day := Fields{Title: "Trip", AllDay: true, StartDate: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)}
	body, _ = EventPatch(day, map[string]bool{"allDay": true, "startDate": true, "endDate": true}, berlin, nil)
	if got := body["end"].(map[string]any); got["date"] != "2026-10-23" || got["dateTime"] != nil {
		t.Fatalf("all-day end = %v", got)
	}
	// Reminders: own relative ones as popups, the defaults as useDefault.
	body, err = EventPatch(f, map[string]bool{"reminders": true}, berlin, nil)
	reminders, _ := json.Marshal(body["reminders"])
	if err != nil || string(reminders) != `{"overrides":[{"method":"popup","minutes":30}],"useDefault":true}` {
		t.Fatalf("reminders = %s %v", reminders, err)
	}
	// Priority goes to the private extended property; none clears it.
	f.Priority = ptr("high")
	body, _ = EventPatch(f, map[string]bool{"priority": true}, berlin, nil)
	if got, _ := json.Marshal(body); string(got) != `{"extendedProperties":{"private":{"lifeosPriority":"high"}}}` {
		t.Fatalf("priority = %s", got)
	}
	f.Priority = nil
	body, _ = EventPatch(f, map[string]bool{"priority": true}, berlin, nil)
	if got, _ := json.Marshal(body); string(got) != `{"extendedProperties":{"private":{"lifeosPriority":""}}}` {
		t.Fatalf("no priority = %s", got)
	}
}

func TestEventPatchRefusesRemindersGoogleCannotHold(t *testing.T) {
	f := dentistFields()
	f.Reminders = []sources.Reminder{{Kind: "localTime", DaysBefore: ptr(1), LocalTime: ptr("19:00")}}
	if _, err := EventPatch(f, map[string]bool{"reminders": true}, berlin, nil); !errors.Is(err, ErrReminderNotInGoogle) {
		t.Fatalf("timed localTime: %v", err)
	}
	f.Reminders = []sources.Reminder{sources.Relative(1), sources.Relative(2), sources.Relative(3), sources.Relative(4), sources.Relative(5), sources.Relative(6)}
	if _, err := EventPatch(f, map[string]bool{"reminders": true}, berlin, nil); !errors.Is(err, ErrTooManyGoogleReminders) {
		t.Fatalf("six reminders: %v", err)
	}
	// On an all-day event a reminder at a time of day before it is minutes
	// before its first midnight; on the day itself it is not.
	day := Fields{Title: "Trip", AllDay: true, StartDate: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
		Reminders: []sources.Reminder{{Kind: "localTime", DaysBefore: ptr(1), LocalTime: ptr("19:00")}}, UseDefaultReminders: true}
	body, err := EventPatch(day, map[string]bool{"reminders": true}, berlin, nil)
	reminders, _ := json.Marshal(body["reminders"])
	if err != nil || string(reminders) != `{"overrides":[{"method":"popup","minutes":300}],"useDefault":false}` {
		t.Fatalf("all-day reminders = %s %v", reminders, err)
	}
	day.Reminders = []sources.Reminder{{Kind: "localTime", DaysBefore: ptr(0), LocalTime: ptr("08:00")}}
	if _, err := EventPatch(day, map[string]bool{"reminders": true}, berlin, nil); !errors.Is(err, ErrReminderNotInGoogle) {
		t.Fatalf("same-day localTime: %v", err)
	}
}

func TestEventPatchKeepsSkipsWhenTheRuleChanges(t *testing.T) {
	f := dentistFields()
	f.Recurrence = &sources.Recurrence{Frequency: "weekly", Interval: 1, ByWeekday: []string{"MO", "WE"}}
	keep := exdateLines(Event{Recurrence: []string{"RRULE:FREQ=WEEKLY;BYDAY=MO", "EXDATE;TZID=Europe/Berlin:20261014T090000"}})
	body, _ := EventPatch(f, map[string]bool{"recurrence": true}, berlin, keep)
	if got := body["recurrence"]; !reflect.DeepEqual(got, []string{"RRULE:FREQ=WEEKLY;BYDAY=MO,WE", "EXDATE;TZID=Europe/Berlin:20261014T090000"}) {
		t.Fatalf("recurrence = %v", got)
	}
	// Moving a series rewrites its rule with it; ending it is an empty list.
	body, _ = EventPatch(f, map[string]bool{"startAt": true, "endAt": true}, berlin, keep)
	if _, ok := body["recurrence"]; !ok {
		t.Fatalf("moved series without its rule: %v", body)
	}
	f.Recurrence = nil
	body, _ = EventPatch(f, map[string]bool{"recurrence": true}, berlin, keep)
	if got := body["recurrence"]; !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("no recurrence = %v", got)
	}
}

func TestExportBodyRoundTripsThroughTheMapping(t *testing.T) {
	id := googleuuid.MustParse("7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10")
	if got := ExportID(id); got != "lifeos7f8d2b1e1c1a4b7e9a512f0c6d1e9a10" || strings.ContainsAny(got, "wxyz-_") {
		t.Fatalf("export id = %q", got)
	}
	until := time.Date(2026, 12, 21, 8, 0, 0, 0, time.UTC)
	f := Fields{
		Title: "Gym", Location: ptr("Studio"), StartAt: time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC), EndAt: time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC),
		Recurrence: &sources.Recurrence{Frequency: "weekly", Interval: 1, ByWeekday: []string{"MO", "TH"}, Until: &until}, Priority: ptr("high"),
	}
	effective := []sources.Reminder{
		sources.Relative(10), sources.Relative(10), {Kind: "localTime", DaysBefore: ptr(1), LocalTime: ptr("19:00")},
		sources.Relative(20), sources.Relative(30), sources.Relative(40), sources.Relative(50), sources.Relative(60),
	}
	body := ExportBody(id, f, effective, berlin, []string{"EXDATE:20261015T070000Z"})
	raw, _ := json.Marshal(body)
	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	event.ID = ExportID(id)
	// Effective reminders as popups, each lead time once, at most five; a
	// time of day before a timed event cannot be held.
	if event.Reminders == nil || event.Reminders.UseDefault || len(event.Reminders.Overrides) != 5 || event.Reminders.Overrides[0].Minutes != 10 {
		t.Fatalf("reminders = %+v", event.Reminders)
	}
	if got, ok := exportedEventID(event); !ok || got != id || event.private(propertyPriority) != "high" {
		t.Fatalf("extended properties = %+v", event.ExtendedProperties)
	}
	items := byUID(derive(t, event))
	gym := items[event.ID]
	if gym.Title != "Gym" || *gym.Location != "Studio" || !gym.StartAt.Equal(f.StartAt) || !gym.EndAt.Equal(f.EndAt) || *gym.TimeZone != "Europe/Berlin" ||
		gym.Recurrence == nil || gym.Recurrence.Frequency != "weekly" || strings.Join(gym.Recurrence.ByWeekday, ",") != "MO,TH" ||
		!gym.Recurrence.Until.Equal(until) || *gym.Priority != "high" {
		t.Fatalf("gym = %+v %+v", gym, gym.Recurrence)
	}
	if exception, ok := gym.Exceptions["2026-10-15T07:00:00Z"]; !ok || !exception.Cancelled {
		t.Fatalf("exceptions = %+v", gym.Exceptions)
	}

	// An all-day yearly series ending on a date keeps that date.
	birthday := Fields{
		Title: "Birthday", AllDay: true, StartDate: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC),
		Recurrence: &sources.Recurrence{Frequency: "yearly", Interval: 1, Until: ptr(time.Date(2030, 9, 17, 10, 0, 0, 0, time.UTC))},
	}
	raw, _ = json.Marshal(ExportBody(id, birthday, nil, berlin, []string{"EXDATE;VALUE=DATE:20270917"}))
	event = Event{}
	_ = json.Unmarshal(raw, &event)
	event.ID = "birthday"
	item := byUID(derive(t, event))["birthday"]
	if !item.AllDay || item.StartDate.Format(time.DateOnly) != "2026-09-17" || item.EndDate.Format(time.DateOnly) != "2026-09-17" ||
		item.Recurrence == nil || item.Recurrence.Until.In(berlin).Format(time.DateOnly) != "2030-09-17" || !item.Exceptions["2027-09-17"].Cancelled {
		t.Fatalf("birthday = %+v %+v", item, item.Recurrence)
	}
	if item.Priority == nil || *item.Priority != "" {
		t.Fatalf("birthday priority = %v", item.Priority)
	}
}

func TestPriorityReadFromGoogle(t *testing.T) {
	master := Event{
		ID: "standup", Summary: "Standup", Start: timed("2026-10-12T09:00:00+02:00", "Europe/Berlin"), End: timed("2026-10-12T09:15:00+02:00", "Europe/Berlin"),
		Recurrence: []string{"RRULE:FREQ=WEEKLY;BYDAY=MO"}, ExtendedProperties: &ExtendedProperties{Private: map[string]string{propertyPriority: "low"}},
	}
	moved := Event{
		ID: "standup_20261019T070000Z", RecurringEventID: "standup", OriginalStartTime: timed("2026-10-19T09:00:00+02:00", "Europe/Berlin"),
		Summary: "Standup", Start: timed("2026-10-19T09:00:00+02:00", "Europe/Berlin"), End: timed("2026-10-19T09:15:00+02:00", "Europe/Berlin"),
		ExtendedProperties: &ExtendedProperties{Private: map[string]string{propertyPriority: "high"}},
	}
	plain := Event{ID: "call", Summary: "Call", Start: timed("2026-10-13T16:00:00Z", ""), End: timed("2026-10-13T17:00:00Z", "")}
	odd := Event{ID: "odd", Summary: "Odd", Start: timed("2026-10-13T16:00:00Z", ""), End: timed("2026-10-13T17:00:00Z", ""),
		ExtendedProperties: &ExtendedProperties{Private: map[string]string{propertyPriority: "urgent"}}}
	items := byUID(derive(t, master, moved, plain, odd))
	standup := items["standup"]
	if standup.Priority == nil || *standup.Priority != "low" {
		t.Fatalf("standup priority = %v", standup.Priority)
	}
	if changes := standup.Exceptions["2026-10-19T07:00:00Z"].Changes; changes.Priority == nil || *changes.Priority != "high" {
		t.Fatalf("occurrence priority = %+v", standup.Exceptions)
	}
	if items["call"].Priority != nil || items["odd"].Priority != nil {
		t.Fatalf("priorities without a valid property = %v %v", items["call"].Priority, items["odd"].Priority)
	}
}
