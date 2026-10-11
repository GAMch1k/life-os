package backup

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Default retention: the newest successful full run of each of the last 7
// days, 4 ISO weeks, and 12 months that have one; failed runs for 3 days.
const (
	DefaultKeepDaily      = 7
	DefaultKeepWeekly     = 4
	DefaultKeepMonthly    = 12
	DefaultKeepFailedDays = 3
)

// Retention is the rotation policy (grandfather-father-son).
type Retention struct {
	Daily      int
	Weekly     int
	Monthly    int
	FailedDays int
}

// RunInfo is what the rotation needs to know about a completed run.
type RunInfo struct {
	ID        string
	StartedAt time.Time
	Result    Result
	Scope     Scope
}

// Expired lists the runs the rotation deletes, given every completed run
// (in any order) at now. Days, ISO weeks, and months are those of location.
//
//   - A successful full run is kept while it is the newest of its day among
//     the last Daily days that have one, of its week among the last Weekly
//     weeks, or of its month among the last Monthly months.
//   - A successful run of only some databases never takes such a place: it
//     is kept for Daily days.
//   - A failed run is kept for FailedDays days.
//   - The newest successful run, and the newest successful full run, are
//     always kept.
func (r Retention) Expired(runs []RunInfo, now time.Time, location *time.Location) []string {
	if location == nil {
		location = time.UTC
	}
	sorted := sortNewestFirst(runs)
	keep := map[string]bool{}

	var full []RunInfo
	newestSuccess, newestFull := "", ""
	for _, run := range sorted {
		if run.Result != ResultSucceeded {
			continue
		}
		if newestSuccess == "" {
			newestSuccess = run.ID
		}
		if run.Scope == ScopeAll {
			if newestFull == "" {
				newestFull = run.ID
			}
			full = append(full, run)
		}
	}
	if newestSuccess != "" {
		keep[newestSuccess] = true
	}
	if newestFull != "" {
		keep[newestFull] = true
	}

	keepNewestPerBucket(full, r.Daily, keep, func(t time.Time) string {
		return t.In(location).Format(time.DateOnly)
	})
	keepNewestPerBucket(full, r.Weekly, keep, func(t time.Time) string {
		year, week := t.In(location).ISOWeek()
		return fmt.Sprintf("%04d-W%02d", year, week)
	})
	keepNewestPerBucket(full, r.Monthly, keep, func(t time.Time) string {
		return t.In(location).Format("2006-01")
	})

	var expired []string
	for _, run := range sorted {
		if keep[run.ID] {
			continue
		}
		age := now.Sub(run.StartedAt)
		switch {
		case run.Result == ResultSucceeded && run.Scope == ScopeAll:
			expired = append(expired, run.ID)
		case run.Result == ResultSucceeded:
			if age >= days(r.Daily) {
				expired = append(expired, run.ID)
			}
		default:
			if age >= days(r.FailedDays) {
				expired = append(expired, run.ID)
			}
		}
	}
	return expired
}

// keepNewestPerBucket marks the newest run of each of the first count
// buckets of runs (newest first).
func keepNewestPerBucket(runs []RunInfo, count int, keep map[string]bool, bucket func(time.Time) string) {
	last := ""
	for _, run := range runs {
		if count <= 0 {
			return
		}
		if key := bucket(run.StartedAt); key != last {
			keep[run.ID] = true
			last = key
			count--
		}
	}
}

func sortNewestFirst(runs []RunInfo) []RunInfo {
	sorted := slices.Clone(runs)
	slices.SortFunc(sorted, func(a, b RunInfo) int {
		if order := b.StartedAt.Compare(a.StartedAt); order != 0 {
			return order
		}
		return strings.Compare(b.ID, a.ID)
	})
	return sorted
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }
