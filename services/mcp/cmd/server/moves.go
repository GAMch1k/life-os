package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// move is a relative reschedule of a Task or event. Tasks and Calendar take
// absolute times, so MCP reads the Task or event, works out the new time on
// the owner's wall clock, and sends that: the caller never needs the current
// time of the Task or event, nor UTC offsets or daylight-saving arithmetic.
//
// The day changes first (days, or toDate), then the time of day (toTime, else
// the one it had), then hours and minutes are added in real time. So days
// keep the wall-clock time across a daylight-saving change (10:00 stays
// 10:00), while hours are elapsed time (01:30 CEST plus 2 hours on the last
// Sunday of October is 02:30 CET). A wall-clock time that does not exist
// that day moves past the gap (02:30 becomes 03:30), and one that occurs
// twice is the first.
type move struct {
	Days     *int    `json:"days,omitempty" jsonschema:"Move by this many calendar days, keeping the time of day (negative: earlier). Counted from its current date, so for 'tomorrow' or 'Friday' give toDate"`
	Hours    *int    `json:"hours,omitempty" jsonschema:"Move by this many hours of real time (negative: earlier), such as 1 for 'an hour later'"`
	Minutes  *int    `json:"minutes,omitempty" jsonschema:"Move by this many minutes of real time (negative: earlier)"`
	ToDate   *string `json:"toDate,omitempty" jsonschema:"Move to this owner-local date (YYYY-MM-DD), keeping the time of day: 'to tomorrow, same time' or 'to Friday'"`
	ToTime   *string `json:"toTime,omitempty" jsonschema:"Set this owner-local time of day (HH:MM, 24-hour), on toDate when given, else on its date"`
	DateOnly *bool   `json:"dateOnly,omitempty" jsonschema:"Tasks only: keep the date (or toDate) and drop the time of day"`
	TimeZone *string `json:"timeZone,omitempty" jsonschema:"IANA timezone the dates and times mean, such as the capture's; default the owner's (LIFE_OS_TIMEZONE)"`
}

// Limits of one move: ten years of days, a year of hours or minutes.
const (
	maxMoveDays    = 3660
	maxMoveHours   = 8784
	maxMoveMinutes = 527040
)

// movePlan is a checked move.
type movePlan struct {
	days     int
	shift    time.Duration
	toDate   time.Time // a civil date (UTC midnight) when hasDate
	hasDate  bool
	hasTime  bool
	hour     int
	minute   int
	dateOnly bool
	zone     *time.Location
}

// refusedMove is a move that does not fit the Task or event, such as an
// hour later for a Task without a time. Its text starts "cannot move:", so
// the processor can hand it to the owner instead of retrying.
type refusedMove struct{ reason string }

func (e refusedMove) Error() string { return "cannot move: " + e.reason }

func refuse(format string, args ...any) error {
	return refusedMove{reason: fmt.Sprintf(format, args...)}
}

// plan checks a move and resolves its timezone.
func (m move) plan(o owner) (movePlan, error) {
	var p movePlan
	zoneName := ""
	if m.TimeZone != nil {
		zoneName = *m.TimeZone
	}
	zone, err := o.location(zoneName)
	if err != nil {
		return p, fmt.Errorf("move.%w", err)
	}
	p.zone = zone
	if m.Days != nil {
		if *m.Days < -maxMoveDays || *m.Days > maxMoveDays {
			return p, fmt.Errorf("move.days must be between %d and %d", -maxMoveDays, maxMoveDays)
		}
		p.days = *m.Days
	}
	if m.Hours != nil {
		if *m.Hours < -maxMoveHours || *m.Hours > maxMoveHours {
			return p, fmt.Errorf("move.hours must be between %d and %d", -maxMoveHours, maxMoveHours)
		}
		p.shift += time.Duration(*m.Hours) * time.Hour
	}
	if m.Minutes != nil {
		if *m.Minutes < -maxMoveMinutes || *m.Minutes > maxMoveMinutes {
			return p, fmt.Errorf("move.minutes must be between %d and %d", -maxMoveMinutes, maxMoveMinutes)
		}
		p.shift += time.Duration(*m.Minutes) * time.Minute
	}
	if m.ToDate != nil {
		day, err := time.Parse(time.DateOnly, strings.TrimSpace(*m.ToDate))
		if err != nil {
			return p, fmt.Errorf("move.toDate must be a date as YYYY-MM-DD, such as 2026-10-23, not %q", *m.ToDate)
		}
		p.toDate, p.hasDate = day, true
	}
	if m.ToTime != nil {
		clock, err := time.Parse("15:04", strings.TrimSpace(*m.ToTime))
		if err != nil {
			return p, fmt.Errorf("move.toTime must be a 24-hour time as HH:MM, such as 09:30, not %q", *m.ToTime)
		}
		p.hour, p.minute, p.hasTime = clock.Hour(), clock.Minute(), true
	}
	p.dateOnly = m.DateOnly != nil && *m.DateOnly
	shifts := (m.Hours != nil && *m.Hours != 0) || (m.Minutes != nil && *m.Minutes != 0)
	switch {
	case m.Days != nil && *m.Days != 0 && p.hasDate:
		return p, errors.New("move.days and move.toDate cannot be combined: toDate already names the day")
	case p.hasTime && shifts:
		return p, errors.New("move.toTime cannot be combined with hours or minutes: toTime already names the time")
	case p.dateOnly && (p.hasTime || shifts):
		return p, errors.New("move.dateOnly drops the time, so it cannot be combined with toTime, hours, or minutes")
	case p.days == 0 && p.shift == 0 && !p.hasDate && !p.hasTime && !p.dateOnly:
		return p, errors.New("move changes nothing: give days, hours, minutes, toDate, toTime, or dateOnly")
	}
	return p, nil
}

