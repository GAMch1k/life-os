package backup

import (
	"slices"
	"testing"
	"time"
)

func run(at time.Time, result Result, scope Scope) RunInfo {
	return RunInfo{ID: at.UTC().Format(RunIDLayout), StartedAt: at, Result: result, Scope: scope}
}

func TestExpiredKeepsDailyWeeklyAndMonthlyRuns(t *testing.T) {
	location := berlin(t)
	retention := Retention{Daily: 7, Weekly: 4, Monthly: 12, FailedDays: 3}
	var runs []RunInfo
	for day := time.Date(2026, 5, 1, 3, 15, 0, 0, location); !day.After(time.Date(2026, 10, 11, 3, 15, 0, 0, location)); day = day.AddDate(0, 0, 1) {
		runs = append(runs, run(day, ResultSucceeded, ScopeAll))
	}
	at := func(month time.Month, day, hour int) time.Time {
		return time.Date(2026, month, day, hour, 15, 0, 0, location)
	}
	recentFailure := run(at(10, 10, 10), ResultFailed, ScopeAll)
	oldFailure := run(at(10, 7, 10), ResultFailed, ScopeAll)
	recentSubset := run(at(10, 6, 12), ResultSucceeded, ScopeSubset)
	oldSubset := run(at(10, 1, 12), ResultSucceeded, ScopeSubset)
	runs = append(runs, recentFailure, oldFailure, recentSubset, oldSubset)

	now := at(10, 11, 12)
	expired := retention.Expired(runs, now, location)
	var kept []string
	for _, candidate := range runs {
		if !slices.Contains(expired, candidate.ID) {
			kept = append(kept, candidate.StartedAt.In(location).Format("01-02 15"))
		}
	}
	slices.Sort(kept)
	want := []string{
		"05-31 03", "06-30 03", "07-31 03", "08-31 03", // monthly
		"09-20 03", "09-27 03", // weekly (with 10-04 and 10-11)
		"09-30 03", // monthly (September)
		"10-04 03", // weekly
		"10-05 03", "10-06 03", "10-06 12", "10-07 03", "10-08 03", "10-09 03", "10-10 03", "10-10 10", "10-11 03",
	}
	if !slices.Equal(kept, want) {
		t.Fatalf("kept %v\nwant %v", kept, want)
	}
}

func TestExpiredNeverDeletesTheNewestSuccessfulRun(t *testing.T) {
	location := time.UTC
	retention := Retention{Daily: 1, Weekly: 0, Monthly: 0, FailedDays: 1}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, location)
	full := run(now.AddDate(0, 0, -100), ResultSucceeded, ScopeAll)
	olderFull := run(now.AddDate(0, 0, -101), ResultSucceeded, ScopeAll)
	subset := run(now.AddDate(0, 0, -20), ResultSucceeded, ScopeSubset)
	failures := []RunInfo{run(now.AddDate(0, 0, -5), ResultFailed, ScopeAll), run(now.AddDate(0, 0, -2), ResultFailed, ScopeAll)}
	expired := retention.Expired(append([]RunInfo{olderFull, full, subset}, failures...), now, location)
	slices.Sort(expired)
	want := []string{olderFull.ID, failures[0].ID, failures[1].ID}
	slices.Sort(want)
	if !slices.Equal(expired, want) {
		t.Fatalf("expired %v, want %v (the newest full and the newest successful run stay)", expired, want)
	}
	if got := retention.Expired(nil, now, location); got != nil {
		t.Fatalf("no runs: %v", got)
	}
}

func TestExpiredCountsDaysThatHaveRuns(t *testing.T) {
	// The service was down for a month: the last 3 days with a run are kept,
	// not just the runs of the last 3 calendar days.
	location := time.UTC
	retention := Retention{Daily: 3, FailedDays: 3}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, location)
	var runs []RunInfo
	for day := 40; day >= 30; day-- {
		runs = append(runs, run(now.AddDate(0, 0, -day), ResultSucceeded, ScopeAll))
	}
	// Two runs on the newest day: only the later one holds its day.
	second := run(now.AddDate(0, 0, -30).Add(2*time.Hour), ResultSucceeded, ScopeAll)
	runs = append(runs, second)
	expired := retention.Expired(runs, now, location)
	if len(expired) != len(runs)-3 {
		t.Fatalf("expired %d of %d runs, want all but 3", len(expired), len(runs))
	}
	for _, id := range []string{second.ID, runs[9].ID, runs[8].ID} {
		if slices.Contains(expired, id) {
			t.Errorf("%s expired", id)
		}
	}
	if !slices.Contains(expired, runs[10].ID) {
		t.Errorf("the earlier run of the newest day was kept")
	}
}
