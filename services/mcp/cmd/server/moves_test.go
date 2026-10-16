package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// testOwner is an owner in Berlin on Friday 16 October 2026, 10:00 CEST.
func testOwner(t *testing.T) owner {
	t.Helper()
	return owner{zone: berlin(t), now: func() time.Time { return mustTime(t, "2026-10-16T08:00:00Z") }}
}

func berlin(t *testing.T) *time.Location {
	t.Helper()
	zone, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return zone
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func ptr[T any](value T) *T { return &value }

func mustPlan(t *testing.T, m move) movePlan {
	t.Helper()
	p, err := m.plan(testOwner(t))
	if err != nil {
		t.Fatalf("plan %+v: %v", m, err)
	}
	return p
}

// In Berlin the clocks go back on Sunday 25 October 2026 at 03:00 CEST
// (01:00 UTC) and forward on Sunday 29 March 2026 at 02:00 CET (01:00 UTC).
func TestWallClockFollowsTheICalendarRuleAcrossDaylightSaving(t *testing.T) {
	zone := berlin(t)
	day := func(value string) time.Time {
		d, _ := time.Parse(time.DateOnly, value)
		return d
	}
	for name, test := range map[string]struct {
		day          string
		hour, minute int
		want         string
	}{
		"summer time":                          {"2026-10-24", 10, 0, "2026-10-24T08:00:00Z"},
		"winter time":                          {"2026-10-25", 10, 0, "2026-10-25T09:00:00Z"},
		"twice when the clocks go back: first": {"2026-10-25", 2, 30, "2026-10-25T00:30:00Z"},
		"before the change":                    {"2026-10-25", 1, 30, "2026-10-24T23:30:00Z"},
		"after the change":                     {"2026-10-25", 3, 0, "2026-10-25T02:00:00Z"},
		"missing when they go forward: later":  {"2026-03-29", 2, 30, "2026-03-29T01:30:00Z"},
		"local midnight":                       {"2026-10-26", 0, 0, "2026-10-25T23:00:00Z"},
	} {
		got := wallClock(day(test.day), test.hour, test.minute, 0, zone)
		if !got.Equal(mustTime(t, test.want)) {
			t.Errorf("%s: %s %02d:%02d is %s, want %s", name, test.day, test.hour, test.minute, got.UTC().Format(time.RFC3339), test.want)
		}
	}
}

func TestTaskMoves(t *testing.T) {
	for name, test := range map[string]struct {
		due  string // "" for an unscheduled Task
		move move
		want string
	}{
		// A Task due Monday 19 October at 15:30, moved "to tomorrow, same
		// time" on Friday 16 October: the date is the caller's, the time
		// the Task's.
		"tomorrow, same time":         {"2026-10-19T13:30:00Z", move{ToDate: ptr("2026-10-17")}, "2026-10-17T13:30:00Z"},
		"two days later, same time":   {"2026-10-19T13:30:00Z", move{Days: ptr(2)}, "2026-10-21T13:30:00Z"},
		"a day earlier":               {"2026-10-19T13:30:00Z", move{Days: ptr(-1)}, "2026-10-18T13:30:00Z"},
		"an hour later":               {"2026-10-19T13:30:00Z", move{Hours: ptr(1)}, "2026-10-19T14:30:00Z"},
		"90 minutes earlier":          {"2026-10-19T13:30:00Z", move{Hours: ptr(-1), Minutes: ptr(-30)}, "2026-10-19T12:00:00Z"},
		"to a date and time":          {"2026-10-19T13:30:00Z", move{ToDate: ptr("2026-10-30"), ToTime: ptr("09:00")}, "2026-10-30T08:00:00Z"},
		"to a time the same day":      {"2026-10-19T13:30:00Z", move{ToTime: ptr("18:00")}, "2026-10-19T16:00:00Z"},
		"a day later at a time":       {"2026-10-19T13:30:00Z", move{Days: ptr(1), ToTime: ptr("07:15")}, "2026-10-20T05:15:00Z"},
		"drop the time":               {"2026-10-19T13:30:00Z", move{DateOnly: ptr(true)}, "2026-10-18T22:00:00Z"},
		"drop the time and move":      {"2026-10-19T13:30:00Z", move{ToDate: ptr("2026-10-26"), DateOnly: ptr(true)}, "2026-10-25T23:00:00Z"},
		"23:30 plus an hour":          {"2026-10-16T21:30:00Z", move{Hours: ptr(1)}, "2026-10-16T22:30:00Z"},
		"23:30 plus 30 minutes":       {"2026-10-16T21:30:00Z", move{Minutes: ptr(30)}, "2026-10-16T22:00:00Z"},
		"tomorrow across the change":  {"2026-10-24T08:00:00Z", move{ToDate: ptr("2026-10-25")}, "2026-10-25T09:00:00Z"},
		"a day across the change":     {"2026-10-24T08:00:00Z", move{Days: ptr(1)}, "2026-10-25T09:00:00Z"},
		"hours across the change":     {"2026-10-24T23:30:00Z", move{Hours: ptr(2)}, "2026-10-25T01:30:00Z"},
		"23:30 on the night it falls": {"2026-10-24T21:30:00Z", move{Hours: ptr(1)}, "2026-10-24T22:30:00Z"},
		"to a time that occurs twice": {"2026-10-24T00:30:00Z", move{Days: ptr(1)}, "2026-10-25T00:30:00Z"},
		"to a time that is missing":   {"2026-03-28T01:30:00Z", move{Days: ptr(1)}, "2026-03-29T01:30:00Z"},
		"back into summer time":       {"2026-10-26T09:00:00Z", move{Days: ptr(-2)}, "2026-10-24T08:00:00Z"},
		// Date-only Tasks (the owner-local midnight) keep having no time.
		"date only, a day later":          {"2026-10-18T22:00:00Z", move{Days: ptr(1)}, "2026-10-19T22:00:00Z"},
		"date only, to Friday":            {"2026-10-18T22:00:00Z", move{ToDate: ptr("2026-10-23")}, "2026-10-22T22:00:00Z"},
		"date only, across the change":    {"2026-10-23T22:00:00Z", move{Days: ptr(2)}, "2026-10-25T23:00:00Z"},
		"date only as a UTC midnight":     {"2026-10-19T00:00:00Z", move{Days: ptr(1)}, "2026-10-19T22:00:00Z"},
		"date only, given a time":         {"2026-10-18T22:00:00Z", move{ToTime: ptr("09:00")}, "2026-10-19T07:00:00Z"},
		"date only, already without time": {"2026-10-18T22:00:00Z", move{DateOnly: ptr(true)}, "2026-10-18T22:00:00Z"},
		// An unscheduled Task gets the date, and a time only when named.
		"unscheduled, to a date":          {"", move{ToDate: ptr("2026-10-20")}, "2026-10-19T22:00:00Z"},
		"unscheduled, to a date and time": {"", move{ToDate: ptr("2026-10-20"), ToTime: ptr("10:00")}, "2026-10-20T08:00:00Z"},
		// The capture's own timezone, when given.
		"in another timezone": {"2026-10-19T13:30:00Z", move{ToDate: ptr("2026-10-20"), TimeZone: ptr("America/New_York")}, "2026-10-20T13:30:00Z"},
	} {
		var due *time.Time
		if test.due != "" {
			at := mustTime(t, test.due)
			due = &at
		}
		got, err := mustPlan(t, test.move).task("Call the vet", due)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !got.Equal(mustTime(t, test.want)) {
			t.Errorf("%s: due %s, want %s", name, got.UTC().Format(time.RFC3339), test.want)
		}
	}
}

func TestTaskMovesThatDoNotFitAreRefused(t *testing.T) {
	for name, test := range map[string]struct {
		due  string
		move move
		want string
	}{
		"hours without a time":      {"2026-10-18T22:00:00Z", move{Hours: ptr(1)}, "without a time"},
		"days without a due date":   {"", move{Days: ptr(1)}, "has no due date"},
		"hours without a due date":  {"", move{Hours: ptr(1)}, "has no due date"},
		"a time without a due date": {"", move{ToTime: ptr("10:00")}, "has no due date"},
		"no time without a date":    {"", move{DateOnly: ptr(true)}, "has no due date"},
		"a date and hours, undated": {"", move{ToDate: ptr("2026-10-20"), Hours: ptr(1)}, "no due time"},
	} {
		var due *time.Time
		if test.due != "" {
			at := mustTime(t, test.due)
			due = &at
		}
		_, err := mustPlan(t, test.move).task("Call the vet", due)
		var refused refusedMove
		if !errors.As(err, &refused) || !strings.HasPrefix(err.Error(), "cannot move: “Call the vet”") || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: err = %v, want a refusal saying %q", name, err, test.want)
		}
	}
}

