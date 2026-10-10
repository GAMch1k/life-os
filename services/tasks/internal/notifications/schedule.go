// Package notifications tells the owner about their Tasks through the
// Notifications service (pkg/notify, the one notification path): a reminder
// before each timed Task is due, a morning list of the date-only Tasks due
// that day and every overdue Task, an evening list of the date-only Tasks due
// the next day, and the daily morning summary with today's Tasks, events
// (read from Calendar), and overdue Tasks. Each is sent once, including those
// that came due while Tasks was down. It follows Calendar's reminder delivery
// (cursor, lease, grace window, handled log) without sharing its code.
package notifications

import (
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	store "github.com/GAMch1k/life-os/services/tasks/internal/repository/generated"
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

// Schedule is when Tasks notifies, on the owner's wall clock.
type Schedule struct {
	// Summary: the daily morning summary (TASKS_SUMMARY_TIME, 08:00).
	Summary Clock
	// Morning: the list of date-only Tasks due today and overdue Tasks
	// (TASKS_MORNING_TIME, 09:00).
	Morning Clock
	// Evening: the list of date-only Tasks due tomorrow (TASKS_EVENING_TIME,
	// 19:00).
	Evening Clock
	// TimedLead: how long before a timed Task is due its reminder is sent
	// (TASKS_TIMED_LEAD_MINUTES, 30).
	TimedLead time.Duration
}

// DefaultSchedule is 08:00 summary, 09:00 morning list, 19:00 evening list,
// and timed reminders 30 minutes ahead.
func DefaultSchedule() Schedule {
	return Schedule{Summary: Clock{8, 0}, Morning: Clock{9, 0}, Evening: Clock{19, 0}, TimedLead: 30 * time.Minute}
}

// Merged reports whether the summary and the morning list are one message:
// at the same time, the summary (which names today's and overdue Tasks too)
// is sent alone.
func (s Schedule) Merged() bool { return s.Summary == s.Morning }

// Kind is what a due notification is.
type Kind string

const (
	// KindDue is a timed Task's reminder.
	KindDue Kind = "due"
	// KindMorning is the morning list of date-only and overdue Tasks.
	KindMorning Kind = "morning"
	// KindEvening is the evening list of tomorrow's date-only Tasks.
	KindEvening Kind = "evening"
	// KindSummary is the daily morning summary.
	KindSummary Kind = "summary"
)

// Task is an open Task with a due date, as notifications see it.
type Task struct {
	ID    string
	Title string
	Due   time.Time
	// DateOnly: due on a day, not at a time of day (see IsDateOnly).
	DateOnly bool
	// Day is the owner-local date it is due on (at midnight UTC).
	Day time.Time
}

// taskFrom reads a stored Task; false when it has no due date.
func taskFrom(row store.Task, owner *time.Location) (Task, bool) {
	if !row.DueAt.Valid {
		return Task{}, false
	}
	due := row.DueAt.Time
	return Task{
		ID: uuid.UUID(row.ID.Bytes).String(), Title: row.Title, Due: due,
		DateOnly: IsDateOnly(due, owner), Day: dateOf(due, owner),
	}, true
}

// IsDateOnly reports whether a due instant stands for a whole day rather
// than a time of day, by the rule the Dashboard shows due dates with: it is
// the start of its day in owner (a date picked without a time is stored as
// the owner-local midnight), or a UTC midnight that falls on the same
// calendar day for the owner (how a bare date is often sent). West of UTC a
// UTC midnight is the previous evening for the owner, a time of day.
func IsDateOnly(due time.Time, owner *time.Location) bool {
	day := dateOf(due, owner)
	if due.Equal(startOfDay(day, owner)) {
		return true
	}
	utc := due.UTC()
	return utc.Hour() == 0 && utc.Minute() == 0 && utc.Second() == 0 && utc.Nanosecond() == 0 &&
		utc.Format(time.DateOnly) == day.Format(time.DateOnly)
}

// Due is one notification that is due.
type Due struct {
	Kind Kind
	// At is when it is due.
	At time.Time
	// Key is the notification dedupe key.
	Key string
	// Day is a daily message's owner-local date (at midnight UTC).
	Day time.Time
	// Task is the Task a timed reminder is for.
	Task Task
}

// dueKey names one timed reminder at the Task's current due time, so a
// changed due time reminds again, while one already sent stays sent.
func dueKey(task Task) string {
	return fmt.Sprintf("tasks:due:%s:%s", task.ID, task.Due.UTC().Format(time.RFC3339))
}

// dailyKey names one day's morning, evening, or summary message.
func dailyKey(kind Kind, day time.Time) string {
	date := day.Format(time.DateOnly)
	switch kind {
	case KindMorning:
		return "tasks:morning:" + date
	case KindEvening:
		return "tasks:evening:" + date
	}
	return "summary:morning:" + date
}

// timedBetween returns the reminders of the timed Tasks among tasks due in
// (from, to]: lead before each due time, an exact duration. Date-only Tasks
// have none (the morning and evening lists name them).
func timedBetween(tasks []Task, lead time.Duration, from, to time.Time) []Due {
	var out []Due
	for _, task := range tasks {
		if task.DateOnly {
			continue
		}
		at := task.Due.Add(-lead)
		if !at.After(from) || at.After(to) {
			continue
		}
		out = append(out, Due{Kind: KindDue, At: at, Key: dueKey(task), Task: task})
	}
	return out
}

// dailyBetween returns the daily messages due in (from, to]: on every
// owner-local day, the summary, the morning list (none when it is merged into
// the summary), and the evening list, each at its wall-clock time that day
// (see wallTime).
func dailyBetween(schedule Schedule, owner *time.Location, from, to time.Time) []Due {
	slots := []struct {
		kind  Kind
		clock Clock
	}{{KindSummary, schedule.Summary}, {KindEvening, schedule.Evening}}
	if !schedule.Merged() {
		slots = append(slots, struct {
			kind  Kind
			clock Clock
		}{KindMorning, schedule.Morning})
	}
	var out []Due
	for day, last := dateOf(from, owner), dateOf(to, owner); !day.After(last); day = day.AddDate(0, 0, 1) {
		for _, slot := range slots {
			at := wallTime(day, slot.clock, owner)
			if !at.After(from) || at.After(to) {
				continue
			}
			out = append(out, Due{Kind: slot.kind, At: at, Key: dailyKey(slot.kind, day), Day: day})
		}
	}
	return out
}

// sortDue orders notifications by due time, then key.
func sortDue(items []Due) {
	sort.SliceStable(items, func(a, b int) bool {
		if !items[a].At.Equal(items[b].At) {
			return items[a].At.Before(items[b].At)
		}
		return items[a].Key < items[b].Key
	})
}

// Lists are the Tasks a daily message on day names, as of at (its time).
type Lists struct {
	// Overdue: due before day (date-only) or before at (timed), oldest
	// first.
	Overdue []Task
	// DateOnlyToday: date-only Tasks due on day.
	DateOnlyToday []Task
	// TimedToday: timed Tasks due on day, at or after at, soonest first.
	TimedToday []Task
	// Tomorrow: date-only Tasks due the day after day.
	Tomorrow []Task
}

// classify sorts open Tasks (in due order) into a day's lists. Tasks due
// later, and timed Tasks due tomorrow, are in none.
func classify(tasks []Task, day, at time.Time) Lists {
	var lists Lists
	tomorrow := day.AddDate(0, 0, 1)
	for _, task := range tasks {
		switch {
		case task.DateOnly && task.Day.Before(day), !task.DateOnly && task.Due.Before(at):
			lists.Overdue = append(lists.Overdue, task)
		case task.DateOnly && task.Day.Equal(day):
			lists.DateOnlyToday = append(lists.DateOnlyToday, task)
		case task.DateOnly && task.Day.Equal(tomorrow):
			lists.Tomorrow = append(lists.Tomorrow, task)
		case !task.DateOnly && task.Day.Equal(day):
			lists.TimedToday = append(lists.TimedToday, task)
		}
	}
	return lists
}

// dateOf is instant's owner-local date, at midnight UTC.
func dateOf(instant time.Time, owner *time.Location) time.Time {
	local := instant.In(owner)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}

// startOfDay is when the owner-local date day begins.
func startOfDay(day time.Time, owner *time.Location) time.Time {
	return wallTime(day, Clock{}, owner)
}

// wallTime is the instant at which owner's clocks show clock on date day
// (at midnight UTC). A time skipped when clocks go forward is taken as the
// same clock time after the jump (02:30 becomes 03:30 CEST); a time that
// occurs twice when clocks go back is the first of the two (02:30 CEST, not
// 02:30 CET), so a message is never later than the owner expects. Calendar
// resolves its localTime reminders the same way.
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
