package google

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/calendar"
	store "github.com/GAMch1k/life-os/services/google-calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/sources"
)

// The Life OS calendar holds the copies of exported Life OS events. They
// are not imported as Google events: the Life OS event is the owner's, and
// a change made to its copy in Google (moved on the phone, renamed,
// deleted) is applied to it through Calendar's events API, as
// system:google-calendar, so Google holds the latest version of every
// event in a Google calendar. Events created in Google in the Life OS
// calendar are imported like any synced calendar's.

// isLifeOSCalendar reports whether calendar is the connection's Life OS
// calendar.
func (s *Syncer) isLifeOSCalendar(ctx context.Context, q *store.Queries, calendarRow store.GoogleCalendar) (bool, error) {
	connection, err := q.GetGoogleConnection(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return connection.ID == calendarRow.ConnectionID && connection.LifeosCalendarID != nil && *connection.LifeosCalendarID == calendarRow.GoogleCalendarID, nil
}

// splitCopies separates the copies of exported Life OS events (and their
// instances) from the calendar's other events.
func splitCopies(events []Event) (others, copies []Event) {
	exported := map[string]bool{}
	for _, event := range events {
		if _, ok := exportedEventID(event); ok && event.RecurringEventID == "" {
			exported[event.ID] = true
		}
	}
	for _, event := range events {
		if exported[event.ID] || exported[event.RecurringEventID] {
			copies = append(copies, event)
		} else {
			others = append(others, event)
		}
	}
	return others, copies
}

// followCopies applies to each exported Life OS event the changes the
// listing reports for its copy (the copy itself or one of its instances
// changed in Google), and cancels those whose copy it reports deleted. A
// copy that only carries this service's own last write changes nothing, so
// a Life OS change still waiting to be exported is not undone.
func (s *Syncer) followCopies(ctx context.Context, q *store.Queries, calendarRow store.GoogleCalendar, m mapper, copies []Event, listed *listingChanges, now time.Time) error {
	masters := map[string]Event{}
	instanceChanged := map[string]bool{}
	for _, event := range copies {
		if event.RecurringEventID == "" {
			masters[event.ID] = event
		} else if listed.changed[event.ID] && !listed.full {
			instanceChanged[event.RecurringEventID] = true
		}
	}
	items, _ := m.derive(copies)
	for _, item := range items {
		master, ok := masters[item.ExternalUID]
		if !ok {
			continue // an instance Life OS's series does not have
		}
		id, _ := exportedEventID(master)
		export, err := q.GetGoogleExport(ctx, pgID(id))
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		masterChanged := (export.Etag == nil || *export.Etag != master.ETag) && (listed.full || listed.changed[master.ID])
		if export.Deleted || !masterChanged && !instanceChanged[master.ID] {
			continue // being deleted, or nothing new from Google
		}
		if err := s.followCopy(ctx, q, export, master, item, now); err != nil {
			return err
		}
	}
	linked, err := q.ListLinkedGoogleExports(ctx, &calendarRow.GoogleCalendarID)
	if err != nil {
		return err
	}
	for _, export := range linked {
		if export.Deleted || export.GoogleEventID == nil {
			continue
		}
		_, listedCopy := masters[*export.GoogleEventID]
		deleted := slices.Contains(listed.gone, *export.GoogleEventID) ||
			listed.full && !listedCopy && export.ExportedAt.Valid && export.ExportedAt.Time.Before(listed.startedAt)
		if !deleted {
			continue
		}
		if err := s.copyDeleted(ctx, q, export, now); err != nil {
			return err
		}
	}
	return nil
}

// exportedEvent is the Life OS event an export copies, as the change feed
// last gave it, or as Calendar has it now; found is false when Calendar no
// longer has it.
func (s *Syncer) exportedEvent(ctx context.Context, export store.GoogleExport) (payload, bool, error) {
	event, known, err := decodePayload(export.Payload)
	if err != nil || known {
		return event, known, err
	}
	current, err := s.service.calendar.GetEvent(ctx, googleuuid.UUID(export.EventID.Bytes).String())
	if calendar.IsNotFound(err) {
		return payload{}, false, nil
	}
	if err != nil {
		return payload{}, false, err
	}
	return payload{Event: current}, true, nil
}

// followCopy applies item, the copy's current state in Google, to the
// exported event. A Life OS change still waiting to be exported conflicts:
// Google's version is kept and the conflict recorded.
func (s *Syncer) followCopy(ctx context.Context, q *store.Queries, export store.GoogleExport, master Event, item sources.Item, now time.Time) error {
	event, found, err := s.exportedEvent(ctx, export)
	if err != nil {
		return err
	}
	if !found || event.Event.Source.Kind != "manual" {
		return nil
	}
	conflict := export.State != "synced"
	after, err := s.applyCopy(ctx, event, item)
	if err != nil {
		return err
	}
	encoded, err := encodePayload(after.Event, after.Exceptions)
	if err != nil {
		return err
	}
	if err := q.LinkGoogleExport(ctx, store.LinkGoogleExportParams{
		EventID: export.EventID, Etag: optional(master.ETag), GoogleUpdated: googleTime(master.Updated), Payload: encoded, Settle: conflict,
	}); err != nil {
		return err
	}
	if conflict {
		s.service.recordConflict(ctx, after.Event, "Life OS calendar", conflictExport, now)
	}
	return nil
}

// copyDeleted cancels an exported event whose copy was deleted in Google
// (kept as history) and unlinks it; restoring it in Life OS exports it
// again.
func (s *Syncer) copyDeleted(ctx context.Context, q *store.Queries, export store.GoogleExport, now time.Time) error {
	if err := q.UnlinkGoogleExport(ctx, export.EventID); err != nil {
		return err
	}
	event, found, err := s.exportedEvent(ctx, export)
	if err != nil || !found || event.Event.Source.Kind != "manual" || event.Event.Status != "active" {
		return err
	}
	updated, err := s.service.calendar.UpdateEvent(ctx, event.Event.ID, map[string]any{"status": "cancelled"})
	if calendar.IsNotFound(err) {
		return nil // deleted in Life OS meanwhile
	}
	if err != nil {
		return err
	}
	encoded, err := encodePayload(updated, event.Exceptions)
	if err != nil {
		return err
	}
	_, err = q.SetGoogleExportPayload(ctx, store.SetGoogleExportPayloadParams{Payload: encoded, Now: timestamptz(now), EventID: export.EventID})
	return err
}

// applyCopy writes item (an exported event's copy as Google has it) over
// the Life OS event through Calendar's events API: title, notes, place,
// times, time zone, rule, and priority, then the occurrences' changes and
// skips. Reminders stay Life OS's (Google holds them only as minutes, so
// the copy carries a simplified form of them). It returns the event as
// Calendar then has it (its exceptions as far as known).
func (s *Syncer) applyCopy(ctx context.Context, event payload, item sources.Item) (payload, error) {
	current := event.Event
	body := copyPatch(current, item, s.owner)
	if len(body) > 0 {
		updated, err := s.service.calendar.UpdateEvent(ctx, current.ID, body)
		if calendar.IsNotFound(err) {
			return event, nil // deleted in Life OS meanwhile: its tombstone deletes the copy
		}
		if err != nil {
			return event, fmt.Errorf("apply Google's version of “%s”: %w", current.Title, err)
		}
		event.Event = updated
	}
	if item.Recurrence == nil {
		return event, nil
	}
	stored, err := exceptionsByKey(event.Exceptions)
	if err != nil {
		return event, err
	}
	for _, write := range copyOccurrences(stored, item) {
		err := s.service.calendar.UpdateOccurrence(ctx, current.ID, write.key, write.body)
		if calendar.IsNotFound(err) || calendar.HasCode(err, 400, "") {
			s.logger.Warn("occurrence changed in Google not applied in Life OS", "event", current.ID, "occurrence", write.key, "error", logCode(err))
			continue
		}
		if err != nil {
			return event, err
		}
	}
	return event, nil
}

// copyPatch is the change that makes the Life OS event current say what
// Google's item says; empty when Google's version says nothing new.
func copyPatch(current calendar.Event, item sources.Item, owner *time.Location) map[string]any {
	body := map[string]any{}
	if item.Title != current.Title {
		body["title"] = item.Title
	}
	if textValue(item.Description) != textValue(current.Description) {
		body["description"] = item.Description
	}
	if textValue(item.Location) != textValue(current.Location) {
		body["location"] = item.Location
	}
	zone := item.TimeZone
	if current.TimeZone == nil && zone != nil && owner != nil && *zone == owner.String() {
		zone = nil // the owner's zone, as Life OS had it
	}
	if item.AllDay {
		first, last := item.StartDate.Format(time.DateOnly), item.EndDate.Format(time.DateOnly)
		if !current.AllDay || deref(current.StartDate) != first || deref(current.EndDate) != last {
			body["allDay"], body["startDate"], body["endDate"] = true, first, last
		}
	} else {
		start, end := item.StartAt.UTC().Truncate(time.Second), item.EndAt.UTC().Truncate(time.Second)
		if current.AllDay || current.StartAt == nil || current.EndAt == nil || !current.StartAt.Truncate(time.Second).Equal(start) ||
			!current.EndAt.Truncate(time.Second).Equal(end) || deref(current.TimeZone) != deref(zone) {
			body["allDay"], body["startAt"], body["endAt"] = false, start.Format(time.RFC3339), end.Format(time.RFC3339)
			body["timeZone"] = zone
		}
	}
	if !sameRule(current.Recurrence, item.Recurrence, item.AllDay, owner) {
		if item.Recurrence == nil {
			body["recurrence"] = nil
		} else {
			rule := map[string]any{"frequency": item.Recurrence.Frequency, "interval": max(item.Recurrence.Interval, 1)}
			if len(item.Recurrence.ByWeekday) > 0 {
				rule["byWeekday"] = item.Recurrence.ByWeekday
			}
			if item.Recurrence.Count > 0 {
				rule["count"] = item.Recurrence.Count
			}
			if until := item.Recurrence.Until; until != nil {
				value := until.UTC()
				if item.AllDay && current.Recurrence != nil && current.Recurrence.Until != nil && sameOwnerDate(*current.Recurrence.Until, value, owner) {
					value = current.Recurrence.Until.UTC() // the same last day
				}
				rule["until"] = value.Format(time.RFC3339)
			}
			body["recurrence"] = rule
		}
	}
	if item.Priority != nil {
		// A Life OS event without a priority of its own is medium (its
		// default).
		wanted := current.DefaultPriority
		if value := *item.Priority; value == "low" || value == "high" {
			wanted = value
		}
		if wanted != "" && wanted != current.Priority {
			body["priority"] = wanted
		}
	}
	return body
}

// sameRule reports whether Calendar's rule is the item's (an all-day
// series' until compared as the owner's date).
func sameRule(current *calendar.Recurrence, rule *sources.Recurrence, allDay bool, owner *time.Location) bool {
	if current == nil || rule == nil {
		return current == nil && rule == nil
	}
	count := 0
	if current.Count != nil {
		count = *current.Count
	}
	if current.Frequency != rule.Frequency || max(current.Interval, 1) != max(rule.Interval, 1) || count != rule.Count ||
		!slices.Equal(current.ByWeekday, rule.ByWeekday) && !(len(current.ByWeekday) <= 1 && len(rule.ByWeekday) <= 1) {
		return false
	}
	if (current.Until == nil) != (rule.Until == nil) {
		return false
	}
	if current.Until == nil {
		return true
	}
	if allDay {
		return sameOwnerDate(*current.Until, *rule.Until, owner)
	}
	return current.Until.Truncate(time.Second).Equal(rule.Until.Truncate(time.Second))
}

func sameOwnerDate(a, b time.Time, owner *time.Location) bool {
	if owner == nil {
		owner = time.UTC
	}
	return a.In(owner).Format(time.DateOnly) == b.In(owner).Format(time.DateOnly)
}

// occurrenceWrite is one occurrence change to make in Calendar.
type occurrenceWrite struct {
	key  string
	body map[string]any
}

// copyOccurrences are the occurrence changes that make the stored
// exceptions say what Google's do: Google's changes and skips (a change
// Google no longer has goes back to the series' value), with the reminders
// of each occurrence kept as they are in Life OS.
func copyOccurrences(stored map[string]sources.Exception, item sources.Item) []occurrenceWrite {
	keys := map[string]bool{}
	for key := range stored {
		keys[key] = true
	}
	for key := range item.Exceptions {
		keys[key] = true
	}
	var writes []occurrenceWrite
	for _, key := range sortedKeys(keys) {
		have, want := stored[key], item.Exceptions[key]
		body := map[string]any{}
		switch {
		case want.Cancelled && !have.Cancelled:
			writes = append(writes, occurrenceWrite{key: key, body: map[string]any{"status": "cancelled"}})
			continue
		case want.Cancelled:
			continue
		case have.Cancelled:
			body["status"] = "active"
		}
		occurrenceFieldsPatch(body, key, have.Changes, want.Changes, item)
		if len(body) > 0 {
			writes = append(writes, occurrenceWrite{key: key, body: body})
		}
	}
	return writes
}

// occurrenceFieldsPatch adds to body what makes an occurrence's own changes
// (have) say Google's (want): a field Google changes takes Google's value,
// and one only Life OS changes goes back to the series' (item's) value.
func occurrenceFieldsPatch(body map[string]any, key string, have, want sources.Changes, item sources.Item) {
	if !sameText(have.Title, want.Title) {
		if want.Title != nil {
			body["title"] = *want.Title
		} else {
			body["title"] = item.Title
		}
	}
	if !sameChangeText(have.Description, want.Description) {
		if want.Description != nil {
			body["description"] = want.Description.Value
		} else {
			body["description"] = item.Description
		}
	}
	if !sameChangeText(have.Location, want.Location) {
		if want.Location != nil {
			body["location"] = want.Location.Value
		} else {
			body["location"] = item.Location
		}
	}
	if item.AllDay {
		if !sameDay(have.StartDate, want.StartDate) || !sameDay(have.EndDate, want.EndDate) {
			first, err := time.Parse(time.DateOnly, key)
			if err == nil {
				last := first.Add(item.EndDate.Sub(item.StartDate))
				if want.StartDate != nil && want.EndDate != nil {
					first, last = *want.StartDate, *want.EndDate
				}
				body["startDate"], body["endDate"] = first.Format(time.DateOnly), last.Format(time.DateOnly)
			}
		}
	} else if !sameInstant(have.StartAt, want.StartAt) || !sameInstant(have.EndAt, want.EndAt) {
		start, err := time.Parse(time.RFC3339, key)
		if err == nil {
			end := start.Add(item.EndAt.Sub(item.StartAt))
			if want.StartAt != nil && want.EndAt != nil {
				start, end = *want.StartAt, *want.EndAt
			}
			body["startAt"], body["endAt"] = start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339)
		}
	}
	if !sameText(have.Priority, want.Priority) && (want.Priority != nil || have.Priority != nil) {
		if want.Priority != nil {
			body["priority"] = *want.Priority
		} else if item.Priority != nil && (*item.Priority == "low" || *item.Priority == "high") {
			body["priority"] = *item.Priority
		} else {
			body["priority"] = "medium"
		}
	}
}

