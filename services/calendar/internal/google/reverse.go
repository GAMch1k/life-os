package google

import (
	"errors"
	"fmt"
	"strings"
	"time"

	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/services/calendar/internal/sources"
)

// The reverse of mapping.go: an event as Life OS holds it, written to
// Google. A synced Google event gets a PATCH with only the fields a change
// names (attendees, conference data, colors, and everything else Calendar
// does not map stay as they are in Google); an exported Life OS event is
// written whole.

// Private extended properties Life OS keeps on Google events.
const (
	// propertyEventID is the Life OS event an exported event copies.
	propertyEventID = "lifeosEventId"
	// propertyPriority is the event's own Life OS priority; Google has no
	// such field.
	propertyPriority = "lifeosPriority"
)

// maxGoogleReminders is how many notifications Google keeps per event.
const maxGoogleReminders = 5

// Fields is an event, or one occurrence of a series, as Life OS holds it.
type Fields struct {
	Title       string
	Description *string
	Location    *string
	AllDay      bool
	// StartAt and EndAt are a timed event's instants; StartDate and
	// EndDate an all-day event's first and last (inclusive) day.
	StartAt, EndAt     time.Time
	StartDate, EndDate time.Time
	// TimeZone is the zone a timed event was scheduled in; nil means the
	// owner's.
	TimeZone            *string
	Recurrence          *sources.Recurrence
	Reminders           []sources.Reminder
	UseDefaultReminders bool
	// Priority is the event's own priority; nil leaves Google's
	// extended property alone on a PATCH, and writes none on an export.
	Priority *string
}

// ErrReminderNotInGoogle: a reminder Google cannot hold.
var ErrReminderNotInGoogle = errors.New("Google Calendar notifications are a number of minutes before the start: a reminder at a time of day can only be kept on an all-day event, at least a day before it")

// ErrTooManyGoogleReminders: Google keeps at most five notifications.
var ErrTooManyGoogleReminders = fmt.Errorf("Google Calendar keeps at most %d notifications per event", maxGoogleReminders)

// zone is the zone a timed event repeats in: its own, else owner.
func (f Fields) zone(owner *time.Location) *time.Location {
	if f.TimeZone != nil {
		if loc := loadZone(*f.TimeZone); loc != nil {
			return loc
		}
	}
	if owner == nil {
		return time.UTC
	}
	return owner
}

// times are Google's start and end: a timed event's instants in its zone
// (with the zone's name, so Google repeats it on that wall clock), an
// all-day event's dates with the end exclusive.
func (f Fields) times(owner *time.Location) (map[string]any, map[string]any) {
	if f.AllDay {
		return map[string]any{"date": f.StartDate.Format(time.DateOnly), "dateTime": nil, "timeZone": nil},
			map[string]any{"date": f.EndDate.AddDate(0, 0, 1).Format(time.DateOnly), "dateTime": nil, "timeZone": nil}
	}
	zone := f.zone(owner)
	return map[string]any{"dateTime": f.StartAt.In(zone).Format(time.RFC3339), "timeZone": zone.String(), "date": nil},
		map[string]any{"dateTime": f.EndAt.In(zone).Format(time.RFC3339), "timeZone": zone.String(), "date": nil}
}

// RRule is the series' rule as an RRULE line. A timed series' until is an
// instant (UTC); an all-day series' is a date, until's date in owner (as
// Calendar compares it).
func RRule(rule sources.Recurrence, allDay bool, owner *time.Location) string {
	parts := []string{"FREQ=" + strings.ToUpper(rule.Frequency)}
	if rule.Interval > 1 {
		parts = append(parts, fmt.Sprintf("INTERVAL=%d", rule.Interval))
	}
	if len(rule.ByWeekday) > 0 {
		parts = append(parts, "BYDAY="+strings.Join(rule.ByWeekday, ","))
	}
	switch {
	case rule.Count > 0:
		parts = append(parts, fmt.Sprintf("COUNT=%d", rule.Count))
	case rule.Until != nil && allDay:
		if owner == nil {
			owner = time.UTC
		}
		parts = append(parts, "UNTIL="+rule.Until.In(owner).Format("20060102"))
	case rule.Until != nil:
		parts = append(parts, "UNTIL="+rule.Until.UTC().Format("20060102T150405Z"))
	}
	return "RRULE:" + strings.Join(parts, ";")
}

