package reminders

import (
	"strings"
	"testing"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
)

var berlin = mustLocation("Europe/Berlin")

func mustLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func ptr[T any](value T) *T { return &value }

func relative(minutes int) generated.Reminder {
	return generated.Reminder{Kind: generated.Relative, MinutesBefore: ptr(minutes)}
}

func localTime(days int, clock string) generated.Reminder {
	return generated.Reminder{Kind: generated.LocalTime, DaysBefore: ptr(days), LocalTime: ptr(clock)}
}

func instant(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

var eventID = googleuuid.MustParse("7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10")

// timed is an active timed event from start for minutes, with reminders as
// its effective ones.
func timed(title, start string, minutes int, reminders ...generated.Reminder) generated.CalendarEvent {
	begin := instant(start)
	end := begin.Add(time.Duration(minutes) * time.Minute)
	return generated.CalendarEvent{
		Id: eventID, Title: title, StartAt: &begin, EndAt: &end, Status: generated.Active,
		Source: generated.EventSource{Kind: generated.EventSourceKindManual}, EffectiveReminders: reminders,
	}
}

func allDay(title, first, last string, reminders ...generated.Reminder) generated.CalendarEvent {
	day := func(value string) *types.Date {
		parsed, err := time.Parse(time.DateOnly, value)
		if err != nil {
			panic(err)
		}
		return &types.Date{Time: parsed}
	}
	return generated.CalendarEvent{
		Id: eventID, Title: title, AllDay: true, StartDate: day(first), EndDate: day(last), Status: generated.Active,
		Source: generated.EventSource{Kind: generated.EventSourceKindManual}, EffectiveReminders: reminders,
	}
}

func TestReminderTimes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		item     generated.CalendarEvent
		reminder generated.Reminder
		want     string // RFC 3339 in UTC
		key      string
	}{
		{"30 minutes before", timed("Dentist", "2026-10-11T13:00:00Z", 60), relative(30), "2026-10-11T12:30:00Z", "rel:30"},
		{"at the start", timed("Dentist", "2026-10-11T13:00:00Z", 60), relative(0), "2026-10-11T13:00:00Z", "rel:0"},
		{"28 days before", timed("Dentist", "2026-10-11T13:00:00Z", 60), relative(40320), "2026-09-13T13:00:00Z", "rel:40320"},
		// Relative reminders are exact durations: across the change to CET
		// "1 day before" is 24 hours, so the wall clock differs by one.
		{"a day before, over the change to winter time", timed("Gym", "2026-10-26T07:00:00Z", 60), relative(1440), "2026-10-25T07:00:00Z", "rel:1440"},
		// localTime reminders keep the wall clock whatever daylight saving
		// does, in both directions.
		{"evening before a pickup", timed("Papier", "2026-10-14T04:00:00Z", 15), localTime(1, "19:00"), "2026-10-13T17:00:00Z", "local:1:19:00"},
		{"evening before, the day before summer time", timed("Papier", "2026-03-29T04:00:00Z", 15), localTime(1, "19:00"), "2026-03-28T18:00:00Z", "local:1:19:00"},
		{"evening before, on the day summer time starts", timed("Papier", "2026-03-30T04:00:00Z", 15), localTime(1, "19:00"), "2026-03-29T17:00:00Z", "local:1:19:00"},
		{"evening before, the day before winter time", timed("Papier", "2026-10-25T05:00:00Z", 15), localTime(1, "19:00"), "2026-10-24T17:00:00Z", "local:1:19:00"},
		{"evening before, on the day winter time starts", timed("Papier", "2026-10-26T05:00:00Z", 15), localTime(1, "19:00"), "2026-10-25T18:00:00Z", "local:1:19:00"},
		// The start's date is the owner's: 00:30 CEST on the 14th is the
		// 13th in UTC.
		{"owner's date of the start", timed("Night train", "2026-10-13T22:30:00Z", 60), localTime(1, "19:00"), "2026-10-13T17:00:00Z", "local:1:19:00"},
		{"skipped wall time", timed("Early", "2026-03-30T04:00:00Z", 15), localTime(1, "02:30"), "2026-03-29T01:30:00Z", "local:1:02:30"},
		{"repeated wall time is the first", timed("Early", "2026-10-26T05:00:00Z", 15), localTime(1, "02:30"), "2026-10-25T00:30:00Z", "local:1:02:30"},
		{"four weeks before", timed("Dentist", "2026-10-11T13:00:00Z", 60), localTime(28, "09:00"), "2026-09-13T07:00:00Z", "local:28:09:00"},
		// An all-day event starts at owner-local midnight of its first day.
		{"all-day at midnight", allDay("Birthday", "2026-10-15", "2026-10-15"), relative(0), "2026-10-14T22:00:00Z", "rel:0"},
		{"all-day a day before", allDay("Birthday", "2026-10-15", "2026-10-15"), relative(1440), "2026-10-13T22:00:00Z", "rel:1440"},
		{"all-day on the day", allDay("Birthday", "2026-10-15", "2026-10-15"), localTime(0, "08:00"), "2026-10-15T06:00:00Z", "local:0:08:00"},
		{"all-day the evening before", allDay("Birthday", "2026-10-15", "2026-10-15"), localTime(1, "19:00"), "2026-10-14T17:00:00Z", "local:1:19:00"},
		{"all-day in winter time", allDay("Holiday", "2026-12-24", "2026-12-26"), localTime(0, "09:00"), "2026-12-24T08:00:00Z", "local:0:09:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, ok := occurrenceStart(tc.item, berlin)
			if !ok {
				t.Fatal("no start")
			}
			got, key, ok := reminderTime(tc.reminder, start, berlin)
			if !ok || got.UTC().Format(time.RFC3339) != tc.want || key != tc.key {
				t.Fatalf("reminderTime = %s %q %v, want %s %q", got.UTC().Format(time.RFC3339), key, ok, tc.want, tc.key)
			}
		})
	}
}