func TestMovesAreCheckedBeforeAnythingIsRead(t *testing.T) {
	for name, test := range map[string]struct {
		move move
		want string
	}{
		"nothing":            {move{}, "changes nothing"},
		"zero days":          {move{Days: ptr(0), Hours: ptr(0)}, "changes nothing"},
		"days and a date":    {move{Days: ptr(1), ToDate: ptr("2026-10-20")}, "days and move.toDate"},
		"a time and hours":   {move{ToTime: ptr("10:00"), Hours: ptr(1)}, "toTime cannot be combined"},
		"no time and a time": {move{DateOnly: ptr(true), ToTime: ptr("10:00")}, "dateOnly drops the time"},
		"no time and hours":  {move{DateOnly: ptr(true), Minutes: ptr(15)}, "dateOnly drops the time"},
		"a date in words":    {move{ToDate: ptr("Friday")}, "YYYY-MM-DD"},
		"a short date":       {move{ToDate: ptr("2026-10-2")}, "YYYY-MM-DD"},
		"a time past 23:59":  {move{ToTime: ptr("24:00")}, "HH:MM"},
		"a time in seconds":  {move{ToTime: ptr("10:00:00")}, "HH:MM"},
		"too many days":      {move{Days: ptr(5000)}, "move.days must be between"},
		"too many hours":     {move{Hours: ptr(-9000)}, "move.hours must be between"},
		"too many minutes":   {move{Minutes: ptr(600000)}, "move.minutes must be between"},
		"an unknown zone":    {move{Days: ptr(1), TimeZone: ptr("Mars/Olympus")}, "move.timeZone"},
		"the local zone":     {move{Days: ptr(1), TimeZone: ptr("Local")}, "move.timeZone"},
	} {
		_, err := test.move.plan(testOwner(t))
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: err = %v, want %q", name, err, test.want)
		}
	}
	// A move that names a day stays valid with zero shifts beside it.
	if _, err := (move{Days: ptr(0), ToDate: ptr("2026-10-20")}).plan(testOwner(t)); err != nil {
		t.Errorf("toDate with days 0: %v", err)
	}
}