// ExDate is the EXDATE line that skips the occurrence with recurrence id
// key: an instant for a timed series, a date for an all-day one.
func ExDate(key string, allDay bool) (string, bool) {
	if allDay {
		day, err := time.Parse(time.DateOnly, key)
		if err != nil {
			return "", false
		}
		return "EXDATE;VALUE=DATE:" + day.Format("20060102"), true
	}
	instant, err := time.Parse(time.RFC3339, key)
	if err != nil {
		return "", false
	}
	return "EXDATE:" + instant.UTC().Format("20060102T150405Z"), true
}

// recurrenceLines are a series' recurrence for Google: its RRULE and the
// kept lines (the EXDATEs Google already has, or the skips of an export).
func recurrenceLines(rule *sources.Recurrence, allDay bool, owner *time.Location, keep []string) []string {
	if rule == nil {
		return []string{}
	}
	return append([]string{RRule(*rule, allDay, owner)}, keep...)
}

// exdateLines are the EXDATE lines of a Google event's recurrence.
func exdateLines(event Event) []string {
	var out []string
	for _, line := range event.Recurrence {
		if propertyName(line) == "EXDATE" {
			out = append(out, cleanLine(line))
		}
	}
	return out
}

// overrideMinutes is a reminder as minutes before the start for Google: a
// relative reminder as is; a localTime reminder only on an all-day event
// (Google counts from midnight of its first day) and at least a minute
// before that midnight.
func overrideMinutes(reminder sources.Reminder, allDay bool) (int, bool) {
	switch reminder.Kind {
	case "relative":
		if reminder.MinutesBefore == nil {
			return 0, false
		}
		return min(max(*reminder.MinutesBefore, 0), maxReminderMinute), true
	case "localTime":
		if !allDay || reminder.DaysBefore == nil || reminder.LocalTime == nil {
			return 0, false
		}
		clock, err := time.Parse("15:04", *reminder.LocalTime)
		if err != nil {
			return 0, false
		}
		minutes := *reminder.DaysBefore*24*60 - (clock.Hour()*60 + clock.Minute())
		if minutes <= 0 || minutes > maxReminderMinute {
			return 0, false
		}
		return minutes, true
	}
	return 0, false
}

// remindersBody is a synced event's reminders for Google: whether the
// calendar's defaults apply, and its own as popup notifications. A reminder
// Google cannot hold is refused rather than dropped.
func remindersBody(f Fields) (map[string]any, error) {
	if len(f.Reminders) > maxGoogleReminders {
		return nil, ErrTooManyGoogleReminders
	}
	overrides := []map[string]any{}
	seen := map[int]bool{}
	for _, reminder := range f.Reminders {
		minutes, ok := overrideMinutes(reminder, f.AllDay)
		if !ok {
			return nil, ErrReminderNotInGoogle
		}
		if seen[minutes] {
			continue
		}
		seen[minutes] = true
		overrides = append(overrides, map[string]any{"method": "popup", "minutes": minutes})
	}
	return map[string]any{"useDefault": f.UseDefaultReminders && !f.AllDay, "overrides": overrides}, nil
}

// exportReminders are an exported event's reminders: the ones that apply
// to it in Life OS (effective) as popup notifications, never the Life OS
// calendar's defaults; reminders Google cannot hold, and those beyond five,
// are left out.
func exportReminders(effective []sources.Reminder, allDay bool) map[string]any {
	overrides := []map[string]any{}
	seen := map[int]bool{}
	for _, reminder := range effective {
		minutes, ok := overrideMinutes(reminder, allDay)
		if !ok || seen[minutes] || len(overrides) == maxGoogleReminders {
			continue
		}
		seen[minutes] = true
		overrides = append(overrides, map[string]any{"method": "popup", "minutes": minutes})
	}
	return map[string]any{"useDefault": false, "overrides": overrides}
}

func textValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// timeFields are the change names that move an event.
var timeFields = []string{"allDay", "startAt", "endAt", "startDate", "endDate", "timeZone"}