// changesDay reports whether the move names a day or a time of day, which
// is resolved on the wall clock.
func (p movePlan) changesDay() bool { return p.days != 0 || p.hasDate || p.hasTime }

// day is the date a moved start falls on, from the date it had.
func (p movePlan) day(from time.Time) time.Time {
	if p.hasDate {
		return p.toDate
	}
	return from.AddDate(0, 0, p.days)
}

// instant moves a timed start (or due time).
func (p movePlan) instant(at time.Time) time.Time {
	moved := at
	if p.changesDay() {
		local := at.In(p.zone)
		hour, minute, second := local.Hour(), local.Minute(), local.Second()
		if p.hasTime {
			hour, minute, second = p.hour, p.minute, 0
		}
		moved = wallClock(p.day(civil(local)), hour, minute, second, p.zone)
	}
	return moved.Add(p.shift).In(p.zone)
}

// task is a Task's new due instant and whether it is date-only (the
// owner-local midnight). due is nil for an unscheduled Task.
func (p movePlan) task(title string, due *time.Time) (time.Time, error) {
	switch {
	case due == nil:
		if !p.hasDate {
			return time.Time{}, refuse("“%s” has no due date to move from: name the day it is due (move.toDate, with move.toTime for a time)", title)
		}
		if p.shift != 0 {
			return time.Time{}, refuse("“%s” has no due time to move by hours or minutes: name the time (move.toTime)", title)
		}
		if p.hasTime {
			return wallClock(p.toDate, p.hour, p.minute, 0, p.zone), nil
		}
		return startOfDay(p.toDate, p.zone), nil
	case isDateOnly(*due, p.zone):
		day := p.day(civil(due.In(p.zone)))
		if p.shift != 0 {
			return time.Time{}, refuse("“%s” is due on %s without a time, so it cannot move by hours or minutes: name the time (move.toTime)", title, due.In(p.zone).Format(dayText))
		}
		if p.hasTime {
			return wallClock(day, p.hour, p.minute, 0, p.zone), nil
		}
		return startOfDay(day, p.zone), nil
	case p.dateOnly:
		return startOfDay(p.day(civil(due.In(p.zone))), p.zone), nil
	default:
		return p.instant(*due), nil
	}
}

// event is the Calendar change that moves an event (or one occurrence):
// a timed one keeps its duration in real time, an all-day one its number
// of days.
func (p movePlan) event(event map[string]any) (map[string]any, error) {
	title, _ := event["title"].(string)
	if p.dateOnly {
		return nil, refuse("“%s” is an event, which has a time or is all-day: only a Task can drop its time (move.dateOnly; an event becomes all-day with allDay true, startDate, and endDate)", title)
	}
	if allDay, _ := event["allDay"].(bool); allDay {
		if p.hasTime || p.shift != 0 {
			return nil, refuse("“%s” is an all-day event, so it has no time of day to move: move it by days or to a date (to give it a time, change it with allDay false, startAt, and endAt)", title)
		}
		start, okStart := date(event["startDate"])
		end, okEnd := date(event["endDate"])
		if !okStart || !okEnd {
			return nil, fmt.Errorf("“%s” has no start and end date to move from", title)
		}
		moved := p.day(start)
		return map[string]any{"startDate": moved.Format(time.DateOnly), "endDate": moved.Add(end.Sub(start)).Format(time.DateOnly)}, nil
	}
	start, okStart := instant(event["startAt"])
	end, okEnd := instant(event["endAt"])
	if !okStart || !okEnd {
		return nil, fmt.Errorf("“%s” has no start and end time to move from", title)
	}
	moved := p.instant(start)
	return map[string]any{"startAt": moved.Format(time.RFC3339), "endAt": moved.Add(end.Sub(start)).Format(time.RFC3339)}, nil
}