func sameChangeText(a, b *sources.Text) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return textValue(a.Value) == textValue(b.Value)
}

func sameDay(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Format(time.DateOnly) == b.Format(time.DateOnly)
}

func sameInstant(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Truncate(time.Second).Equal(b.Truncate(time.Second))
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// googleTime reads Google's updated time.
func googleTime(value string) pgtype.Timestamptz {
	instant, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return pgtype.Timestamptz{}
	}
	return timestamptz(instant)
}

// Activity of conflicts, attributed to SyncActor (an export) or the
// requester.

// EventConflict is a change Life OS could not write because the event had
// changed in Google meanwhile: Google's version was kept.
const EventConflict = "calendar.google.conflict"

// Why a conflict happened.
const (
	conflictEdit   = "edit"
	conflictExport = "export"
)

func eventRef(event calendar.Event) (activity.Subject, map[string]any) {
	data := map[string]any{"title": activity.Preview(event.Title, 200), "allDay": event.AllDay, "status": event.Status, "sourceKind": event.Source.Kind}
	if event.AllDay && event.StartDate != nil {
		data["startDate"] = *event.StartDate
	} else if event.StartAt != nil {
		data["startAt"] = event.StartAt.UTC().Format(time.RFC3339)
	}
	if event.Source.SourceID != nil {
		data["sourceId"] = *event.Source.SourceID
	}
	return activity.Subject{Type: "calendar_event", ID: event.ID}, data
}