func TestInvalidRemindersAreNotDue(t *testing.T) {
	start := instant("2026-10-11T13:00:00Z")
	for name, reminder := range map[string]generated.Reminder{
		"negative minutes":   relative(-1),
		"beyond 28 days":     relative(40321),
		"no minutes":         {Kind: generated.Relative},
		"no local time":      {Kind: generated.LocalTime, DaysBefore: ptr(1)},
		"bad local time":     localTime(1, "7pm"),
		"too many days":      localTime(29, "19:00"),
		"unknown kind":       {Kind: "email", MinutesBefore: ptr(10)},
		"single digit clock": localTime(1, "9:00"),
	} {
		if _, _, ok := reminderTime(reminder, start, berlin); ok {
			t.Errorf("%s: reminder is due", name)
		}
	}
}

func TestWallTimeElsewhere(t *testing.T) {
	// Lord Howe Island moves its clocks by 30 minutes.
	lordHowe := mustLocation("Australia/Lord_Howe")
	got := wallTime(2026, time.April, 5, 1, 45, lordHowe)
	if got.Hour() != 1 || got.Minute() != 45 {
		t.Fatalf("wallTime = %s", got)
	}
	_, offset := got.Zone()
	if offset != 11*3600 {
		t.Fatalf("a repeated wall time must be the first (UTC+11), got %s", got)
	}
	if got := wallTime(2026, time.October, 10, 19, 0, time.UTC); !got.Equal(instant("2026-10-10T19:00:00Z")) {
		t.Fatalf("UTC wallTime = %s", got)
	}
}