func TestEventMovesKeepTheLength(t *testing.T) {
	timed := func(start, end string) map[string]any {
		return map[string]any{"title": "Dentist", "allDay": false, "startAt": start, "endAt": end}
	}
	for name, test := range map[string]struct {
		event      map[string]any
		move       move
		start, end string
	}{
		"an hour later":              {timed("2026-10-19T13:00:00Z", "2026-10-19T13:45:00Z"), move{Hours: ptr(1)}, "2026-10-19T16:00:00+02:00", "2026-10-19T16:45:00+02:00"},
		"tomorrow, same time":        {timed("2026-10-19T13:00:00Z", "2026-10-19T13:45:00Z"), move{ToDate: ptr("2026-10-17")}, "2026-10-17T15:00:00+02:00", "2026-10-17T15:45:00+02:00"},
		"to Friday at 09:00":         {timed("2026-10-19T13:00:00Z", "2026-10-19T13:45:00Z"), move{ToDate: ptr("2026-10-30"), ToTime: ptr("09:00")}, "2026-10-30T09:00:00+01:00", "2026-10-30T09:45:00+01:00"},
		"23:30 plus an hour":         {timed("2026-10-16T21:30:00Z", "2026-10-16T22:15:00Z"), move{Hours: ptr(1)}, "2026-10-17T00:30:00+02:00", "2026-10-17T01:15:00+02:00"},
		"a day across the change":    {timed("2026-10-24T08:00:00Z", "2026-10-24T09:30:00Z"), move{Days: ptr(1)}, "2026-10-25T10:00:00+01:00", "2026-10-25T11:30:00+01:00"},
		"an hour across the change":  {timed("2026-10-24T23:30:00Z", "2026-10-25T00:00:00Z"), move{Hours: ptr(1)}, "2026-10-25T02:30:00+02:00", "2026-10-25T02:00:00+01:00"},
		"two hours across it":        {timed("2026-10-24T23:30:00Z", "2026-10-25T00:00:00Z"), move{Hours: ptr(2)}, "2026-10-25T02:30:00+01:00", "2026-10-25T03:00:00+01:00"},
		"all day, two days later":    {map[string]any{"title": "Trip", "allDay": true, "startDate": "2026-10-24", "endDate": "2026-10-25"}, move{Days: ptr(2)}, "2026-10-26", "2026-10-27"},
		"all day, to a date":         {map[string]any{"title": "Trip", "allDay": true, "startDate": "2026-10-24", "endDate": "2026-10-25"}, move{ToDate: ptr("2026-11-01")}, "2026-11-01", "2026-11-02"},
		"all day, one day, tomorrow": {map[string]any{"title": "Birthday", "allDay": true, "startDate": "2026-10-16", "endDate": "2026-10-16"}, move{ToDate: ptr("2026-10-17")}, "2026-10-17", "2026-10-17"},
	} {
		body, err := mustPlan(t, test.move).event(test.event)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		startField, endField := "startAt", "endAt"
		if allDay, _ := test.event["allDay"].(bool); allDay {
			startField, endField = "startDate", "endDate"
		}
		if body[startField] != test.start || body[endField] != test.end || len(body) != 2 {
			t.Errorf("%s: body = %v, want %s %s – %s", name, body, startField, test.start, test.end)
		}
	}
	allDay := map[string]any{"title": "Trip", "allDay": true, "startDate": "2026-10-24", "endDate": "2026-10-25"}
	for name, m := range map[string]move{"an hour": {Hours: ptr(1)}, "a time": {ToTime: ptr("10:00")}, "no time": {DateOnly: ptr(true)}} {
		if _, err := mustPlan(t, m).event(allDay); !errors.As(err, new(refusedMove)) {
			t.Errorf("all-day event moved by %s: err = %v", name, err)
		}
	}
	if _, err := mustPlan(t, move{DateOnly: ptr(true)}).event(timed("2026-10-19T13:00:00Z", "2026-10-19T13:45:00Z")); !errors.As(err, new(refusedMove)) || !strings.Contains(err.Error(), "only a Task can drop its time") {
		t.Errorf("timed event without a time: err = %v", err)
	}
}

