package reminders

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/GAMch1k/life-os/pkg/notify"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
)

// Category is the Notifications category of every Calendar reminder.
const Category = "calendar.reminder"

// Notifications limits (see docs/services/notifications.md).
const (
	maxTitle = 200
	maxBody  = 2000
)

// reminderImportance is how urgently the owner hears about a reminder for
// item: its effective priority (its own, an occurrence's or its series',
// else its default: its source's for an imported event, medium for a Life
// OS one), as resolved for listing.
func reminderImportance(item generated.CalendarEvent) notify.Importance {
	return priorityImportance(string(item.Priority))
}

// priorityImportance maps an event priority to a notification importance:
// low, medium, and high map to themselves; anything else is medium.
func priorityImportance(priority string) notify.Importance {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "high":
		return notify.High
	case "low":
		return notify.Low
	}
	return notify.Medium
}

// notification is the message for one due reminder. Its title is the
// event's title; its body says when the event starts as seen from the
// reminder time, such as "In 30 minutes · 15:00–16:00 · Dr. Weber" or
// "Tomorrow at 06:00", in the owner's timezone with a 24-hour clock. A
// reminder sent late (after Calendar was down) is phrased from sentAt and
// ends with "(late)". showLocation adds the event's location.
func notification(due Due, sentAt time.Time, late, showLocation bool, owner *time.Location) notify.Notification {
	at := due.At
	if late {
		at = sentAt
	}
	parts := []string{when(due, at, owner)}
	if showLocation && due.Event.Location != nil {
		if location := oneLine(*due.Event.Location); location != "" {
			parts = append(parts, location)
		}
	}
	body := strings.Join(parts, " · ")
	if late {
		body += " (late)"
	}
	title := oneLine(due.Event.Title)
	if title == "" {
		title = "Calendar reminder"
	}
	return notify.Notification{
		Title:      clip(title, maxTitle),
		Body:       clip(body, maxBody),
		Importance: reminderImportance(due.Event),
		Category:   Category,
		Link:       "/calendar?date=" + due.Start.In(owner).Format(time.DateOnly),
		DedupeKey:  due.Key,
		OccurredAt: due.At,
	}
}

// when describes the occurrence's time as seen at at.
func when(due Due, at time.Time, owner *time.Location) string {
	if due.Event.AllDay {
		return allDayWhen(due, at, owner)
	}
	start := due.Start.In(owner)
	end := start
	if due.Event.EndAt != nil {
		end = due.Event.EndAt.In(owner)
	}
	now := at.In(owner)
	span := start.Format("15:04") + "–" + end.Format("15:04")
	if !sameDay(start, end) {
		span = start.Format("15:04") + "–" + dayLabel(end, now) + " " + end.Format("15:04")
	}
	lead := start.Sub(at).Round(time.Minute)
	days := daysBetween(now, start)
	switch {
	case lead < 0:
		return "Started " + duration(-lead) + " ago · " + span
	case lead == 0:
		return "Now · " + span
	case lead < time.Hour:
		return "In " + duration(lead) + " · " + span
	case days == 0:
		return "In " + duration(lead) + " · " + span
	case days == 1:
		return "Tomorrow at " + start.Format("15:04")
	}
	return dayLabel(start, now) + " at " + start.Format("15:04")
}

// allDayWhen describes an all-day occurrence: "Today, all day", "Tomorrow,
// all day", "Thu 15 Oct, all day", or for several days "Tomorrow until Sun
// 18 Oct".
func allDayWhen(due Due, at time.Time, owner *time.Location) string {
	now := at.In(owner)
	first := due.Start.In(owner)
	last := first
	if due.Event.EndDate != nil {
		day := due.Event.EndDate.Time
		last = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, owner)
	}
	var label string
	switch days := daysBetween(now, first); {
	case days < 0:
		if sameDay(first, last) || daysBetween(now, last) < 0 {
			return "Started " + dayLabel(first, now)
		}
		return "Until " + dayLabel(last, now)
	case days == 0:
		label = "Today"
	case days == 1:
		label = "Tomorrow"
	default:
		label = dayLabel(first, now)
	}
	if sameDay(first, last) {
		return label + ", all day"
	}
	return label + " until " + dayLabel(last, now)
}

// daysBetween is how many calendar days b's date is after a's (both in the
// same location).
func daysBetween(a, b time.Time) int {
	da := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	db := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(db.Sub(da).Hours() / 24)
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}

// dayLabel is "Thu 15 Oct", with the year when it is not now's.
func dayLabel(day, now time.Time) string {
	if day.Year() != now.Year() {
		return day.Format("Mon 2 Jan 2006")
	}
	return day.Format("Mon 2 Jan")
}

// duration is a positive whole-minute duration in words: "1 minute", "30
// minutes", "3 hours", "2 hours 30 minutes", "2 days 3 hours".
func duration(d time.Duration) string {
	minutes := int(d.Round(time.Minute) / time.Minute)
	unit := func(n int, name string) string {
		if n == 1 {
			return "1 " + name
		}
		return fmt.Sprintf("%d %ss", n, name)
	}
	switch {
	case minutes < 60:
		return unit(minutes, "minute")
	case minutes < 24*60:
		text := unit(minutes/60, "hour")
		if rest := minutes % 60; rest > 0 {
			text += " " + unit(rest, "minute")
		}
		return text
	}
	text := unit(minutes/(24*60), "day")
	if rest := minutes % (24 * 60) / 60; rest > 0 {
		text += " " + unit(rest, "hour")
	}
	return text
}

// oneLine collapses whitespace and control characters into single spaces.
func oneLine(text string) string {
	return strings.Join(strings.FieldsFunc(text, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}

// clip shortens text to limit runes, ending with an ellipsis.
func clip(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}
