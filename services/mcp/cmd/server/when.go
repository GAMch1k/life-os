package main

import (
	"fmt"
	"strings"
	"time"
)

// owner is the owner's timezone and clock. The owning services store
// instants (UTC); the tools also show every time on the owner's wall clock
// and move Tasks and events there, so a caller never converts offsets or
// does daylight-saving arithmetic. The zone is LIFE_OS_TIMEZONE (else UTC);
// a call may name another one, such as the capturing browser's.
type owner struct {
	zone *time.Location
	now  func() time.Time
}

func (o owner) clock() time.Time {
	if o.now != nil {
		return o.now()
	}
	return time.Now()
}

// location is the zone a call names, else the owner's.
func (o owner) location(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		if o.zone == nil {
			return time.UTC, nil
		}
		return o.zone, nil
	}
	zone, err := time.LoadLocation(name)
	if err != nil || name == "Local" {
		return nil, fmt.Errorf("timeZone %q is not an IANA timezone, such as Europe/Berlin", name)
	}
	return zone, nil
}

// ownerZone is LIFE_OS_TIMEZONE as a location: UTC when unset, and UTC (with
// the reason) when it names no zone, as in the other services.
func ownerZone(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return time.UTC, nil
	}
	zone, err := time.LoadLocation(name)
	if err != nil || name == "Local" {
		return time.UTC, fmt.Errorf("LIFE_OS_TIMEZONE %q is not an IANA timezone; using UTC", name)
	}
	return zone, nil
}

const (
	dayText     = "Mon 2 Jan 2006"
	instantText = "Mon 2 Jan 2006, 15:04 MST"
)

// civil is the calendar date of a wall-clock time, as a UTC midnight, so
// that date arithmetic never meets a daylight-saving change.
func civil(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// startOfDay is the owner-local midnight of a date: how a date without a
// time is stored (the Dashboard's and Tasks' rule; Go's normalization when
// midnight does not exist that day).
func startOfDay(day time.Time, zone *time.Location) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, zone)
}

// wallClock is the instant a wall-clock time on a date has in zone, by the
// iCalendar rule (RFC 5545, 3.3.5): a time that occurs twice, when the
// clocks go back, is its first occurrence, and a time that does not occur,
// when they go forward, is read with the offset before the gap (02:30 on the
// last Sunday of March in Berlin is 03:30 CEST).
func wallClock(day time.Time, hour, minute, second int, zone *time.Location) time.Time {
	naive := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, second, 0, time.UTC)
	want := naive.Format("2006-01-02T15:04:05")
	_, before := naive.Add(-24 * time.Hour).In(zone).Zone()
	_, after := naive.Add(24 * time.Hour).In(zone).Zone()
	var found time.Time
	for _, offset := range []int{before, after} {
		candidate := naive.Add(-time.Duration(offset) * time.Second)
		if candidate.In(zone).Format("2006-01-02T15:04:05") == want && (found.IsZero() || candidate.Before(found)) {
			found = candidate
		}
	}
	if found.IsZero() {
		found = naive.Add(-time.Duration(before) * time.Second)
	}
	return found.In(zone)
}

// isDateOnly reports whether a Task's due instant stands for a whole day,
// by the rule Tasks and the Dashboard use: it is the owner-local midnight,
// or a UTC midnight that falls on the same calendar day for the owner (how a
// bare date is often sent). West of UTC a UTC midnight is the previous
// evening for the owner, a time of day.
func isDateOnly(due time.Time, zone *time.Location) bool {
	local := due.In(zone)
	if due.Equal(startOfDay(local, zone)) {
		return true
	}
	utc := due.UTC()
	return utc.Hour() == 0 && utc.Minute() == 0 && utc.Second() == 0 && utc.Nanosecond() == 0 &&
		utc.Format(time.DateOnly) == local.Format(time.DateOnly)
}

// localInstant is an instant as the owner reads it, with its UTC form.
func localInstant(at time.Time, zone *time.Location) map[string]any {
	local := at.In(zone)
	return map[string]any{
		"date":    local.Format(time.DateOnly),
		"weekday": local.Weekday().String(),
		"time":    local.Format("15:04"),
		"local":   local.Format(time.RFC3339),
		"utc":     at.UTC().Format(time.RFC3339),
		"text":    local.Format(instantText),
	}
}

// localDate is a date without a time of day.
func localDate(day time.Time) map[string]any {
	return map[string]any{"date": day.Format(time.DateOnly), "weekday": day.Weekday().String(), "text": day.Format(dayText)}
}

func instant(value any) (time.Time, bool) {
	text, _ := value.(string)
	at, err := time.Parse(time.RFC3339, text)
	return at, err == nil
}

func date(value any) (time.Time, bool) {
	text, _ := value.(string)
	day, err := time.Parse(time.DateOnly, text)
	return day, err == nil
}

func clone(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+8)
	for key, value := range in {
		out[key] = value
	}
	return out
}

// taskDue is a Task's due date as the owner reads it: the date, and the time
// only when it has one (hasTime). An open Task is overdue from the day after
// a date-only due date, and once a due time has passed.
func taskDue(due time.Time, completed bool, zone *time.Location, now time.Time) map[string]any {
	var out map[string]any
	if isDateOnly(due, zone) {
		day := civil(due.In(zone))
		out = localDate(day)
		out["text"] = day.Format(dayText) + " (no time)"
		out["hasTime"] = false
		if !completed {
			out["overdue"] = civil(now.In(zone)).After(day)
		}
	} else {
		out = localInstant(due, zone)
		out["hasTime"] = true
		if !completed {
			out["overdue"] = now.After(due)
		}
	}
	return out
}

