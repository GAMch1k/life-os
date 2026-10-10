package notifications

import (
	"reflect"
	"testing"
	"time"
)

var (
	berlin  = mustZone("Europe/Berlin")
	newYork = mustZone("America/New_York")
)

func mustZone(name string) *time.Location {
	zone, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return zone
}

func instant(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func date(value string) time.Time {
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

// task is an open Task due at due (RFC 3339), classified for Berlin.
func task(id, title, due string) Task {
	at := instant(due)
	return Task{ID: id, Title: title, Due: at, DateOnly: IsDateOnly(at, berlin), Day: dateOf(at, berlin)}
}

func TestParseClock(t *testing.T) {
	for value, want := range map[string]Clock{"08:00": {8, 0}, "19:30": {19, 30}, "00:00": {0, 0}, "23:59": {23, 59}} {
		got, err := ParseClock(value)
		if err != nil || got != want || got.String() != value {
			t.Errorf("ParseClock(%q) = %v, %v", value, got, err)
		}
	}
	for _, value := range []string{"8:00", "24:00", "08:60", "0800", "08:00:00", "", " 08:00"} {
		if _, err := ParseClock(value); err == nil {
			t.Errorf("ParseClock(%q) accepted", value)
		}
	}
}

func TestDateOnlyDueDatesFollowTheDashboardRule(t *testing.T) {
	cases := []struct {
		due   string
		zone  *time.Location
		want  bool
		about string
	}{
		{"2026-10-19T22:00:00Z", berlin, true, "the owner's local midnight (CEST)"},
		{"2026-11-19T23:00:00Z", berlin, true, "the owner's local midnight (CET)"},
		{"2026-10-20T00:00:00Z", berlin, true, "a bare date sent as UTC midnight is that day in Berlin"},
		{"2026-10-20T00:00:00Z", newYork, false, "west of UTC that instant is the previous evening"},
		{"2026-10-20T04:00:00Z", newYork, true, "New York's local midnight"},
		{"2026-10-20T12:00:00Z", berlin, false, "a time of day"},
		{"2026-10-19T22:00:01Z", berlin, false, "a second after midnight is a time"},
		{"2026-10-20T00:00:00Z", time.UTC, true, "UTC midnight for a UTC owner"},
		{"2026-03-28T23:00:00Z", berlin, true, "midnight before the spring change (CET)"},
		{"2026-03-29T22:00:00Z", berlin, true, "midnight after the spring change (CEST)"},
		{"2026-10-24T22:00:00Z", berlin, true, "midnight before the autumn change (CEST)"},
		{"2026-10-25T23:00:00Z", berlin, true, "midnight after the autumn change (CET)"},
	}
	for _, c := range cases {
		if got := IsDateOnly(instant(c.due), c.zone); got != c.want {
			t.Errorf("%s: IsDateOnly(%s, %s) = %v", c.about, c.due, c.zone, got)
		}
	}
	// A UTC-midnight date keeps its day for the owner.
	if day := dateOf(instant("2026-10-20T00:00:00Z"), berlin); !day.Equal(date("2026-10-20")) {
		t.Errorf("day = %s", day)
	}
}

func TestWallTimesAcrossDaylightSaving(t *testing.T) {
	cases := []struct {
		day   string
		clock Clock
		want  string
	}{
		{"2026-10-10", Clock{8, 0}, "2026-10-10T06:00:00Z"},
		{"2026-12-10", Clock{8, 0}, "2026-12-10T07:00:00Z"},
		// The summer-time change: before and after it on the same day.
		{"2026-03-29", Clock{0, 0}, "2026-03-28T23:00:00Z"},
		{"2026-03-29", Clock{8, 0}, "2026-03-29T06:00:00Z"},
		// 02:30 does not exist: the same clock time after the jump.
		{"2026-03-29", Clock{2, 30}, "2026-03-29T01:30:00Z"},
		// The winter-time change: 02:30 happens twice, the first one counts.
		{"2026-10-25", Clock{2, 30}, "2026-10-25T00:30:00Z"},
		{"2026-10-25", Clock{0, 0}, "2026-10-24T22:00:00Z"},
		{"2026-10-25", Clock{9, 0}, "2026-10-25T08:00:00Z"},
		{"2026-10-25", Clock{19, 0}, "2026-10-25T18:00:00Z"},
	}
	for _, c := range cases {
		if got := wallTime(date(c.day), c.clock, berlin); !got.Equal(instant(c.want)) {
			t.Errorf("wallTime(%s %s) = %s, want %s", c.day, c.clock, got.UTC().Format(time.RFC3339), c.want)
		}
	}
}

func TestDailyMessagesAreDueAtTheOwnersWallClock(t *testing.T) {
	schedule := DefaultSchedule()
	keys := func(items []Due) []string {
		var out []string
		for _, item := range items {
			out = append(out, item.Key+"@"+item.At.UTC().Format(time.RFC3339))
		}
		return out
	}
	// One whole day (Berlin), across the autumn change: 08:00 and 09:00 are
	// CET (UTC+1) on 25 October, 19:00 too.
	got := dailyBetween(schedule, berlin, instant("2026-10-24T22:00:00Z"), instant("2026-10-25T22:59:00Z"))
	sortDue(got)
	want := []string{
		"summary:morning:2026-10-25@2026-10-25T07:00:00Z",
		"tasks:morning:2026-10-25@2026-10-25T08:00:00Z",
		"tasks:evening:2026-10-25@2026-10-25T18:00:00Z",
	}
	if !reflect.DeepEqual(keys(got), want) {
		t.Fatalf("daily = %v, want %v", keys(got), want)
	}
	for _, item := range got {
		if !item.Day.Equal(date("2026-10-25")) {
			t.Errorf("%s: day %s", item.Key, item.Day)
		}
	}
	// The window is (from, to]: a message exactly at from was due in the
	// previous run, one exactly at to is due now.
	if got := dailyBetween(schedule, berlin, instant("2026-10-10T06:00:00Z"), instant("2026-10-10T07:00:00Z")); !reflect.DeepEqual(keys(got), []string{"tasks:morning:2026-10-10@2026-10-10T07:00:00Z"}) {
		t.Errorf("boundaries = %v", keys(got))
	}
	// Several days after an outage, each day's messages once.
	if got := dailyBetween(schedule, berlin, instant("2026-10-10T05:00:00Z"), instant("2026-10-12T06:30:00Z")); len(got) != 7 {
		t.Errorf("three days = %v", keys(got))
	}
}

func TestEqualSummaryAndMorningTimesAreOneMessage(t *testing.T) {
	schedule := DefaultSchedule()
	schedule.Summary = Clock{9, 0}
	if !schedule.Merged() || DefaultSchedule().Merged() {
		t.Fatal("Merged")
	}
	got := dailyBetween(schedule, berlin, instant("2026-10-09T22:00:00Z"), instant("2026-10-10T21:59:00Z"))
	if len(got) != 2 || got[0].Kind != KindSummary || got[0].Key != "summary:morning:2026-10-10" || !got[0].At.Equal(instant("2026-10-10T07:00:00Z")) || got[1].Kind != KindEvening {
		t.Fatalf("merged = %+v", got)
	}
}

func TestTimedRemindersAreTheLeadBeforeTheDueTime(t *testing.T) {
	tasks := []Task{
		task("a", "Call mom", "2026-10-10T13:00:00Z"),       // 15:00 CEST
		task("b", "Renew passport", "2026-10-10T22:00:00Z"), // date-only (local midnight)
		task("c", "Bank", "2026-10-10T13:31:00Z"),
		task("d", "Later", "2026-10-10T13:29:00Z"),
		task("e", "At the window start", "2026-10-10T12:59:00Z"), // reminded in the previous run
	}
	got := timedBetween(tasks, 30*time.Minute, instant("2026-10-10T12:29:00Z"), instant("2026-10-10T13:00:00Z"))
	if len(got) != 2 {
		t.Fatalf("due = %+v", got)
	}
	if got[0].Key != "tasks:due:a:2026-10-10T13:00:00Z" || !got[0].At.Equal(instant("2026-10-10T12:30:00Z")) || got[0].Kind != KindDue {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Task.ID != "d" {
		t.Errorf("second = %+v", got[1])
	}
	// A date-only Task never has a timed reminder, even at 23:30 the day
	// before.
	if got := timedBetween(tasks, 30*time.Minute, instant("2026-10-10T21:00:00Z"), instant("2026-10-10T22:00:00Z")); len(got) != 0 {
		t.Errorf("date-only reminded: %+v", got)
	}
	// A new due time is a new key.
	moved := task("a", "Call mom", "2026-10-10T14:00:00Z")
	if dueKey(moved) == dueKey(tasks[0]) {
		t.Error("a moved Task keeps its key")
	}
}

func TestClassifySortsTasksIntoADaysLists(t *testing.T) {
	day, at := date("2026-10-10"), instant("2026-10-10T07:00:00Z") // 09:00 CEST
	tasks := []Task{
		task("old", "Pay rent", "2026-10-05T22:00:00Z"),           // date-only Tue 6 Oct
		task("yesterday", "Call bank", "2026-10-09T15:00:00Z"),    // timed yesterday
		task("early", "Take pills", "2026-10-10T06:30:00Z"),       // timed today 08:30, before 09:00
		task("today", "Renew passport", "2026-10-09T22:00:00Z"),   // date-only today
		task("utc", "Water plants", "2026-10-10T00:00:00Z"),       // date-only today, sent as UTC midnight
		task("later", "Call mom", "2026-10-10T13:00:00Z"),         // timed today 15:00
		task("tomorrow", "Buy gift", "2026-10-10T22:00:00Z"),      // date-only tomorrow
		task("tomorrow-timed", "Dentist", "2026-10-11T08:00:00Z"), // timed tomorrow: in none
		task("far", "Taxes", "2026-10-11T22:00:00Z"),              // date-only the day after: in none
	}
	got := classify(tasks, day, at)
	ids := func(list []Task) []string {
		var out []string
		for _, task := range list {
			out = append(out, task.ID)
		}
		return out
	}
	check := func(name string, list []Task, want ...string) {
		if !reflect.DeepEqual(ids(list), want) {
			t.Errorf("%s = %v, want %v", name, ids(list), want)
		}
	}
	check("overdue", got.Overdue, "old", "yesterday", "early")
	check("date-only today", got.DateOnlyToday, "today", "utc")
	check("timed today", got.TimedToday, "later")
	check("tomorrow", got.Tomorrow, "tomorrow")
	// A date-only Task due today is not overdue on its day, even after
	// its midnight; the next day it is.
	if next := classify(tasks, date("2026-10-11"), instant("2026-10-11T07:00:00Z")); !reflect.DeepEqual(ids(next.Overdue), []string{"old", "yesterday", "early", "today", "utc", "later"}) {
		t.Errorf("next day overdue = %v", ids(next.Overdue))
	}
}

func TestDailyKeys(t *testing.T) {
	day := date("2026-10-10")
	for kind, want := range map[Kind]string{KindMorning: "tasks:morning:2026-10-10", KindEvening: "tasks:evening:2026-10-10", KindSummary: "summary:morning:2026-10-10"} {
		if got := dailyKey(kind, day); got != want {
			t.Errorf("dailyKey(%s) = %s", kind, got)
		}
	}
}
