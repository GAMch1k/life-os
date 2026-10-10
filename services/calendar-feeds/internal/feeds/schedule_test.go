package feeds

import (
	"strings"
	"testing"
	"time"
)

var (
	berlin, _ = time.LoadLocation("Europe/Berlin")
	// now is Saturday 10 October 2026, 12:00 in Berlin (CEST).
	now = time.Date(2026, time.October, 10, 10, 0, 0, 0, time.UTC)
)

func TestScheduleAndBackoff(t *testing.T) {
	schedule, err := ParseSchedule("04:30", berlin)
	if err != nil {
		t.Fatal(err)
	}
	// 04:30 CEST is 02:30 UTC; after the change on 25 October, 03:30 UTC.
	for after, want := range map[string]string{
		"2026-10-10T10:00:00Z": "2026-10-11T02:30:00Z",
		"2026-10-11T02:29:00Z": "2026-10-11T02:30:00Z",
		"2026-10-11T02:30:00Z": "2026-10-12T02:30:00Z",
		"2026-10-24T12:00:00Z": "2026-10-25T03:30:00Z",
	} {
		from, _ := time.Parse(time.RFC3339, after)
		if got := schedule.Next(from).Format(time.RFC3339); got != want {
			t.Errorf("Next(%s) = %s, want %s", after, got, want)
		}
	}
	for failures, want := range map[int]time.Duration{1: 15 * time.Minute, 2: 30 * time.Minute, 3: time.Hour, 5: 4 * time.Hour, 6: 6 * time.Hour, 20: 6 * time.Hour} {
		if got := backoff(failures); got != want {
			t.Errorf("backoff(%d) = %v, want %v", failures, got, want)
		}
	}
	// Never later than the next daily slot.
	late := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	if got := schedule.retryAt(late, 10); !got.Equal(time.Date(2026, 10, 11, 2, 30, 0, 0, time.UTC)) {
		t.Errorf("retryAt = %v", got)
	}
	if got := schedule.retryAt(now, 1); !got.Equal(now.Add(15 * time.Minute)) {
		t.Errorf("first retry = %v", got)
	}
	for _, bad := range []string{"4:30", "24:00", "04:60", "", "noon"} {
		if _, err := ParseSchedule(bad, berlin); err == nil {
			t.Errorf("ParseSchedule(%q) accepted", bad)
		}
	}
}

func TestSourceActor(t *testing.T) {
	for name, want := range map[string]string{
		"ZAW": "ics-zaw", "Müllabfuhr Großstadt": "ics-muellabfuhr-grossstadt", "  Public holidays (DE) ": "ics-public-holidays-de",
		"日本": "ics-source", strings.Repeat("a", 80): "ics-" + strings.Repeat("a", 40),
	} {
		if got := SourceActor(name); got.Kind != "system" || got.Name != want {
			t.Errorf("SourceActor(%q) = %+v, want system:%s", name, got, want)
		}
	}
}