func TestDueBetweenTakesTheWindowInOrder(t *testing.T) {
	dentist := timed("Dentist", "2026-10-11T13:00:00Z", 60, relative(30), relative(180), relative(1440))
	cancelled := timed("Cancelled", "2026-10-11T13:00:00Z", 60, relative(30))
	cancelled.Status = generated.Cancelled
	other := timed("Call", "2026-10-11T12:40:00Z", 30, relative(10))
	other.Id = googleuuid.MustParse("00000000-0000-4000-8000-000000000001")
	items := []generated.CalendarEvent{dentist, cancelled, other}

	// (from, to]: the 12:30 reminders are in, 10:00 (exactly from) is out.
	due := dueBetween(items, berlin, instant("2026-10-11T10:00:00Z"), instant("2026-10-11T12:30:00Z"))
	var got []string
	for _, reminder := range due {
		got = append(got, reminder.Event.Title+"@"+reminder.At.UTC().Format("15:04"))
	}
	if strings.Join(got, " ") != "Call@12:30 Dentist@12:30" {
		t.Fatalf("due = %v", got)
	}
	due = dueBetween(items, berlin, instant("2026-10-11T09:59:59Z"), instant("2026-10-11T10:00:00Z"))
	if len(due) != 1 || due[0].Key != "calendar:reminder:"+eventID.String()+":single:2026-10-11T13:00:00Z:rel:180" {
		t.Fatalf("due = %+v", due)
	}
	// The same reminder twice (defaults and own) is due once.
	twice := timed("Twice", "2026-10-11T13:00:00Z", 60, relative(30), relative(30))
	if due := dueBetween([]generated.CalendarEvent{twice}, berlin, instant("2026-10-11T12:00:00Z"), instant("2026-10-11T13:00:00Z")); len(due) != 1 {
		t.Fatalf("due = %+v", due)
	}
}

func TestDedupeKeysNameTheOccurrenceAtItsTime(t *testing.T) {
	id := eventID.String()
	single := timed("Dentist", "2026-10-11T13:00:00Z", 60, relative(30))
	occurrence := timed("Gym", "2026-10-12T05:00:00Z", 60, localTime(1, "19:00"))
	occurrence.Occurrence = &generated.EventOccurrence{SeriesId: eventID, RecurrenceId: "2026-10-12T05:00:00Z"}
	moved := timed("Gym", "2026-10-12T07:00:00Z", 60, localTime(1, "19:00"))
	moved.Occurrence = &generated.EventOccurrence{SeriesId: eventID, RecurrenceId: "2026-10-12T05:00:00Z", Modified: true}
	birthday := allDay("Birthday", "2026-10-15", "2026-10-15", localTime(1, "19:00"))
	series := allDay("Bins", "2026-10-16", "2026-10-16", relative(0))
	series.Occurrence = &generated.EventOccurrence{SeriesId: eventID, RecurrenceId: "2026-10-16"}

	keyOf := func(item generated.CalendarEvent) string {
		start, _ := occurrenceStart(item, berlin)
		_, reminderKey, _ := reminderTime(item.EffectiveReminders[0], start, berlin)
		return dedupeKey(item, start, berlin, reminderKey)
	}
	for _, tc := range []struct {
		item generated.CalendarEvent
		want string
	}{
		{single, "calendar:reminder:" + id + ":single:2026-10-11T13:00:00Z:rel:30"},
		{occurrence, "calendar:reminder:" + id + ":2026-10-12T05:00:00Z:2026-10-12T05:00:00Z:local:1:19:00"},
		{moved, "calendar:reminder:" + id + ":2026-10-12T05:00:00Z:2026-10-12T07:00:00Z:local:1:19:00"},
		{birthday, "calendar:reminder:" + id + ":single:2026-10-15:local:1:19:00"},
		{series, "calendar:reminder:" + id + ":2026-10-16:2026-10-16:rel:0"},
	} {
		if got := keyOf(tc.item); got != tc.want {
			t.Errorf("key = %q, want %q", got, tc.want)
		}
		if len(keyOf(tc.item)) > 200 {
			t.Errorf("key %q is longer than Notifications allows", tc.want)
		}
	}
	rescheduled := single
	later := instant("2026-10-11T15:00:00Z")
	rescheduled.StartAt = &later
	if keyOf(rescheduled) == keyOf(single) {
		t.Fatal("a moved event must remind again at its new time")
	}
}
