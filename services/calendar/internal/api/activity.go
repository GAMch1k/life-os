package api

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// Activity event types for changes to events made through the API. Source
// refreshes record their own summary events (see sources.Refresher).
const (
	eventCreated     = "calendar.event.created"
	eventUpdated     = "calendar.event.updated"
	eventRescheduled = "calendar.event.rescheduled"
	eventCancelled   = "calendar.event.cancelled"
	eventRestored    = "calendar.event.restored"
	eventDeleted     = "calendar.event.deleted"
)

// eventSubject is the Activity subject type of a calendar event.
const eventSubject = "calendar_event"

// WithActivity records every event change made through the API in Activity,
// attributed to the request's actor and tied to its correlation and
// causation. A nil emitter records nothing.
func (h *Handler) WithActivity(emitter *activity.Emitter) *Handler {
	h.activity = emitter
	return h
}

// timeFields are the changes that move an event in time.
var timeFields = map[string]bool{"allDay": true, "startAt": true, "endAt": true, "startDate": true, "endDate": true}

// classifyUpdate names what an update did, by comparing the stored event
// before and after it: a cancellation or restoration first, then a move in
// time, else an edit. It returns no type when nothing changed.
func classifyUpdate(before, after store.Event) (string, []string) {
	changes := changedFields(before, after)
	if len(changes) == 0 {
		return "", nil
	}
	if before.Status != after.Status {
		if after.Status == "cancelled" {
			return eventCancelled, changes
		}
		return eventRestored, changes
	}
	for _, change := range changes {
		if timeFields[change] {
			return eventRescheduled, changes
		}
	}
	return eventUpdated, changes
}

// changedFields lists the API fields that differ between two stored states.
func changedFields(before, after store.Event) []string {
	var changes []string
	add := func(name string, changed bool) {
		if changed {
			changes = append(changes, name)
		}
	}
	add("title", before.Title != after.Title)
	add("description", !sameString(before.Description, after.Description))
	add("location", !sameString(before.Location, after.Location))
	add("allDay", before.AllDay != after.AllDay)
	add("startAt", !sameInstant(before.StartAt, after.StartAt))
	add("endAt", !sameInstant(before.EndAt, after.EndAt))
	add("startDate", !sameDate(before.StartDate, after.StartDate))
	add("endDate", !sameDate(before.EndDate, after.EndDate))
	add("timeZone", !sameString(before.TimeZone, after.TimeZone))
	add("recurrence", !sameString(before.RecurrenceFrequency, after.RecurrenceFrequency) ||
		!sameInt(before.RecurrenceInterval, after.RecurrenceInterval) || !sameInstant(before.RecurrenceUntil, after.RecurrenceUntil) ||
		!slices.Equal(before.RecurrenceByWeekday, after.RecurrenceByWeekday) || !sameInt(before.RecurrenceCount, after.RecurrenceCount))
	add("reminders", !bytes.Equal(before.Reminders, after.Reminders) || before.UseDefaultReminders != after.UseDefaultReminders)
	add("priority", !sameString(before.Priority, after.Priority))
	add("status", before.Status != after.Status)
	return changes
}

