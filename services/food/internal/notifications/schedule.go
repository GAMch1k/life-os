// Package notifications tells the owner, through the Notifications service
// (pkg/notify, the one notification path), which pantry food expires soon:
// once a day at the expiry time on the owner's wall clock, one message with
// the in-stock items that expire that day or the next. Each day's reminder is
// handled once, including one that came due while Food was down. It follows
// Tasks' notification delivery (cursor, lease, grace window, handled log)
// without sharing its code.
package notifications

import (
	"fmt"
	"time"
)

// Clock is a time of day on the owner's wall clock.
type Clock struct{ Hour, Minute int }

// ParseClock reads a 24-hour "HH:MM" time.
func ParseClock(value string) (Clock, error) {
	parsed, err := time.Parse("15:04", value)
	if err != nil || parsed.Format("15:04") != value {
		return Clock{}, fmt.Errorf("%q is not a 24-hour HH:MM time", value)
	}
	return Clock{Hour: parsed.Hour(), Minute: parsed.Minute()}, nil
}

func (c Clock) String() string { return fmt.Sprintf("%02d:%02d", c.Hour, c.Minute) }

// DefaultExpiryTime is when the expiry reminder is due unless
// FOOD_EXPIRY_TIME says otherwise.
var DefaultExpiryTime = Clock{Hour: 8}

// Due is one day's expiry reminder.
type Due struct {
	// At is when it is due.
	At time.Time
	// Key is the notification dedupe key.
	Key string
	// Day is the owner-local date it is for (at midnight UTC).
	Day time.Time
}

// expiryKey names one day's expiry reminder.
func expiryKey(day time.Time) string {
	return "food:expiry:" + day.Format(time.DateOnly)
}

// dueBetween returns the expiry reminders due in (from, to]: one on every
// owner-local day, at clock on that day's wall clock (see wallTime), in due
// order.
func dueBetween(clock Clock, owner *time.Location, from, to time.Time) []Due {
	var out []Due
	for day, last := dateOf(from, owner), dateOf(to, owner); !day.After(last); day = day.AddDate(0, 0, 1) {
		at := wallTime(day, clock, owner)
		if !at.After(from) || at.After(to) {
			continue
		}
		out = append(out, Due{At: at, Key: expiryKey(day), Day: day})
	}
	return out
}

// dateOf is instant's owner-local date, at midnight UTC.
func dateOf(instant time.Time, owner *time.Location) time.Time {
	local := instant.In(owner)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}

// wallTime is the instant at which owner's clocks show clock on date day
// (at midnight UTC). A time skipped when clocks go forward is taken as the
// same clock time after the jump (02:30 becomes 03:30 CEST); a time that
// occurs twice when clocks go back is the first of the two (02:30 CEST, not
// 02:30 CET), so a reminder is never later than the owner expects. Tasks and
// Calendar resolve their wall times the same way.
func wallTime(day time.Time, clock Clock, owner *time.Location) time.Time {
	year, month, date := day.Date()
	at := time.Date(year, month, date, clock.Hour, clock.Minute, 0, 0, owner)
	want := time.Date(year, month, date, clock.Hour, clock.Minute, 0, 0, time.UTC)
	for _, probe := range []time.Duration{-3 * time.Hour, 3 * time.Hour} {
		_, offset := at.Add(probe).Zone()
		candidate := time.Date(year, month, date, clock.Hour, clock.Minute, 0, 0, time.FixedZone("", offset))
		if candidate.Before(at) && sameWall(candidate.In(owner), want) {
			at = candidate
		}
	}
	return at.In(owner)
}

// sameWall reports whether local shows wall's date and clock.
func sameWall(local, wall time.Time) bool {
	return local.Year() == wall.Year() && local.Month() == wall.Month() && local.Day() == wall.Day() &&
		local.Hour() == wall.Hour() && local.Minute() == wall.Minute()
}