func TestTaskDetailsSayWhenItIsDueOnTheOwnersClock(t *testing.T) {
	zone, now := berlin(t), mustTime(t, "2026-10-16T08:00:00Z")
	timed := taskDetails(map[string]any{"id": "t", "title": "Call the vet", "completed": false, "dueAt": "2026-10-19T13:30:00Z", "createdAt": "2026-10-10T07:00:00Z", "recurrence": map[string]any{"frequency": "weekly", "interval": 2.0}}, zone, now)
	due := timed["due"].(map[string]any)
	if due["date"] != "2026-10-19" || due["weekday"] != "Monday" || due["hasTime"] != true || due["time"] != "15:30" || due["local"] != "2026-10-19T15:30:00+02:00" || due["utc"] != "2026-10-19T13:30:00Z" || due["overdue"] != false || due["text"] != "Mon 19 Oct 2026, 15:30 CEST" {
		t.Fatalf("timed due = %#v", due)
	}
	if timed["timeZone"] != "Europe/Berlin" || timed["createdAtLocal"] != "2026-10-10T09:00:00+02:00" || timed["dueAt"] != "2026-10-19T13:30:00Z" || timed["recurring"] != true || timed["recurrenceText"] != "every 2 weeks" {
		t.Fatalf("timed details = %#v", timed)
	}

	dateOnly := taskDetails(map[string]any{"title": "Renew passport", "completed": false, "dueAt": "2026-10-14T22:00:00Z"}, zone, now)["due"].(map[string]any)
	if dateOnly["date"] != "2026-10-15" || dateOnly["hasTime"] != false || dateOnly["overdue"] != true || dateOnly["text"] != "Thu 15 Oct 2026 (no time)" {
		t.Fatalf("date-only due = %#v", dateOnly)
	}
	if _, has := dateOnly["time"]; has {
		t.Fatalf("a date-only due date has a time: %#v", dateOnly)
	}
	// Due today without a time is not overdue yet; a past time is.
	if today := taskDetails(map[string]any{"completed": false, "dueAt": "2026-10-15T22:00:00Z"}, zone, now)["due"].(map[string]any); today["overdue"] != false {
		t.Fatalf("today = %#v", today)
	}
	if earlier := taskDetails(map[string]any{"completed": false, "dueAt": "2026-10-16T07:00:00Z"}, zone, now)["due"].(map[string]any); earlier["overdue"] != true || earlier["hasTime"] != true {
		t.Fatalf("earlier today = %#v", earlier)
	}
	done := taskDetails(map[string]any{"completed": true, "completedAt": "2026-10-16T06:00:00Z", "dueAt": "2026-10-14T22:00:00Z"}, zone, now)
	if _, has := done["due"].(map[string]any)["overdue"]; has || done["completedAtLocal"] != "2026-10-16T08:00:00+02:00" {
		t.Fatalf("completed = %#v", done)
	}
	if unscheduled := taskDetails(map[string]any{"title": "Someday"}, zone, now); unscheduled["due"] != nil || unscheduled["recurring"] != false {
		t.Fatalf("unscheduled = %#v", unscheduled)
	}
}

