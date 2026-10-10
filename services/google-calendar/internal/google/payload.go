package google

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/GAMch1k/life-os/services/google-calendar/internal/calendar"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/sources"
)

// payload is an event as Calendar's change feed gives it: its current
// state and a series' occurrence changes and skips. Exports and waiting
// edits keep it (google_exports.payload, google_pushes.payload).
type payload struct {
	Event      calendar.Event                 `json:"event"`
	Exceptions []calendar.OccurrenceException `json:"exceptions"`
}

func decodePayload(raw []byte) (payload, bool, error) {
	if len(raw) == 0 {
		return payload{}, false, nil
	}
	var out payload
	if err := json.Unmarshal(raw, &out); err != nil {
		return payload{}, false, fmt.Errorf("decode stored event: %w", err)
	}
	return out, true, nil
}

func encodePayload(event calendar.Event, exceptions []calendar.OccurrenceException) ([]byte, error) {
	if exceptions == nil {
		exceptions = []calendar.OccurrenceException{}
	}
	return json.Marshal(payload{Event: event, Exceptions: exceptions})
}

// ownPriority is an event's own priority: none when it follows its default.
func ownPriority(event calendar.Event) *string {
	if event.Priority == "" || event.Priority == event.DefaultPriority {
		return nil
	}
	value := event.Priority
	return &value
}

// toReminders converts Calendar's reminders.
func toReminders(list []calendar.Reminder) []sources.Reminder {
	out := make([]sources.Reminder, 0, len(list))
	for _, reminder := range list {
		out = append(out, sources.Reminder(reminder))
	}
	return out
}

// eventFields is a Calendar event as Fields.
func eventFields(event calendar.Event) Fields {
	fields := Fields{
		Title: event.Title, Description: event.Description, Location: event.Location, AllDay: event.AllDay,
		TimeZone: event.TimeZone, UseDefaultReminders: event.UseDefaultReminders, Priority: ownPriority(event),
		Reminders: toReminders(event.Reminders),
	}
	if event.AllDay {
		fields.StartDate, fields.EndDate = parseDate(event.StartDate), parseDate(event.EndDate)
	} else {
		if event.StartAt != nil {
			fields.StartAt = event.StartAt.UTC()
		}
		if event.EndAt != nil {
			fields.EndAt = event.EndAt.UTC()
		}
	}
	if rule := event.Recurrence; rule != nil {
		recurrence := &sources.Recurrence{Frequency: rule.Frequency, Interval: max(rule.Interval, 1), ByWeekday: rule.ByWeekday}
		if rule.Count != nil {
			recurrence.Count = *rule.Count
		}
		if rule.Until != nil {
			until := rule.Until.UTC()
			recurrence.Until = &until
		}
		fields.Recurrence = recurrence
	}
	return fields
}

func parseDate(value *string) time.Time {
	if value == nil {
		return time.Time{}
	}
	day, err := time.Parse(time.DateOnly, *value)
	if err != nil {
		return time.Time{}
	}
	return day
}

// exceptionsByKey are a payload's occurrence exceptions by recurrence id,
// decoded.
func exceptionsByKey(list []calendar.OccurrenceException) (map[string]sources.Exception, error) {
	out := make(map[string]sources.Exception, len(list))
	for _, row := range list {
		changes, err := decodeChanges(row.Changes)
		if err != nil {
			return nil, err
		}
		out[row.RecurrenceID] = sources.Exception{Changes: changes.Changes, Cancelled: row.Cancelled}
	}
	return out, nil
}