// recordConflict records that a change to event could not be written to
// calendar (by name) because it changed there meanwhile; Google's version
// was kept.
func (s *Service) recordConflict(ctx context.Context, event calendar.Event, calendarName, kind string, now time.Time) {
	subject, data := eventRef(event)
	data["calendar"], data["kind"] = calendarName, kind
	trace := activity.TraceFrom(ctx)
	actor := trace.Actor
	if kind == conflictExport || actor.IsZero() {
		actor = SyncActor
	}
	summary := fmt.Sprintf("“%s” changed in Google (%s) meanwhile: Life OS kept Google's version", activity.Preview(event.Title, 120), calendarName)
	if kind == conflictExport {
		summary = fmt.Sprintf("“%s” changed in Google (%s) before Life OS's change reached it: Google's version was kept", activity.Preview(event.Title, 120), calendarName)
	}
	s.activity.Emit(ctx, activity.Event{
		ID:            activity.EventID(EventConflict, subject.ID, now.UTC().Format(time.RFC3339Nano)),
		Type:          EventConflict,
		Subject:       subject,
		CorrelationID: trace.CorrelationID,
		CausationID:   trace.CausationID,
		Actor:         actor,
		Outcome:       activity.Failed,
		Summary:       activity.Preview(summary, 500),
		Data:          data,
		OccurredAt:    now,
	})
}

func pgID(id googleuuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }
