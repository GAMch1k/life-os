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

func TestParseClock(t *testing.T) {
	for value, want := range map[string]Clock{"08:00": {8, 0}, "00:00": {0, 0}, "23:59": {23, 59}, "07:30": {7, 30}} {
		if got, err := ParseClock(value); err != nil || got != want || got.String() != value {
			t.Errorf("ParseClock(%q) = %v, %v", value, got, err)
		}
	}
	for _, value := range []string{"", "8:00", "24:00", "08:60", "08:00:00", "8am", " 08:00"} {
		if _, err := ParseClock(value); err == nil {
			t.Errorf("ParseClock(%q) accepted", value)
		}
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
		{"2026-10-25", Clock{8, 0}, "2026-10-25T07:00:00Z"},
	}
	for _, c := range cases {
		if got := wallTime(date(c.day), c.clock, berlin); !got.Equal(instant(c.want)) {
			t.Errorf("wallTime(%s %s) = %s, want %s", c.day, c.clock, got.UTC().Format(time.RFC3339), c.want)
		}
	}
}

func keys(due []Due) []string {
	out := make([]string, 0, len(due))
	for _, item := range due {
		out = append(out, item.Key+"@"+item.At.UTC().Format(time.RFC3339))
	}
	return out
}

func TestTheExpiryReminderIsDueDailyAtTheOwnersWallClock(t *testing.T) {
	// 08:00 in Berlin is 06:00 UTC in summer time and 07:00 UTC in winter
	// time; the day the clocks go back has its reminder at 08:00 CET.
	got := dueBetween(Clock{8, 0}, berlin, instant("2026-10-23T22:00:00Z"), instant("2026-10-26T22:00:00Z"))
	want := []string{
		"food:expiry:2026-10-24@2026-10-24T06:00:00Z",
		"food:expiry:2026-10-25@2026-10-25T07:00:00Z",
		"food:expiry:2026-10-26@2026-10-26T07:00:00Z",
	}
	if !reflect.DeepEqual(keys(got), want) {
		t.Fatalf("due = %v, want %v", keys(got), want)
	}
	// And forward in spring.
	if got := dueBetween(Clock{8, 0}, berlin, instant("2026-03-28T00:00:00Z"), instant("2026-03-29T12:00:00Z")); !reflect.DeepEqual(keys(got), []string{
		"food:expiry:2026-03-28@2026-03-28T07:00:00Z",
		"food:expiry:2026-03-29@2026-03-29T06:00:00Z",
	}) {
		t.Fatalf("spring = %v", keys(got))
	}
	// The window is (from, to]: a reminder due exactly at from is not in it,
	// one due exactly at to is.
	if got := dueBetween(Clock{8, 0}, berlin, instant("2026-10-10T06:00:00Z"), instant("2026-10-11T06:00:00Z")); !reflect.DeepEqual(keys(got), []string{"food:expiry:2026-10-11@2026-10-11T06:00:00Z"}) {
		t.Fatalf("window = %v", keys(got))
	}
	// The owner's date names the reminder, also west of UTC: 08:00 on 10
	// October in New York is 12:00 UTC.
	if got := dueBetween(Clock{8, 0}, newYork, instant("2026-10-10T11:00:00Z"), instant("2026-10-10T13:00:00Z")); !reflect.DeepEqual(keys(got), []string{"food:expiry:2026-10-10@2026-10-10T12:00:00Z"}) {
		t.Fatalf("New York = %v", keys(got))
	}
	if got := dueBetween(Clock{8, 0}, berlin, instant("2026-10-10T06:30:00Z"), instant("2026-10-11T05:59:00Z")); len(got) != 0 {
		t.Fatalf("nothing due between two reminders, got %v", keys(got))
	}
}
