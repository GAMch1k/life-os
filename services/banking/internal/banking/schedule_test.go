package banking

import (
	"strings"
	"testing"
	"time"
)

func berlin(t *testing.T) *time.Location {
	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func TestParseSyncTimes(t *testing.T) {
	minutes, err := ParseSyncTimes(" 23:00,06:00 ,12:00,18:00")
	if err != nil || len(minutes) != 4 || minutes[0] != 360 || minutes[3] != 1380 {
		t.Fatalf("ParseSyncTimes = %v, %v", minutes, err)
	}
	for _, value := range []string{"", "6:0", "24:00", "06:00,06:00", "06:00,09:00,12:00,15:00,18:00", "noon"} {
		if _, err := ParseSyncTimes(value); err == nil {
			t.Errorf("ParseSyncTimes(%q) accepted", value)
		}
	}
	if got := strings.Join((Schedule{Minutes: minutes}).Times(), ","); got != "06:00,12:00,18:00,23:00" {
		t.Errorf("Times = %s", got)
	}
}

func TestNextIsTheNextSyncTimeInTheOwnersTimezone(t *testing.T) {
	location := berlin(t)
	minutes, _ := ParseSyncTimes(DefaultSyncTimes)
	schedule := Schedule{Minutes: minutes, Location: location}
	at := func(value string) time.Time {
		parsed, err := time.ParseInLocation("2006-01-02 15:04", value, location)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	for _, tc := range []struct{ now, want string }{
		{"2026-10-10 05:59", "2026-10-10 06:00"},
		{"2026-10-10 06:00", "2026-10-10 12:00"},
		{"2026-10-10 23:30", "2026-10-11 06:00"},
		// The clocks go back on 25 Oct 2026: still 06:00 local.
		{"2026-10-24 23:30", "2026-10-25 06:00"},
	} {
		if got := schedule.Next(at(tc.now)); !got.Equal(at(tc.want)) {
			t.Errorf("Next(%s) = %s, want %s", tc.now, got.In(location), tc.want)
		}
	}
	if got := schedule.FirstTomorrow(at("2026-10-10 23:30")); !got.Equal(at("2026-10-11 06:00")) {
		t.Errorf("FirstTomorrow = %s", got)
	}
	// 23:30 UTC on the 10th is already the 11th in Berlin.
	if day := schedule.Day(time.Date(2026, 10, 10, 23, 30, 0, 0, time.UTC)); day.Format(time.DateOnly) != "2026-10-11" {
		t.Errorf("Day = %s", day)
	}
}

func TestRetriesBackOffWithinTheSchedule(t *testing.T) {
	location := berlin(t)
	minutes, _ := ParseSyncTimes(DefaultSyncTimes)
	schedule := Schedule{Minutes: minutes, Location: location}
	now := time.Date(2026, 10, 10, 7, 0, 0, 0, location)
	for failures, want := range map[int]time.Duration{1: 15 * time.Minute, 2: 30 * time.Minute, 3: time.Hour, 4: 2 * time.Hour, 9: 2 * time.Hour} {
		if got := schedule.bankRetry(now, failures).Sub(now); got != want {
			t.Errorf("bankRetry(%d) = %s, want %s", failures, got, want)
		}
	}
	late := time.Date(2026, 10, 10, 11, 50, 0, 0, location)
	if got := schedule.bankRetry(late, 3); !got.Equal(time.Date(2026, 10, 10, 12, 0, 0, 0, location)) {
		t.Errorf("a retry never waits past the next sync time: %s", got)
	}
	if got := schedule.rateLimitRetry(now); !got.Equal(time.Date(2026, 10, 10, 18, 0, 0, 0, location)) {
		t.Errorf("rateLimitRetry = %s", got)
	}
	if got := financeRetry(now, 1).Sub(now); got != time.Minute {
		t.Errorf("financeRetry(1) = %s", got)
	}
	if got := financeRetry(now, 10).Sub(now); got != 30*time.Minute {
		t.Errorf("financeRetry(10) = %s", got)
	}
}