func TestEventDetailsSayWhenItIsOnTheOwnersClock(t *testing.T) {
	zone := berlin(t)
	timed := eventDetails(map[string]any{"title": "Dentist", "allDay": false, "startAt": "2026-10-23T13:00:00Z", "endAt": "2026-10-23T14:00:00Z", "location": "Dr. Weber"}, zone)
	start := timed["start"].(map[string]any)
	if start["date"] != "2026-10-23" || start["weekday"] != "Friday" || start["time"] != "15:00" || start["local"] != "2026-10-23T15:00:00+02:00" || start["utc"] != "2026-10-23T13:00:00Z" {
		t.Fatalf("start = %#v", start)
	}
	if timed["end"].(map[string]any)["time"] != "16:00" || timed["durationMinutes"] != 60 || timed["when"] != "Fri 23 Oct 2026, 15:00–16:00 CEST" || timed["recurring"] != false || timed["location"] != "Dr. Weber" {
		t.Fatalf("timed = %#v", timed)
	}
	overnight := eventDetails(map[string]any{"title": "Party", "allDay": false, "startAt": "2026-10-24T20:00:00Z", "endAt": "2026-10-25T02:00:00Z"}, zone)
	if overnight["when"] != "Sat 24 Oct 2026, 22:00 CEST – Sun 25 Oct 2026, 03:00 CET" || overnight["durationMinutes"] != 360 {
		t.Fatalf("overnight = %#v", overnight)
	}
	allDay := eventDetails(map[string]any{"title": "Trip", "allDay": true, "startDate": "2026-10-24", "endDate": "2026-10-26"}, zone)
	if allDay["days"] != 3 || allDay["when"] != "Sat 24 Oct 2026 – Mon 26 Oct 2026 (all day)" || allDay["start"].(map[string]any)["date"] != "2026-10-24" || allDay["end"].(map[string]any)["weekday"] != "Monday" {
		t.Fatalf("all day = %#v", allDay)
	}
	occurrence := eventDetails(map[string]any{"title": "Gym", "allDay": false, "startAt": "2026-10-17T05:00:00Z", "endAt": "2026-10-17T06:00:00Z",
		"recurrence": map[string]any{"frequency": "weekly", "interval": 1.0, "byWeekday": []any{"TU", "SA"}, "until": "2026-12-31T23:00:00Z"},
		"occurrence": map[string]any{"seriesId": "s", "recurrenceId": "2026-10-17T05:00:00Z", "modified": false}}, zone)
	if occurrence["recurring"] != true || occurrence["recurrenceText"] != "every week on TU, SA, until Fri 1 Jan 2027" {
		t.Fatalf("occurrence = %#v", occurrence)
	}
}
