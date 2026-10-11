package backup

import (
	"testing"
	"time"
)

func berlin(t *testing.T) *time.Location {
	t.Helper()
	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func TestParseTime(t *testing.T) {
	for value, want := range map[string]int{"03:15": 195, "00:00": 0, "23:59": 1439, " 02:30 ": 150} {
		if got, err := ParseTime(value); err != nil || got != want {
			t.Errorf("ParseTime(%q) = %d, %v; want %d", value, got, err, want)
		}
	}
	for _, value := range []string{"", "3:15", "03:5", "24:00", "03:60", "noon", "03-15", "03:15:00"} {
		if _, err := ParseTime(value); err == nil {
			t.Errorf("ParseTime(%q) accepted", value)
		}
	}
	if got := (Schedule{Minute: 195}).Time(); got != "03:15" {
		t.Errorf("Time = %s", got)
	}
}

func TestNextFollowsTheOwnersWallClockAcrossDaylightSavingTime(t *testing.T) {
	location := berlin(t)
	schedule := Schedule{Minute: 195, Location: location} // 03:15
	at := func(value string) time.Time {
		t.Helper()
		parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, location)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	for _, tc := range []struct{ now, want string }{
		{"2026-10-11 03:00:00", "2026-10-11 03:15:00"},
		{"2026-10-11 03:15:00", "2026-10-12 03:15:00"},
		{"2026-10-11 23:59:00", "2026-10-12 03:15:00"},
		// The clocks go back at 03:00 on 25 October: still 03:15 local, 25 hours later.
		{"2026-10-24 04:00:00", "2026-10-25 03:15:00"},
		// They go forward at 02:00 on 29 March: still 03:15 local, 23 hours later.
		{"2026-03-28 04:00:00", "2026-03-29 03:15:00"},
	} {
		if got := schedule.Next(at(tc.now)); !got.Equal(at(tc.want)) {
			t.Errorf("Next(%s) = %s, want %s", tc.now, got, tc.want)
		}
	}
	autumn := schedule.Next(at("2026-10-25 03:15:00")).Sub(schedule.Next(at("2026-10-24 04:00:00")))
	if autumn != 24*time.Hour {
		t.Errorf("25 to 26 October: %s between runs, want 24h", autumn)
	}
	if got := schedule.Next(at("2026-10-24 04:00:00")).UTC(); !got.Equal(time.Date(2026, 10, 25, 2, 15, 0, 0, time.UTC)) {
		t.Errorf("25 October 03:15 CET = %s, want 02:15 UTC", got)
	}
	if got := schedule.Previous(at("2026-10-11 03:14:59")); !got.Equal(at("2026-10-10 03:15:00")) {
		t.Errorf("Previous before the time = %s", got)
	}
	if got := schedule.Previous(at("2026-10-11 03:15:00")); !got.Equal(at("2026-10-11 03:15:00")) {
		t.Errorf("Previous at the time = %s", got)
	}
}

func TestATimeTheClocksSkipOrRepeatRunsOnce(t *testing.T) {
	location := berlin(t)
	schedule := Schedule{Minute: 150, Location: location} // 02:30
	// 02:30 does not exist on 29 March: the run is when the clocks jump, 03:00 CEST.
	if got := schedule.On(2026, time.March, 29); !got.Equal(time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC)) {
		t.Errorf("29 March = %s, want 01:00 UTC (03:00 CEST)", got.UTC())
	}
	// 02:30 happens twice on 25 October: the run is the first, 02:30 CEST.
	first := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)
	if got := schedule.On(2026, time.October, 25); !got.Equal(first) {
		t.Errorf("25 October = %s, want 00:30 UTC (02:30 CEST)", got.UTC())
	}
	// After the first 02:30, the second one (02:30 CET) is not a run.
	if got := schedule.Next(first); !got.Equal(time.Date(2026, 10, 26, 1, 30, 0, 0, time.UTC)) {
		t.Errorf("Next after the first 02:30 = %s, want 26 October 02:30 CET", got.UTC())
	}
	if got := schedule.Next(time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC)) {
		t.Errorf("Next into the gap = %s", got.UTC())
	}
	// A run time on a normal day is unaffected; a schedule without a zone is UTC.
	if got := schedule.On(2026, time.October, 11); !got.Equal(time.Date(2026, 10, 11, 0, 30, 0, 0, time.UTC)) {
		t.Errorf("11 October = %s", got.UTC())
	}
	if got := (Schedule{Minute: 150}).On(2026, time.March, 29); !got.Equal(time.Date(2026, 3, 29, 2, 30, 0, 0, time.UTC)) {
		t.Errorf("UTC schedule = %s", got)
	}
}

func TestCatchUpIsDueWhenTheLastScheduledRunWasMissed(t *testing.T) {
	location := berlin(t)
	schedule := Schedule{Minute: 195, Location: location}
	at := func(day, hour, minute int) time.Time {
		return time.Date(2026, 10, day, hour, minute, 0, 0, location)
	}
	for _, tc := range []struct {
		name string
		last time.Time
		now  time.Time
		want bool
	}{
		{"no successful run yet", time.Time{}, at(11, 15, 0), true},
		{"today's run happened", at(11, 3, 15), at(11, 15, 0), false},
		{"today's run was missed", at(10, 3, 15), at(11, 4, 0), true},
		{"before today's time, yesterday's run happened", at(10, 3, 15), at(11, 3, 0), false},
		{"before today's time, yesterday's run was missed", at(9, 3, 15), at(11, 3, 0), true},
		{"a manual full run an hour before stands in", at(11, 2, 30), at(11, 4, 0), false},
		{"a manual full run long before does not", at(11, 2, 0), at(11, 4, 0), true},
		{"down for days", at(1, 3, 15), at(11, 12, 0), true},
	} {
		if got := schedule.CatchUpDue(tc.last, tc.now); got != tc.want {
			t.Errorf("%s: CatchUpDue = %v, want %v", tc.name, got, tc.want)
		}
	}
}
