package backup

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultTime is when the nightly run starts, owner-local.
const DefaultTime = "03:15"

// CatchUpMargin is how much earlier than a scheduled time a successful full
// run may have started and still count for it (a manual full run at 02:30
// stands in for a missed 03:15).
const CatchUpMargin = time.Hour

// Schedule is the nightly run time in the owner's timezone.
type Schedule struct {
	// Minute is the run time as minutes after local midnight.
	Minute   int
	Location *time.Location
}

// ParseTime reads an HH:MM time such as 03:15 as minutes after midnight.
func ParseTime(value string) (int, error) {
	hours, minutes, ok := strings.Cut(strings.TrimSpace(value), ":")
	h, errH := strconv.Atoi(hours)
	m, errM := strconv.Atoi(minutes)
	if !ok || errH != nil || errM != nil || len(hours) != 2 || len(minutes) != 2 || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("%q is not a time such as 03:15", value)
	}
	return h*60 + m, nil
}

// DefaultRestoreCheckDay is the weekday whose nightly run a restore check
// follows.
const DefaultRestoreCheckDay = "sunday"

// ParseCheckDay reads a weekday such as sunday, or off. enabled is false
// for off.
func ParseCheckDay(value string) (day time.Weekday, enabled bool, err error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "off" {
		return 0, false, nil
	}
	for weekday := time.Sunday; weekday <= time.Saturday; weekday++ {
		if strings.ToLower(weekday.String()) == value {
			return weekday, true, nil
		}
	}
	return 0, false, fmt.Errorf("%q is not a weekday such as sunday, or off", value)
}

// Time is the run time as HH:MM.
func (s Schedule) Time() string {
	return fmt.Sprintf("%02d:%02d", s.Minute/60, s.Minute%60)
}

func (s Schedule) location() *time.Location {
	if s.Location == nil {
		return time.UTC
	}
	return s.Location
}

// On is the run time on the given local date. Daylight saving time never
// skips or doubles a run: a time the clocks jump over (02:30 when they go
// from 02:00 to 03:00) runs when the jump ends, and a time that happens
// twice (02:30 when they go back from 03:00 to 02:00) runs the first time.
func (s Schedule) On(year int, month time.Month, day int) time.Time {
	location := s.location()
	hour, minute := s.Minute/60, s.Minute%60
	// time.Date picks one of the zones around a transition; the zones in
	// effect twelve hours either side of it are the two candidates.
	guess := time.Date(year, month, day, hour, minute, 0, 0, location)
	wall := time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
	var first time.Time
	for _, probe := range []time.Time{guess.Add(-12 * time.Hour), guess.Add(12 * time.Hour)} {
		_, offset := probe.Zone()
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(location)
		if local.Year() == year && local.Month() == month && local.Day() == day && local.Hour() == hour && local.Minute() == minute {
			if first.IsZero() || candidate.Before(first) {
				first = candidate
			}
		}
	}
	if !first.IsZero() {
		return first.In(location)
	}
	// The clocks jump over the time: run at the moment they jump, the start
	// of the zone the time would fall in by the old offset.
	_, before := guess.Add(-12 * time.Hour).Zone()
	start, _ := wall.Add(-time.Duration(before) * time.Second).In(location).ZoneBounds()
	return start.In(location)
}

// Next is the first run time strictly after t.
func (s Schedule) Next(t time.Time) time.Time {
	local := t.In(s.location())
	for day := 0; day < 3; day++ {
		date := time.Date(local.Year(), local.Month(), local.Day()+day, 12, 0, 0, 0, s.location())
		if slot := s.On(date.Year(), date.Month(), date.Day()); slot.After(t) {
			return slot
		}
	}
	return t.Add(24 * time.Hour)
}

// Previous is the latest run time at or before t.
func (s Schedule) Previous(t time.Time) time.Time {
	local := t.In(s.location())
	for day := 0; day < 3; day++ {
		date := time.Date(local.Year(), local.Month(), local.Day()-day, 12, 0, 0, 0, s.location())
		if slot := s.On(date.Year(), date.Month(), date.Day()); !slot.After(t) {
			return slot
		}
	}
	return t.Add(-24 * time.Hour)
}

// CatchUpDue says whether a run is owed at now: true when no successful full
// run is known, or when the newest one (lastSuccess, its start) is older
// than the latest scheduled time less CatchUpMargin, that is older than one
// schedule interval plus the margin, counted from the run time the service
// missed.
func (s Schedule) CatchUpDue(lastSuccess time.Time, now time.Time) bool {
	if lastSuccess.IsZero() {
		return true
	}
	return lastSuccess.Before(s.Previous(now).Add(-CatchUpMargin))
}
