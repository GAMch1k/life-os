package banking

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultSyncTimes are when the scheduled syncs run, owner-local.
const DefaultSyncTimes = "06:00,12:00,18:00,23:00"

// MaxUnattendedPerDay is the most reads without the owner present a day:
// PSD2 lets banks refuse more than four.
const MaxUnattendedPerDay = 4

// Schedule is the daily sync times in the owner's timezone.
type Schedule struct {
	// Minutes are the sync times as minutes after local midnight, sorted.
	Minutes  []int
	Location *time.Location
}

// ParseSyncTimes reads BANKING_SYNC_TIMES: comma-separated HH:MM times, at
// most MaxUnattendedPerDay, each once.
func ParseSyncTimes(value string) ([]int, error) {
	var minutes []int
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		hours, mins, ok := strings.Cut(part, ":")
		h, errH := strconv.Atoi(hours)
		m, errM := strconv.Atoi(mins)
		if !ok || errH != nil || errM != nil || len(mins) != 2 || h < 0 || h > 23 || m < 0 || m > 59 {
			return nil, fmt.Errorf("BANKING_SYNC_TIMES: %q is not a time such as 06:00", part)
		}
		if slices.Contains(minutes, h*60+m) {
			return nil, fmt.Errorf("BANKING_SYNC_TIMES names %s twice", part)
		}
		minutes = append(minutes, h*60+m)
	}
	if len(minutes) == 0 {
		return nil, fmt.Errorf("BANKING_SYNC_TIMES must name at least one time")
	}
	if len(minutes) > MaxUnattendedPerDay {
		return nil, fmt.Errorf("BANKING_SYNC_TIMES names %d times; banks allow at most %d reads a day without you", len(minutes), MaxUnattendedPerDay)
	}
	slices.Sort(minutes)
	return minutes, nil
}

// Times are the sync times as HH:MM.
func (s Schedule) Times() []string {
	times := make([]string, 0, len(s.Minutes))
	for _, minute := range s.Minutes {
		times = append(times, fmt.Sprintf("%02d:%02d", minute/60, minute%60))
	}
	return times
}

func (s Schedule) location() *time.Location {
	if s.Location == nil {
		return time.UTC
	}
	return s.Location
}

// Next is the first sync time strictly after t.
func (s Schedule) Next(t time.Time) time.Time {
	local := t.In(s.location())
	for day := 0; day < 3; day++ {
		date := local.AddDate(0, 0, day)
		for _, minute := range s.Minutes {
			slot := time.Date(date.Year(), date.Month(), date.Day(), minute/60, minute%60, 0, 0, s.location())
			if slot.After(t) {
				return slot
			}
		}
	}
	return t.Add(24 * time.Hour)
}

// FirstTomorrow is the first sync time of the local day after t's.
func (s Schedule) FirstTomorrow(t time.Time) time.Time {
	local := t.In(s.location())
	next := local.AddDate(0, 0, 1)
	minute := 0
	if len(s.Minutes) > 0 {
		minute = s.Minutes[0]
	}
	return time.Date(next.Year(), next.Month(), next.Day(), minute/60, minute%60, 0, 0, s.location())
}

// Day is t's local date, the day an unattended read counts against.
func (s Schedule) Day(t time.Time) time.Time {
	local := t.In(s.location())
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}

// RateLimitPause is how long to leave a bank alone after it refused reads
// as too many, as the provider recommends.
const RateLimitPause = 6 * time.Hour

// bankRetry is when to try again after the bank or provider failed
// failures times in a row: 15 minutes, doubling up to 2 hours, but never
// later than the next sync time. Every retry is an unattended read.
func (s Schedule) bankRetry(now time.Time, failures int) time.Time {
	delay := 15 * time.Minute
	for i := 1; i < failures && delay < 2*time.Hour; i++ {
		delay *= 2
	}
	return earliest(now.Add(min(delay, 2*time.Hour)), s.Next(now))
}

// rateLimitRetry is the first sync time at least RateLimitPause away.
func (s Schedule) rateLimitRetry(now time.Time) time.Time {
	return s.Next(now.Add(RateLimitPause - time.Second))
}

// financeRetry is when to send an import Finance did not take again: 1
// minute, doubling up to 30. It does not read the bank.
func financeRetry(now time.Time, failures int) time.Time {
	delay := time.Minute
	for i := 1; i < failures && delay < 30*time.Minute; i++ {
		delay *= 2
	}
	return now.Add(min(delay, 30*time.Minute))
}

func earliest(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
