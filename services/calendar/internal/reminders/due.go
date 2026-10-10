// Package reminders delivers Calendar reminders: every effective reminder of
// every active event and occurrence is sent to the Notifications service
// (through pkg/notify, the one notification path) when it is due, exactly
// once, including reminders that came due while Calendar was down.
package reminders

import (
	"fmt"
	"sort"
	"time"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
)

// Reminder offsets are bounded by the API (validateReminder): a relative
// reminder is at most 28 days before the start, a localTime one at most 28
// days before the start date, at any time of that day. So a reminder due at
// R belongs to an occurrence starting no earlier than R - scanBefore (a
// localTime reminder late on the event's own day, after it started) and no
// later than R + scanAhead.
const (
	maxDaysBefore = 28
	scanBefore    = 26 * time.Hour                               // a day, plus a daylight saving shift and slack
	scanAhead     = (maxDaysBefore+1)*24*time.Hour + 2*time.Hour // 28 days back to midnight, plus slack
)

// Due is one effective reminder of one occurrence.
type Due struct {
	Event    generated.CalendarEvent
	Reminder generated.Reminder
	// Start is when the occurrence starts (an all-day one at owner-local
	// midnight of its first day); At is when the reminder is due.
	Start, At time.Time
	// Key is the notification dedupe key.
	Key string
}

// dueBetween returns every effective reminder of the active items due in
// (from, to], in due order (then by key), each once.
func dueBetween(items []generated.CalendarEvent, owner *time.Location, from, to time.Time) []Due {
	var out []Due
	seen := map[string]bool{}
	for _, item := range items {
		if item.Status != generated.Active {
			continue
		}
		start, ok := occurrenceStart(item, owner)
		if !ok {
			continue
		}
		for _, reminder := range item.EffectiveReminders {
			at, reminderKey, ok := reminderTime(reminder, start, owner)
			if !ok || !at.After(from) || at.After(to) {
				continue
			}
			key := dedupeKey(item, start, owner, reminderKey)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Due{Event: item, Reminder: reminder, Start: start, At: at, Key: key})
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if !out[a].At.Equal(out[b].At) {
			return out[a].At.Before(out[b].At)
		}
		return out[a].Key < out[b].Key
	})
	return out
}

// occurrenceStart is when an item starts: a timed event's instant, an
// all-day event's first day at midnight in owner.
func occurrenceStart(item generated.CalendarEvent, owner *time.Location) (time.Time, bool) {
	if item.AllDay {
		if item.StartDate == nil {
			return time.Time{}, false
		}
		day := item.StartDate.Time
		return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, owner), true
	}
	if item.StartAt == nil {
		return time.Time{}, false
	}
	return *item.StartAt, true
}

// reminderTime is when reminder is due for an occurrence starting at start,
// with its stable key: relative is minutesBefore before the start ("rel:30");
// localTime is the owner's wall clock localTime on the start's owner-local
// date minus daysBefore ("local:1:19:00"), resolved in owner (see wallTime).
// ok is false for a reminder that is not valid.
func reminderTime(reminder generated.Reminder, start time.Time, owner *time.Location) (time.Time, string, bool) {
	switch reminder.Kind {
	case generated.Relative:
		if reminder.MinutesBefore == nil || *reminder.MinutesBefore < 0 || *reminder.MinutesBefore > maxDaysBefore*24*60 {
			return time.Time{}, "", false
		}
		minutes := *reminder.MinutesBefore
		return start.Add(-time.Duration(minutes) * time.Minute), fmt.Sprintf("rel:%d", minutes), true
	case generated.LocalTime:
		if reminder.DaysBefore == nil || reminder.LocalTime == nil || *reminder.DaysBefore < 0 || *reminder.DaysBefore > maxDaysBefore {
			return time.Time{}, "", false
		}
		clock, err := time.Parse("15:04", *reminder.LocalTime)
		if err != nil || clock.Format("15:04") != *reminder.LocalTime {
			return time.Time{}, "", false
		}
		days := *reminder.DaysBefore
		local := start.In(owner)
		at := wallTime(local.Year(), local.Month(), local.Day()-days, clock.Hour(), clock.Minute(), owner)
		return at, fmt.Sprintf("local:%d:%s", days, clock.Format("15:04")), true
	}
	return time.Time{}, "", false
}

// wallTime is the instant at which owner's clocks show the given date and
// time (day may overflow the month). A time skipped when clocks go forward
// is taken as the same clock time after the jump (02:30 becomes 03:30
// CEST); a time that occurs twice when clocks go back is the first of the
// two (02:30 CEST, not 02:30 CET), so the reminder is never later than the
// owner expects.
func wallTime(year int, month time.Month, day, hour, minute int, owner *time.Location) time.Time {
	at := time.Date(year, month, day, hour, minute, 0, 0, owner)
	want := time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
	for _, probe := range []time.Duration{-3 * time.Hour, 3 * time.Hour} {
		_, offset := at.Add(probe).Zone()
		candidate := time.Date(want.Year(), want.Month(), want.Day(), hour, minute, 0, 0, time.FixedZone("", offset))
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

// dedupeKey names one reminder of one occurrence at its current time:
// calendar:reminder:<eventId>:<recurrenceId or "single">:<start>:<reminder>,
// where start is the occurrence's start instant in UTC (RFC 3339) or its
// first date for an all-day event. A moved event or occurrence therefore
// gets new keys and reminds again at its new time, while a reminder already
// sent for the old time stays sent.
func dedupeKey(item generated.CalendarEvent, start time.Time, owner *time.Location, reminderKey string) string {
	occurrence := "single"
	if item.Occurrence != nil && item.Occurrence.RecurrenceId != "" {
		occurrence = item.Occurrence.RecurrenceId
	}
	at := start.UTC().Format(time.RFC3339)
	if item.AllDay {
		at = start.In(owner).Format(time.DateOnly)
	}
	return fmt.Sprintf("calendar:reminder:%s:%s:%s:%s", item.Id, occurrence, at, reminderKey)
}
