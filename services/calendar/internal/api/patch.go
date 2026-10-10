package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
)

// updatableFields are the UpdateEventRequest properties; other keys in a
// PATCH body (such as source or id) are ignored.
var updatableFields = map[string]bool{
	"title": true, "description": true, "location": true, "allDay": true,
	"startAt": true, "endAt": true, "startDate": true, "endDate": true, "timeZone": true,
	"recurrence": true, "reminders": true, "useDefaultReminders": true, "status": true,
}

// requiredValueFields cannot be cleared: null is not a value for them.
var requiredValueFields = []string{"title", "allDay", "useDefaultReminders", "status"}

// eventPatch is a decoded PATCH body. A generated pointer field cannot tell an
// absent property from an explicit null, so present records which properties
// the body names: present with a nil value means "clear".
type eventPatch struct {
	body    generated.UpdateEventRequest
	present map[string]bool
}

func decodePatch(raw []byte) (eventPatch, error) {
	var properties map[string]json.RawMessage
	if err := json.Unmarshal(raw, &properties); err != nil || properties == nil {
		return eventPatch{}, errors.New("invalid event request")
	}
	var body generated.UpdateEventRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		return eventPatch{}, errors.New("invalid event request")
	}
	patch := eventPatch{body: body, present: map[string]bool{}}
	for name := range properties {
		if updatableFields[name] {
			patch.present[name] = true
		}
	}
	if len(patch.present) == 0 {
		return eventPatch{}, errors.New("the update names no event field")
	}
	for _, name := range requiredValueFields {
		if patch.present[name] && string(properties[name]) == "null" {
			return eventPatch{}, fmt.Errorf("%s cannot be null", name)
		}
	}
	return patch, nil
}

// onlyReminders reports whether the patch changes nothing but the
// reminders (its own and whether the defaults apply), the one thing the
// owner may change on an imported event.
func (p eventPatch) onlyReminders() bool {
	for name := range p.present {
		if name != "reminders" && name != "useDefaultReminders" {
			return false
		}
	}
	return true
}

// apply merges the patch into current and validates the result. now stamps
// a cancellation.
func (p eventPatch) apply(current eventFields, now time.Time) (eventFields, error) {
	next := current
	body := p.body
	if p.present["allDay"] && *body.AllDay != current.AllDay {
		// Switching kind drops the other kind's fields; the body must supply
		// the new kind's start and end.
		if *body.AllDay {
			next.StartAt, next.EndAt, next.TimeZone = nil, nil, nil
		} else {
			next.StartDate, next.EndDate = nil, nil
		}
		next.AllDay = *body.AllDay
	}
	if p.present["title"] {
		next.Title = *body.Title
	}
	set := func(name string, target **string, value *string) {
		if p.present[name] {
			*target = value
		}
	}
	set("description", &next.Description, body.Description)
	set("location", &next.Location, body.Location)
	set("timeZone", &next.TimeZone, body.TimeZone)
	if p.present["startAt"] {
		next.StartAt = body.StartAt
	}
	if p.present["endAt"] {
		next.EndAt = body.EndAt
	}
	if p.present["startDate"] {
		next.StartDate = body.StartDate
	}
	if p.present["endDate"] {
		next.EndDate = body.EndDate
	}
	if p.present["recurrence"] {
		next.Recurrence = body.Recurrence
	}
	if p.present["reminders"] {
		// null returns the event to its defaults alone.
		next.Reminders = remindersOrNone(body.Reminders)
		if body.Reminders == nil {
			next.UseDefaultReminders = true
		}
	}
	if p.present["useDefaultReminders"] {
		next.UseDefaultReminders = *body.UseDefaultReminders
	}
	if p.present["status"] {
		switch *body.Status {
		case generated.Cancelled:
			if current.Status != generated.Cancelled {
				cancelledAt := now.UTC()
				next.CancelledAt = &cancelledAt
			}
		case generated.Active:
			next.CancelledAt, next.RemovedFromSourceAt = nil, nil
		default:
			return eventFields{}, errors.New("status must be active or cancelled")
		}
		next.Status = *body.Status
	}
	return next, next.validate()
}
