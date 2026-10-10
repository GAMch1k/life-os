// Package sources runs Calendar's source registry: refreshing a source
// (download, parse, reconcile its imported events, record the outcome) and
// the daily scheduler. The ICS specifics live in package ics.
package sources

import (
	"fmt"
	"time"
)

// Schedule is the daily refresh slot: a wall-clock time in the owner's zone.
type Schedule struct {
	Hour, Minute int
	Location     *time.Location
}

// ParseSchedule reads an HH:MM daily refresh time.
func ParseSchedule(value string, location *time.Location) (Schedule, error) {
	parsed, err := time.Parse("15:04", value)
	if err != nil || parsed.Format("15:04") != value {
		return Schedule{}, fmt.Errorf("refresh time %q must be a 24-hour HH:MM time such as 04:30", value)
	}
	if location == nil {
		location = time.UTC
	}
	return Schedule{Hour: parsed.Hour(), Minute: parsed.Minute(), Location: location}, nil
}

func (s Schedule) String() string { return fmt.Sprintf("%02d:%02d %s", s.Hour, s.Minute, s.Location) }

// Next is the first daily slot strictly after after.
func (s Schedule) Next(after time.Time) time.Time {
	local := after.In(s.Location)
	slot := time.Date(local.Year(), local.Month(), local.Day(), s.Hour, s.Minute, 0, 0, s.Location)
	for !slot.After(after) {
		local = local.AddDate(0, 0, 1)
		slot = time.Date(local.Year(), local.Month(), local.Day(), s.Hour, s.Minute, 0, 0, s.Location)
	}
	return slot.UTC()
}

// Retry backoff after failed refreshes: 15 minutes, doubling up to 6 hours,
// and never later than the next daily slot.
const (
	firstRetry = 15 * time.Minute
	maxRetry   = 6 * time.Hour
)

func backoff(failures int) time.Duration {
	delay := firstRetry
	for range max(failures-1, 0) {
		delay *= 2
		if delay >= maxRetry {
			return maxRetry
		}
	}
	return delay
}

// retryAt is when a source that has now failed failures times in a row is
// tried again.
func (s Schedule) retryAt(now time.Time, failures int) time.Time {
	retry := now.Add(backoff(failures)).UTC()
	if daily := s.Next(now); daily.Before(retry) {
		return daily
	}
	return retry
}