// dueText says when a Task is due, for a summary of a move.
func dueText(task map[string]any, zone *time.Location) string {
	due, ok := instant(task["dueAt"])
	if !ok {
		return "no due date"
	}
	if isDateOnly(due, zone) {
		return due.In(zone).Format(dayText) + " (no time)"
	}
	return due.In(zone).Format(instantText)
}

// taskDetails is a Task as Tasks returns it plus what a reader needs without
// converting anything: the owner's timezone, due (null when unscheduled:
// date, weekday, hasTime, and for a timed Task time, local, and utc;
// overdue for an open Task), the created, updated, and completed times on
// the owner's clock, and the recurrence in words.
func taskDetails(task map[string]any, zone *time.Location, now time.Time) map[string]any {
	out := clone(task)
	out["timeZone"] = zone.String()
	completed, _ := task["completed"].(bool)
	if due, ok := instant(task["dueAt"]); ok {
		out["due"] = taskDue(due, completed, zone, now)
	} else {
		out["due"] = nil
	}
	for _, field := range []string{"createdAt", "updatedAt", "completedAt"} {
		if at, ok := instant(task[field]); ok {
			out[field+"Local"] = at.In(zone).Format(time.RFC3339)
		}
	}
	if recurrence, ok := task["recurrence"].(map[string]any); ok {
		out["recurring"] = true
		out["recurrenceText"] = recurrenceText(recurrence, zone)
	} else {
		out["recurring"] = false
	}
	return out
}

// eventDetails is a Calendar event as Calendar returns it plus its times on
// the owner's clock: start and end (a timed event's with time, local, and
// utc; the end is exclusive; an all-day event's dates, the end its last
// day), durationMinutes or days, when in words, whether it repeats, and the
// recurrence in words. Location, reminders, priority, status, and occurrence
// are Calendar's own fields.
func eventDetails(event map[string]any, zone *time.Location) map[string]any {
	out := clone(event)
	out["timeZone"] = zone.String()
	if allDay, _ := event["allDay"].(bool); allDay {
		start, okStart := date(event["startDate"])
		end, okEnd := date(event["endDate"])
		if okStart && okEnd {
			out["start"] = localDate(start)
			out["end"] = localDate(end)
			out["days"] = int(end.Sub(start).Hours()/24) + 1
		}
	} else {
		start, okStart := instant(event["startAt"])
		end, okEnd := instant(event["endAt"])
		if okStart && okEnd {
			out["start"] = localInstant(start, zone)
			out["end"] = localInstant(end, zone)
			out["durationMinutes"] = int(end.Sub(start).Minutes())
		}
	}
	if when := eventWhen(event, zone); when != "" {
		out["when"] = when
	}
	recurrence, _ := event["recurrence"].(map[string]any)
	out["recurring"] = recurrence != nil || event["occurrence"] != nil
	if recurrence != nil {
		out["recurrenceText"] = recurrenceText(recurrence, zone)
	}
	return out
}

// eventWhen reads like "Fri 23 Oct 2026, 15:00–16:00 CEST" or
// "Sat 24 Oct 2026 (all day)".
func eventWhen(event map[string]any, zone *time.Location) string {
	if allDay, _ := event["allDay"].(bool); allDay {
		start, okStart := date(event["startDate"])
		end, okEnd := date(event["endDate"])
		switch {
		case !okStart:
			return ""
		case !okEnd || !end.After(start):
			return start.Format(dayText) + " (all day)"
		default:
			return start.Format(dayText) + " – " + end.Format(dayText) + " (all day)"
		}
	}
	start, ok := instant(event["startAt"])
	if !ok {
		return ""
	}
	start = start.In(zone)
	end, ok := instant(event["endAt"])
	if !ok {
		return start.Format(instantText)
	}
	end = end.In(zone)
	if end.Format(time.DateOnly) == start.Format(time.DateOnly) && end.Format("MST") == start.Format("MST") {
		return start.Format(dayText+", 15:04") + "–" + end.Format("15:04 MST")
	}
	return start.Format(instantText) + " – " + end.Format(instantText)
}

// recurrenceText reads like "every 2 weeks on MO, TH, until Sat 31 Oct 2026".
func recurrenceText(recurrence map[string]any, zone *time.Location) string {
	frequency, _ := recurrence["frequency"].(string)
	unit := map[string]string{"daily": "day", "weekly": "week", "monthly": "month", "yearly": "year"}[frequency]
	if unit == "" {
		return ""
	}
	interval := 1
	if value, ok := recurrence["interval"].(float64); ok && value > 1 {
		interval = int(value)
	}
	text := "every " + unit
	if interval > 1 {
		text = fmt.Sprintf("every %d %ss", interval, unit)
	}
	if days, ok := recurrence["byWeekday"].([]any); ok && len(days) > 0 {
		names := make([]string, 0, len(days))
		for _, day := range days {
			if name, ok := day.(string); ok {
				names = append(names, name)
			}
		}
		text += " on " + strings.Join(names, ", ")
	}
	if count, ok := recurrence["count"].(float64); ok && count > 0 {
		text += fmt.Sprintf(", %d times", int(count))
	}
	if until, ok := instant(recurrence["until"]); ok {
		text += ", until " + until.In(zone).Format(dayText)
	}
	return text
}

// withItems adds details to every item of a list answer.
func withItems(data map[string]any, zone *time.Location, details func(map[string]any) map[string]any) map[string]any {
	items, ok := data["items"].([]any)
	if !ok {
		return data
	}
	out := clone(data)
	enriched := make([]any, len(items))
	for index, item := range items {
		if entity, ok := item.(map[string]any); ok {
			enriched[index] = details(entity)
		} else {
			enriched[index] = item
		}
	}
	out["items"] = enriched
	out["timeZone"] = zone.String()
	return out
}
