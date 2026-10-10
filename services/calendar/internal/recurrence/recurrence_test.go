package recurrence

import (
	"strings"
	"testing"
	"time"

	"github.com/teambition/rrule-go"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func wall(value string) time.Time {
	parsed, err := time.Parse("2006-01-02T15:04", value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func instant(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func starts(occurrences []Occurrence) string {
	var out []string
	for _, occurrence := range occurrences {
		out = append(out, occurrence.Start.UTC().Format("2006-01-02T15:04Z"))
	}
	return strings.Join(out, " ")
}

// A weekly 09:00 Berlin meeting stays at 09:00 local when Berlin leaves
// summer time on 25 October 2026: 07:00 UTC before, 08:00 UTC after.
func TestTimedSeriesKeepsItsWallClockAcrossDST(t *testing.T) {
	rule, err := SimpleRule("weekly", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	series := Series{Rule: rule, Start: wall("2026-10-12T09:00"), Duration: time.Hour, Zone: LocationZone{mustLoad(t, "Europe/Berlin")}}
	got, err := series.Between(instant("2026-10-01T00:00:00Z"), instant("2026-11-03T00:00:00Z"), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "2026-10-12T07:00Z 2026-10-19T07:00Z 2026-10-26T08:00Z 2026-11-02T08:00Z"; starts(got) != want {
		t.Fatalf("starts = %s, want %s", starts(got), want)
	}
	if !got[2].End.Equal(got[2].Start.Add(time.Hour)) {
		t.Errorf("duration not kept: %+v", got[2])
	}
	// Spring forward: 02:30 does not exist on 28 March 2027 in Berlin; the
	// occurrence still exists and lands on the shifted clock.
	daily, _ := SimpleRule("daily", 1, nil)
	night := Series{Rule: daily, Start: wall("2027-03-27T02:30"), Duration: 30 * time.Minute, Zone: LocationZone{mustLoad(t, "Europe/Berlin")}}
	nights, _ := night.Between(instant("2027-03-27T00:00:00Z"), instant("2027-03-30T00:00:00Z"), 0, nil)
	if len(nights) != 3 || nights[0].Start.Format(time.RFC3339) != "2027-03-27T01:30:00Z" || nights[2].Start.Format(time.RFC3339) != "2027-03-29T00:30:00Z" {
		t.Fatalf("spring-forward nights = %s", starts(nights))
	}
}

func TestMonthlySeriesSkipsMonthsWithoutTheDay(t *testing.T) {
	rule, _ := SimpleRule("monthly", 1, nil)
	series := Series{Rule: rule, Start: wall("2027-01-31T10:00"), Duration: time.Hour, Zone: LocationZone{time.UTC}}
	got, _ := series.Between(instant("2027-01-01T00:00:00Z"), instant("2027-06-01T00:00:00Z"), 0, nil)
	if want := "2027-01-31T10:00Z 2027-03-31T10:00Z 2027-05-31T10:00Z"; starts(got) != want {
		t.Fatalf("starts = %s, want %s", starts(got), want)
	}
}

func TestUntilIsInclusiveAndIntervalsCount(t *testing.T) {
	until := instant("2026-10-24T08:00:00Z")
	rule, _ := SimpleRule("daily", 2, &until)
	series := Series{Rule: rule, Start: wall("2026-10-20T08:00"), Duration: time.Hour, Zone: LocationZone{time.UTC}}
	got, _ := series.Between(instant("2026-01-01T00:00:00Z"), instant("2027-01-01T00:00:00Z"), 0, nil)
	if want := "2026-10-20T08:00Z 2026-10-22T08:00Z 2026-10-24T08:00Z"; starts(got) != want {
		t.Fatalf("starts = %s, want %s", starts(got), want)
	}
	// A wall-clock bound (an ICS UNTIL without a zone) works the same way.
	untilWall := wall("2026-10-22T08:00")
	series.Rule = Rule{Option: rrule.ROption{Freq: rrule.DAILY, Interval: 2}, UntilWall: &untilWall}
	got, _ = series.Between(instant("2026-01-01T00:00:00Z"), instant("2027-01-01T00:00:00Z"), 0, nil)
	if want := "2026-10-20T08:00Z 2026-10-22T08:00Z"; starts(got) != want {
		t.Fatalf("wall-bounded starts = %s, want %s", starts(got), want)
	}
	if _, err := SimpleRule("hourly", 1, nil); err == nil {
		t.Error("hourly is not an API frequency")
	}
}

// All-day occurrences are dates; their span is taken in the zone's days, and
// an occurrence that started before the range but still runs is included.
func TestAllDaySeriesOverlapsByTheZonesDays(t *testing.T) {
	rule, _ := SimpleRule("yearly", 1, nil)
	berlin := mustLoad(t, "Europe/Berlin")
	series := Series{Rule: rule, AllDay: true, Start: wall("2020-12-24T00:00"), Days: 3, Zone: LocationZone{berlin}}
	got, _ := series.Between(instant("2026-12-25T12:00:00Z"), instant("2026-12-26T00:00:00Z"), 0, nil)
	if len(got) != 1 || got[0].Wall.Format(time.DateOnly) != "2026-12-24" || got[0].LastDay.Format(time.DateOnly) != "2026-12-26" {
		t.Fatalf("occurrences = %+v", got)
	}
	if got[0].Start.Format(time.RFC3339) != "2026-12-23T23:00:00Z" || got[0].End.Format(time.RFC3339) != "2026-12-26T23:00:00Z" {
		t.Fatalf("span = %v .. %v", got[0].Start, got[0].End)
	}
	// After the last day, Berlin time, nothing is left.
	if after, _ := series.Between(instant("2026-12-26T23:00:00Z"), instant("2026-12-27T00:00:00Z"), 0, nil); len(after) != 0 {
		t.Fatalf("after the last day: %+v", after)
	}
}

func TestBetweenHonoursLimitAndFilter(t *testing.T) {
	rule, _ := SimpleRule("daily", 1, nil)
	series := Series{Rule: rule, Start: wall("2026-10-01T12:00"), Duration: time.Hour, Zone: LocationZone{time.UTC}}
	after := instant("2026-10-05T12:00:00Z")
	got, _ := series.Between(instant("2026-10-01T00:00:00Z"), instant("2026-11-01T00:00:00Z"), 2, func(o Occurrence) bool { return o.Start.After(after) })
	if want := "2026-10-06T12:00Z 2026-10-07T12:00Z"; starts(got) != want {
		t.Fatalf("starts = %s, want %s", starts(got), want)
	}
	// COUNT and BYDAY from a full rule.
	full := Series{Rule: Rule{Option: rrule.ROption{Freq: rrule.WEEKLY, Count: 3, Byweekday: []rrule.Weekday{rrule.MO, rrule.TH}}}, Start: wall("2026-10-12T06:00"), Duration: time.Hour, Zone: LocationZone{time.UTC}}
	got, _ = full.Between(instant("2026-01-01T00:00:00Z"), instant("2027-01-01T00:00:00Z"), 0, nil)
	if want := "2026-10-12T06:00Z 2026-10-15T06:00Z 2026-10-19T06:00Z"; starts(got) != want {
		t.Fatalf("starts = %s, want %s", starts(got), want)
	}
}

func TestIterationIsBounded(t *testing.T) {
	rule, _ := SimpleRule("daily", 1, nil)
	series := Series{Rule: rule, Start: wall("1700-01-01T00:00"), Duration: time.Hour, Zone: LocationZone{time.UTC}}
	got, err := series.Between(instant("2026-01-01T00:00:00Z"), instant("2026-01-02T00:00:00Z"), 0, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("a rule starting centuries ago stops after %d steps: %v %v", MaxSteps, got, err)
	}
}

// The API's rule: a weekly rule on several days, a count, and a yearly
// rule on the start's date ("annually on September 17").
func TestSpecRules(t *testing.T) {
	berlin := mustLoad(t, "Europe/Berlin")
	rule, err := Spec{Frequency: "weekly", Interval: 1, ByWeekday: []string{"TH", "MO", "MO"}, Count: 5}.Rule()
	if err != nil {
		t.Fatal(err)
	}
	// Monday 07:00 Berlin, across the end of summer time on 25 October.
	series := Series{Rule: rule, Start: wall("2026-10-19T07:00"), Duration: time.Hour, Zone: LocationZone{berlin}}
	got, _ := series.Between(instant("2026-01-01T00:00:00Z"), instant("2027-01-01T00:00:00Z"), 0, nil)
	if want := "2026-10-19T05:00Z 2026-10-22T05:00Z 2026-10-26T06:00Z 2026-10-29T06:00Z 2026-11-02T06:00Z"; starts(got) != want {
		t.Fatalf("weekly on Monday and Thursday, 5 times = %s, want %s", starts(got), want)
	}
	every2, _ := Spec{Frequency: "weekly", Interval: 2, ByWeekday: []string{"MO", "WE"}}.Rule()
	series = Series{Rule: every2, Start: wall("2026-10-12T10:00"), Duration: time.Hour, Zone: LocationZone{time.UTC}}
	got, _ = series.Between(instant("2026-10-01T00:00:00Z"), instant("2026-11-01T00:00:00Z"), 0, nil)
	if want := "2026-10-12T10:00Z 2026-10-14T10:00Z 2026-10-26T10:00Z 2026-10-28T10:00Z"; starts(got) != want {
		t.Fatalf("every other week on Monday and Wednesday = %s, want %s", starts(got), want)
	}
	birthday, _ := Spec{Frequency: "yearly", Interval: 1}.Rule()
	series = Series{Rule: birthday, AllDay: true, Start: wall("1990-09-17T00:00"), Days: 1, Zone: LocationZone{berlin}}
	got, _ = series.Between(instant("2026-01-01T00:00:00Z"), instant("2028-12-31T00:00:00Z"), 0, nil)
	if len(got) != 3 || got[0].Wall.Format(time.DateOnly) != "2026-09-17" || got[2].Wall.Format(time.DateOnly) != "2028-09-17" {
		t.Fatalf("annually on September 17 = %+v", got)
	}
	for name, spec := range map[string]Spec{
		"weekdays on a monthly rule": {Frequency: "monthly", Interval: 1, ByWeekday: []string{"MO"}},
		"an unknown weekday":         {Frequency: "weekly", Interval: 1, ByWeekday: []string{"XX"}},
		"a negative count":           {Frequency: "daily", Interval: 1, Count: -1},
	} {
		if _, err := spec.Rule(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if codes, ok := NormalizeWeekdays([]string{"SU", "MO", "SU", "WE"}); !ok || strings.Join(codes, ",") != "MO,WE,SU" {
		t.Errorf("normalized = %v %v", codes, ok)
	}
	if WeekdayCode(time.Saturday) != "SA" {
		t.Errorf("Saturday = %q", WeekdayCode(time.Saturday))
	}
}
