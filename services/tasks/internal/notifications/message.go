package notifications

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/GAMch1k/life-os/pkg/notify"
)

// Notifications categories.
const (
	CategoryDue     = "tasks.due"
	CategoryMorning = "tasks.morning"
	CategoryEvening = "tasks.evening"
	CategorySummary = "summary.morning"
)

// Message limits: Notifications' own (docs/services/notifications.md), and
// how many Tasks or events one list names before "and N more".
const (
	maxTitle     = 200
	maxBody      = 2000
	maxItems     = 10
	maxItemTitle = 120
)

// Links the messages open: the Tasks workspace for a list, Home for the
// summary. A message about one Task opens that Task (taskLink).
const (
	tasksLink = "/tasks"
	homeLink  = "/"
)

// taskLink opens one Task in the Tasks workspace: "/tasks?task=<id>". The
// Android app reads the id from it to offer Done on the notification.
func taskLink(id string) string {
	return tasksLink + "?task=" + url.QueryEscape(id)
}

// dueMessage is a timed Task's reminder; it links to the Task (taskLink).
// Its title is the Task's title; its body says when it is due, as seen from
// the reminder time, in the owner's timezone with a 24-hour clock: "Due in
// 30 minutes · 15:00". A reminder sent late (after Tasks was down) is
// phrased from sentAt and ends with "(late)": "Due in 10 minutes · 15:00
// (late)", "Was due 20 minutes ago · 15:00 (late)".
func dueMessage(due Due, sentAt time.Time, late bool, owner *time.Location) notify.Notification {
	at := due.At
	if late {
		at = sentAt
	}
	deadline := due.Task.Due.In(owner)
	clock := deadline.Format("15:04")
	if now := at.In(owner); !sameDate(deadline, now) {
		clock = dayLabel(deadline, now) + " " + clock
	}
	var when string
	switch lead := due.Task.Due.Sub(at).Round(time.Minute); {
	case lead > 0:
		when = "Due in " + duration(lead)
	case lead == 0:
		when = "Due now"
	default:
		when = "Was due " + duration(-lead) + " ago"
	}
	body := when + " · " + clock
	if late {
		body += " (late)"
	}
	title := oneLine(due.Task.Title)
	if title == "" {
		title = "Task due"
	}
	return notify.Notification{
		Title: clip(title, maxTitle), Body: clip(body, maxBody), Importance: notify.Medium,
		Category: CategoryDue, Link: taskLink(due.Task.ID), DedupeKey: due.Key, OccurredAt: due.At,
	}
}

// morningMessage is the morning list: the date-only Tasks due on the day,
// then every overdue Task, one per line ("Today: Renew passport", "Overdue
// since Tue 6 Oct: Pay rent"), at most maxItems lines and "and N more".
// Titled "Tasks for today", or "Overdue tasks" when nothing is due today.
// false when there is nothing to list.
func morningMessage(due Due, lists Lists, sentAt time.Time, owner *time.Location) (notify.Notification, bool) {
	if len(lists.DateOnlyToday)+len(lists.Overdue) == 0 {
		return notify.Notification{}, false
	}
	sentOn := dateOf(sentAt, owner)
	today := relativeDay(due.Day, sentOn)
	lines := make([]string, 0, len(lists.DateOnlyToday)+len(lists.Overdue))
	for _, task := range lists.DateOnlyToday {
		lines = append(lines, capitalize(today)+": "+itemTitle(task.Title))
	}
	for _, task := range lists.Overdue {
		lines = append(lines, "Overdue since "+since(task, due.Day, owner)+": "+itemTitle(task.Title))
	}
	title := "Tasks for " + today
	if len(lists.DateOnlyToday) == 0 {
		title = "Overdue tasks"
	}
	return notify.Notification{
		Title: title, Body: clip(strings.Join(capped(lines), "\n"), maxBody), Importance: notify.Medium,
		Category: CategoryMorning, Link: tasksLink, DedupeKey: due.Key, OccurredAt: due.At,
	}, true
}

// eveningMessage is the evening list: "Tasks for tomorrow" with the
// date-only Tasks due the next day, one title per line, at most maxItems
// and "and N more". false when there are none.
func eveningMessage(due Due, lists Lists, sentAt time.Time, owner *time.Location) (notify.Notification, bool) {
	if len(lists.Tomorrow) == 0 {
		return notify.Notification{}, false
	}
	lines := make([]string, 0, len(lists.Tomorrow))
	for _, task := range lists.Tomorrow {
		lines = append(lines, itemTitle(task.Title))
	}
	return notify.Notification{
		Title: "Tasks for " + relativeDay(due.Day.AddDate(0, 0, 1), dateOf(sentAt, owner)),
		Body:  clip(strings.Join(capped(lines), "\n"), maxBody), Importance: notify.Medium,
		Category: CategoryEvening, Link: tasksLink, DedupeKey: due.Key, OccurredAt: due.At,
	}, true
}