func sameString(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func sameInt(a, b *int32) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func sameInstant(a, b pgtype.Timestamptz) bool {
	return a.Valid == b.Valid && (!a.Valid || a.Time.Equal(b.Time))
}

func sameDate(a, b pgtype.Date) bool {
	return a.Valid == b.Valid && (!a.Valid || a.Time.Equal(b.Time))
}

// recordEvent records one event change in Activity after it has persisted.
// previous is the stored state before an update (nil for a creation or a
// deletion) and changes the fields the update changed.
func (h *Handler) recordEvent(c fiber.Ctx, eventType string, item store.Event, previous *store.Event, changes []string) {
	// Deterministic ids: an idempotent create replay or a re-sent event
	// records nothing new. An update is identified by the state it wrote.
	var version []string
	if previous != nil {
		version = append(version, item.UpdatedAt.Time.UTC().Format(time.RFC3339Nano))
	}
	h.emitEvent(c, eventType, item, previous, changes, nil, version...)
}

// emitEvent records one event change: extra adds to its data, and version
// tells repeated changes of the same event apart in its deterministic id.
func (h *Handler) emitEvent(c fiber.Ctx, eventType string, item store.Event, previous *store.Event, changes []string, extra map[string]any, version ...string) {
	if h.activity == nil {
		return
	}
	trace := activity.FromRequest(c)
	id := uuid(item.ID).String()
	eventID := activity.EventID(eventType, append([]string{id}, version...)...)
	data := eventData(item)
	for key, value := range extra {
		data[key] = value
	}
	if previous != nil {
		data["changes"] = changes
		if previous.Title != item.Title {
			data["previousTitle"] = activity.Preview(previous.Title, 200)
		}
		if eventType == eventRescheduled {
			before := map[string]any{"allDay": previous.AllDay}
			addTimes(before, *previous)
			data["previous"] = before
		}
	}
	h.activity.Emit(c.Context(), activity.Event{
		ID:            eventID,
		Type:          eventType,
		Subject:       activity.Subject{Type: eventSubject, ID: id},
		CorrelationID: trace.CorrelationID,
		CausationID:   trace.CausationID,
		Actor:         trace.Actor,
		Outcome:       activity.Succeeded,
		Summary:       activity.Preview(eventSummary(eventType, item, changes, h.ownerLocation), 500),
		Data:          data,
	})
}

// eventData is what Activity keeps about the event: enough to label it and
// to open the Calendar at its date, never its description or reminders.
func eventData(item store.Event) map[string]any {
	data := map[string]any{
		"title": activity.Preview(item.Title, 200), "allDay": item.AllDay,
		"status": item.Status, "sourceKind": item.SourceKind,
	}
	addTimes(data, item)
	if item.TimeZone != nil {
		data["timeZone"] = *item.TimeZone
	}
	if item.SourceID.Valid {
		data["sourceId"] = uuid(item.SourceID).String()
	}
	if item.RecurrenceFrequency != nil {
		data["recurrence"] = *item.RecurrenceFrequency
	}
	return data
}

func addTimes(data map[string]any, item store.Event) {
	if item.AllDay {
		if item.StartDate.Valid {
			data["startDate"] = item.StartDate.Time.Format(time.DateOnly)
		}
		if item.EndDate.Valid {
			data["endDate"] = item.EndDate.Time.Format(time.DateOnly)
		}
		return
	}
	if item.StartAt.Valid {
		data["startAt"] = item.StartAt.Time.UTC().Format(time.RFC3339)
	}
	if item.EndAt.Valid {
		data["endAt"] = item.EndAt.Time.UTC().Format(time.RFC3339)
	}
}

// changeLabels name changed fields in summaries.
var changeLabels = map[string]string{
	"title": "title", "description": "description", "location": "location", "timeZone": "time zone",
	"recurrence": "repetition", "reminders": "reminders", "priority": "priority",
}

// eventSummary is the change as a sentence, with times in zone (the owner's).
func eventSummary(eventType string, item store.Event, changes []string, zone *time.Location) string {
	title := activity.Preview(item.Title, 120)
	when := eventWhen(item, zone)
	switch eventType {
	case eventCreated:
		return fmt.Sprintf("Event “%s” created for %s", title, when)
	case eventRescheduled:
		return fmt.Sprintf("Event “%s” moved to %s", title, when)
	case eventCancelled:
		return fmt.Sprintf("Event “%s” on %s cancelled", title, when)
	case eventRestored:
		return fmt.Sprintf("Event “%s” on %s restored", title, when)
	case eventDeleted:
		return fmt.Sprintf("Event “%s” on %s deleted", title, when)
	}
	var labels []string
	for _, change := range changes {
		if label, ok := changeLabels[change]; ok {
			labels = append(labels, label)
		}
	}
	if len(labels) == 0 {
		return fmt.Sprintf("Event “%s” updated", title)
	}
	return fmt.Sprintf("Event “%s” updated: %s", title, strings.Join(labels, ", "))
}

// eventWhen is when the event (or a recurring event's first occurrence)
// happens, such as "Tue 17 Nov 2026, 09:30 CET" or "Sat 17 Oct 2026, all day".
func eventWhen(item store.Event, zone *time.Location) string {
	if zone == nil {
		zone = time.UTC
	}
	var when string
	switch {
	case item.AllDay && item.StartDate.Valid:
		when = item.StartDate.Time.Format("Mon 2 Jan 2006")
		if item.EndDate.Valid && item.EndDate.Time.After(item.StartDate.Time) {
			when += " – " + item.EndDate.Time.Format("Mon 2 Jan 2006")
		}
		when += ", all day"
	case item.StartAt.Valid:
		when = item.StartAt.Time.In(zone).Format("Mon 2 Jan 2006, 15:04 MST")
	default:
		when = "an unknown time"
	}
	if item.RecurrenceFrequency != nil {
		when += ", " + repeats(*item.RecurrenceFrequency, item.RecurrenceInterval)
	}
	return when
}

func repeats(frequency string, interval *int32) string {
	units := map[string]string{"daily": "day", "weekly": "week", "monthly": "month", "yearly": "year"}
	if interval == nil || *interval <= 1 || units[frequency] == "" {
		return "repeating " + frequency
	}
	return fmt.Sprintf("repeating every %d %ss", *interval, units[frequency])
}

// occurrenceRow is one occurrence as a stored event, for Activity: the
// series' row with the occurrence's fields, its own status, and no rule (an
// occurrence does not repeat), so its summary names the occurrence's time.
func occurrenceRow(series store.Event, fields eventFields) store.Event {
	row := series
	row.Title, row.Description, row.Location = fields.Title, fields.Description, fields.Location
	row.StartAt, row.EndAt = timestamp(fields.StartAt), timestamp(fields.EndAt)
	row.StartDate, row.EndDate = datePointer(fields.StartDate), datePointer(fields.EndDate)
	if reminders, err := encodeReminders(&fields.Reminders); err == nil {
		row.Reminders = reminders
	}
	row.UseDefaultReminders = fields.UseDefaultReminders
	// The occurrence's priority as it applies (a series' own priority is
	// stored only when it is not the default), so changes compare as seen.
	priority := string(priorityOr(fields.Priority, defaultPriority))
	row.Priority = &priority
	row.Status, row.CancelledAt = string(fields.Status), timestamp(fields.CancelledAt)
	row.RecurrenceFrequency, row.RecurrenceInterval, row.RecurrenceUntil = nil, nil, pgtype.Timestamptz{}
	row.RecurrenceByWeekday, row.RecurrenceCount = nil, nil
	return row
}

// recordOccurrence records a change of one occurrence (This event): its
// skip, restoration, move, or edit, with its recurrence id and its times.
// stamp tells repeated changes of the occurrence apart.
func (h *Handler) recordOccurrence(c fiber.Ctx, series store.Event, key string, before, after eventFields, stamp time.Time) {
	previous, current := occurrenceRow(series, before), occurrenceRow(series, after)
	eventType, changes := classifyUpdate(previous, current)
	if eventType == "" {
		return
	}
	extra := map[string]any{"recurrenceId": key}
	if series.RecurrenceFrequency != nil {
		extra["recurrence"] = *series.RecurrenceFrequency
	}
	h.emitEvent(c, eventType, current, &previous, changes, extra, key, stamp.UTC().Format(time.RFC3339Nano))
}