// EventPatch is the PATCH body for a synced Google event: the fields a
// change names (present, the API's field names), from f, the event after
// the change. A series' recurrence keeps keep (its EXDATEs). Status is the
// caller's (a cancellation deletes the event at Google).
func EventPatch(f Fields, present map[string]bool, owner *time.Location, keep []string) (map[string]any, error) {
	body := map[string]any{}
	if present["title"] {
		body["summary"] = f.Title
	}
	if present["description"] {
		body["description"] = textValue(f.Description)
	}
	if present["location"] {
		body["location"] = textValue(f.Location)
	}
	for _, name := range timeFields {
		if present[name] {
			body["start"], body["end"] = f.times(owner)
			break
		}
	}
	if present["recurrence"] {
		body["recurrence"] = recurrenceLines(f.Recurrence, f.AllDay, owner, keep)
	} else if present["allDay"] || present["startAt"] || present["startDate"] || present["timeZone"] {
		// A series moved in time: Google keeps its rule; the until of an
		// all-day series and a timed one are written differently.
		if f.Recurrence != nil {
			body["recurrence"] = recurrenceLines(f.Recurrence, f.AllDay, owner, keep)
		}
	}
	if present["reminders"] || present["useDefaultReminders"] {
		reminders, err := remindersBody(f)
		if err != nil {
			return nil, err
		}
		body["reminders"] = reminders
	}
	if present["priority"] {
		body["extendedProperties"] = map[string]any{"private": map[string]any{propertyPriority: textValue(f.Priority)}}
	}
	return body, nil
}

// ExportID is the Google event id of a Life OS event's copy: "lifeos" and
// the event's UUID in hex (Google ids are base32hex, a-v and 0-9), so an
// export repeated after a crash finds the copy instead of adding another.
func ExportID(id googleuuid.UUID) string {
	return "lifeos" + strings.ReplaceAll(id.String(), "-", "")
}

// exportedEventID is the Life OS event a Google event copies, if it is an
// export.
func exportedEventID(event Event) (googleuuid.UUID, bool) {
	value := event.private(propertyEventID)
	if value == "" {
		return googleuuid.UUID{}, false
	}
	id, err := googleuuid.Parse(value)
	return id, err == nil
}

// ExportBody is the whole Google event for a Life OS event: f, its
// effective reminders, and the recurrence with skips (exdates).
func ExportBody(id googleuuid.UUID, f Fields, effective []sources.Reminder, owner *time.Location, exdates []string) map[string]any {
	start, end := f.times(owner)
	private := map[string]any{propertyEventID: id.String(), propertyPriority: textValue(f.Priority)}
	return map[string]any{
		"summary":            f.Title,
		"description":        textValue(f.Description),
		"location":           textValue(f.Location),
		"start":              start,
		"end":                end,
		"recurrence":         recurrenceLines(f.Recurrence, f.AllDay, owner, exdates),
		"reminders":          exportReminders(effective, f.AllDay),
		"status":             "confirmed",
		"extendedProperties": map[string]any{"private": private},
	}
}

// instanceBody is a changed occurrence of an exported series as Google's
// instance: its own title, notes, place, times, and reminders.
func instanceBody(f Fields, effective []sources.Reminder, owner *time.Location) map[string]any {
	start, end := f.times(owner)
	return map[string]any{
		"summary": f.Title, "description": textValue(f.Description), "location": textValue(f.Location),
		"start": start, "end": end, "reminders": exportReminders(effective, f.AllDay), "status": "confirmed",
	}
}

// originalStart is how Google names an occurrence's original start: the
// recurrence id as is (an RFC 3339 instant, or a date).
func originalStart(key string) string { return key }

// instanceID is Google's id of the instance of a recurring event that
// originally starts at key: "<id>_20261012T070000Z", or "<id>_20261024".
func instanceID(eventID, key string, allDay bool) string {
	if allDay {
		day, err := time.Parse(time.DateOnly, key)
		if err == nil {
			return eventID + "_" + day.Format("20060102")
		}
	}
	instant, err := time.Parse(time.RFC3339, key)
	if err != nil {
		return eventID + "_" + key
	}
	return eventID + "_" + instant.UTC().Format("20060102T150405Z")
}