// EventsState is what the summary knows about the day's events.
type EventsState int

const (
	// EventsListed: Calendar answered (possibly with no events).
	EventsListed EventsState = iota
	// EventsUnavailable: Calendar did not answer; the summary says so.
	EventsUnavailable
	// EventsOff: no Calendar is configured; the summary has no events.
	EventsOff
)

// summaryMessage is the daily morning summary, "Your day": one line each for
// the day's events ("Events: 09:00 Dentist · 20:00 Dinner"), Tasks
// ("Tasks: Renew passport · 15:00 Call mom": date-only, then timed with
// their time), and overdue Tasks ("Overdue: Pay rent (since Tue 6 Oct)"),
// each naming at most maxItems and "and N more". A list with nothing is left
// out; "Events: Calendar unavailable" when Calendar did not answer. false
// when there is no event and no Task at all. It is low importance; merged
// (the morning list at the same time) it is medium when it names a
// date-only Task due today or an overdue one, as the morning list would be.
func summaryMessage(due Due, lists Lists, events []Event, state EventsState, merged bool, sentAt time.Time, owner *time.Location) (notify.Notification, bool) {
	today := len(lists.DateOnlyToday) + len(lists.TimedToday)
	if len(events) == 0 && today == 0 && len(lists.Overdue) == 0 {
		return notify.Notification{}, false
	}
	var lines []string
	switch {
	case state == EventsUnavailable:
		lines = append(lines, "Events: Calendar unavailable")
	case len(events) > 0:
		items := make([]string, 0, len(events))
		for _, event := range events {
			items = append(items, event.label(due.Day, owner))
		}
		lines = append(lines, "Events: "+strings.Join(capped(items), " · "))
	}
	if today > 0 {
		items := make([]string, 0, today)
		for _, task := range lists.DateOnlyToday {
			items = append(items, itemTitle(task.Title))
		}
		for _, task := range lists.TimedToday {
			items = append(items, task.Due.In(owner).Format("15:04")+" "+itemTitle(task.Title))
		}
		lines = append(lines, "Tasks: "+strings.Join(capped(items), " · "))
	}
	if len(lists.Overdue) > 0 {
		items := make([]string, 0, len(lists.Overdue))
		for _, task := range lists.Overdue {
			items = append(items, itemTitle(task.Title)+" (since "+since(task, due.Day, owner)+")")
		}
		lines = append(lines, "Overdue: "+strings.Join(capped(items), " · "))
	}
	title := "Your day"
	if sentOn := dateOf(sentAt, owner); !sentOn.Equal(due.Day) {
		title += ": " + due.Day.Format("Mon 2 Jan")
	}
	importance := notify.Low
	if merged && len(lists.DateOnlyToday)+len(lists.Overdue) > 0 {
		importance = notify.Medium
	}
	return notify.Notification{
		Title: title, Body: clip(strings.Join(lines, "\n"), maxBody), Importance: importance,
		Category: CategorySummary, Link: homeLink, DedupeKey: due.Key, OccurredAt: due.At,
	}, true
}

// since is when an overdue Task became due, as a message on day says it:
// its time when it was due that day ("08:30"), else its date ("Tue 6 Oct").
func since(task Task, day time.Time, owner *time.Location) string {
	if !task.DateOnly && task.Day.Equal(day) {
		return task.Due.In(owner).Format("15:04")
	}
	return dayLabel(task.Day, day)
}

// relativeDay names day as seen on the owner-local date sentOn: "today",
// "tomorrow", or "Sat 10 Oct" (a message sent late, on another day).
func relativeDay(day, sentOn time.Time) string {
	switch {
	case day.Equal(sentOn):
		return "today"
	case day.Equal(sentOn.AddDate(0, 0, 1)):
		return "tomorrow"
	}
	return dayLabel(day, sentOn)
}

// capped keeps at most maxItems items, adding "and N more" for the rest.
func capped(items []string) []string {
	if len(items) <= maxItems {
		return items
	}
	return append(items[:maxItems:maxItems], fmt.Sprintf("and %d more", len(items)-maxItems))
}

func itemTitle(title string) string {
	text := oneLine(title)
	if text == "" {
		text = "(untitled)"
	}
	return clip(text, maxItemTitle)
}

func capitalize(text string) string {
	if text == "" {
		return text
	}
	runes := []rune(text)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// sameDate reports whether a and b (in the same location) fall on one date.
func sameDate(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}

// dayLabel is "Tue 6 Oct", with the year when it is not now's.
func dayLabel(day, now time.Time) string {
	if day.Year() != now.Year() {
		return day.Format("Mon 2 Jan 2006")
	}
	return day.Format("Mon 2 Jan")
}

// duration is a positive whole-minute duration in words: "1 minute", "30
// minutes", "3 hours", "2 hours 30 minutes", "1 day 2 hours".
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
