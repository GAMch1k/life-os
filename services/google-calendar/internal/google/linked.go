package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	store "github.com/GAMch1k/life-os/services/google-calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/sources"
)

// The Life OS calendar holds the copies of exported Life OS events. They
// are not imported as Google events: the Life OS event is the owner's, and
// a change made to its copy in Google (moved on the phone, renamed,
// deleted) is applied to it, so Google holds the latest version of every
// event in a Google calendar. Events created in Google in the Life OS
// calendar are imported like any synced calendar's.

// isLifeOSCalendar reports whether calendar is the connection's Life OS
// calendar.
func (s *Syncer) isLifeOSCalendar(ctx context.Context, q *store.Queries, calendar store.GoogleCalendar) (bool, error) {
	connection, err := q.GetGoogleConnection(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return connection.ID == calendar.ConnectionID && connection.LifeosCalendarID != nil && *connection.LifeosCalendarID == calendar.GoogleCalendarID, nil
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
// copy that only carries Calendar's own last write changes nothing, so a
// Life OS change still waiting to be exported is not undone.
func (s *Syncer) followCopies(ctx context.Context, q *store.Queries, calendar store.GoogleCalendar, m mapper, copies []Event, listed *listingChanges, now time.Time) error {
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
	linked, err := q.ListLinkedGoogleExports(ctx, &calendar.GoogleCalendarID)
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

// followCopy applies item, the copy's current state in Google, to the
// exported event. A Life OS change still waiting to be exported conflicts:
// Google's version is kept and the conflict recorded.
func (s *Syncer) followCopy(ctx context.Context, q *store.Queries, export store.GoogleExport, master Event, item sources.Item, now time.Time) error {
	event, err := q.GetEvent(ctx, export.EventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if event.SourceKind != "manual" {
		return nil
	}
	conflict := export.State != "synced"
	after, changed, err := applyCopy(ctx, q, event, item, s.owner)
	if err != nil {
		return err
	}
	if err := q.LinkGoogleExport(ctx, store.LinkGoogleExportParams{
		EventID: export.EventID, Etag: optional(master.ETag), GoogleUpdated: googleTime(master.Updated), Settle: conflict,
	}); err != nil {
		return err
	}
	if changed {
		s.service.recordCopyChange(ctx, event, after, now)
	}
	if conflict {
		s.service.recordConflict(ctx, after, "Life OS calendar", conflictExport, now)
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
	event, err := q.GetEvent(ctx, export.EventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || event.SourceKind != "manual" || event.Status != "active" {
		return err
	}
	params := updateParams(event)
	params.Status, params.CancelledAt = "cancelled", timestamptz(now)
	after, err := q.UpdateEvent(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // changed meanwhile: the change is exported and recreates the copy
	}
	if err != nil {
		return err
	}
	s.service.recordCopyDeleted(ctx, after, now)
	return nil
}

// updateParams rewrites event as it is (the caller changes fields).
func updateParams(event store.Event) store.UpdateEventParams {
	return store.UpdateEventParams{
		Title: event.Title, Description: event.Description, Location: event.Location, AllDay: event.AllDay,
		StartAt: event.StartAt, EndAt: event.EndAt, StartDate: event.StartDate, EndDate: event.EndDate, TimeZone: event.TimeZone,
		RecurrenceFrequency: event.RecurrenceFrequency, RecurrenceInterval: event.RecurrenceInterval, RecurrenceUntil: event.RecurrenceUntil,
		RecurrenceByWeekday: event.RecurrenceByWeekday, RecurrenceCount: event.RecurrenceCount,
		Reminders: event.Reminders, UseDefaultReminders: event.UseDefaultReminders, Priority: event.Priority,
		Status: event.Status, CancelledAt: event.CancelledAt, RemovedFromSourceAt: event.RemovedFromSourceAt,
		ID: event.ID, ExpectedUpdatedAt: event.UpdatedAt,
	}
}

// applyCopy writes item (an exported event's copy as Google has it) over
// event: title, notes, place, times, time zone, and rule, and the
// occurrence changes and skips. Reminders stay Life OS's (Google holds them
// only as minutes, so the copy carries a simplified form of them). changed
// is false when Google's version says nothing new.
func applyCopy(ctx context.Context, q *store.Queries, event store.Event, item sources.Item, owner *time.Location) (store.Event, bool, error) {
	params := updateParams(event)
	params.Title, params.Description, params.Location, params.AllDay = item.Title, item.Description, item.Location, item.AllDay
	if item.Priority != nil {
		// A Life OS event without a priority of its own is medium.
		params.Priority = nil
		if value := *item.Priority; value == "low" || value == "high" {
			params.Priority = &value
		}
	}
	if item.AllDay {
		params.StartAt, params.EndAt, params.TimeZone = pgtype.Timestamptz{}, pgtype.Timestamptz{}, nil
		params.StartDate, params.EndDate = pgtype.Date{Time: item.StartDate, Valid: true}, pgtype.Date{Time: item.EndDate, Valid: true}
	} else {
		params.StartDate, params.EndDate = pgtype.Date{}, pgtype.Date{}
		params.StartAt, params.EndAt = timestamptz(item.StartAt), timestamptz(item.EndAt)
		params.TimeZone = item.TimeZone
		if event.TimeZone == nil && item.TimeZone != nil && owner != nil && *item.TimeZone == owner.String() {
			params.TimeZone = nil // the owner's zone, as Life OS had it
		}
	}
	params.RecurrenceFrequency, params.RecurrenceInterval, params.RecurrenceUntil, params.RecurrenceByWeekday, params.RecurrenceCount = nil, nil, pgtype.Timestamptz{}, nil, nil
	if rule := item.Recurrence; rule != nil {
		frequency, interval := rule.Frequency, int32(rule.Interval)
		params.RecurrenceFrequency, params.RecurrenceInterval = &frequency, &interval
		params.RecurrenceByWeekday = rule.ByWeekday
		if rule.Count > 0 {
			count := int32(rule.Count)
			params.RecurrenceCount = &count
		}
		if rule.Until != nil {
			until := *rule.Until
			if item.AllDay && event.RecurrenceUntil.Valid && sameOwnerDate(event.RecurrenceUntil.Time, until, owner) {
				until = event.RecurrenceUntil.Time // the same last day
			}
			params.RecurrenceUntil = timestamptz(until)
		}
	}
	stored, err := q.ListEventExceptions(ctx, []pgtype.UUID{event.ID})
	if err != nil {
		return store.Event{}, false, err
	}
	writes, drops, err := copyExceptions(stored, item.Exceptions)
	if err != nil {
		return store.Event{}, false, err
	}
	if sameEventRow(event, params) && len(writes) == 0 && len(drops) == 0 {
		return event, false, nil
	}
	after, err := q.UpdateEvent(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return event, false, nil // changed in Life OS meanwhile: that change is exported next
	}
	if err != nil {
		return store.Event{}, false, err
	}
	if len(drops) > 0 {
		if _, err := q.DeleteEventExceptions(ctx, store.DeleteEventExceptionsParams{EventID: event.ID, RecurrenceIds: drops}); err != nil {
			return store.Event{}, false, err
		}
	}
	for _, key := range sortedKeys(writes) {
		write := writes[key]
		if _, err := q.UpsertEventException(ctx, store.UpsertEventExceptionParams{EventID: event.ID, RecurrenceID: key, Changes: write.changes, CancelledAt: write.cancelledAt}); err != nil {
			return store.Event{}, false, err
		}
	}
	return after, true, nil
}

func sameOwnerDate(a, b time.Time, owner *time.Location) bool {
	if owner == nil {
		owner = time.UTC
	}
	return a.In(owner).Format(time.DateOnly) == b.In(owner).Format(time.DateOnly)
}

// sameEventRow reports whether params would leave event as it is.
func sameEventRow(event store.Event, params store.UpdateEventParams) bool {
	sameText := func(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	sameCount := func(a, b *int32) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	sameInstant := func(a, b pgtype.Timestamptz) bool {
		return a.Valid == b.Valid && (!a.Valid || a.Time.Truncate(time.Second).Equal(b.Time.Truncate(time.Second)))
	}
	sameDate := func(a, b pgtype.Date) bool {
		return a.Valid == b.Valid && (!a.Valid || a.Time.Format(time.DateOnly) == b.Time.Format(time.DateOnly))
	}
	return event.Title == params.Title && sameText(event.Description, params.Description) && sameText(event.Location, params.Location) &&
		event.AllDay == params.AllDay && sameInstant(event.StartAt, params.StartAt) && sameInstant(event.EndAt, params.EndAt) &&
		sameDate(event.StartDate, params.StartDate) && sameDate(event.EndDate, params.EndDate) && sameText(event.TimeZone, params.TimeZone) &&
		sameText(event.RecurrenceFrequency, params.RecurrenceFrequency) && sameCount(event.RecurrenceInterval, params.RecurrenceInterval) &&
		sameInstant(event.RecurrenceUntil, params.RecurrenceUntil) && slices.Equal(event.RecurrenceByWeekday, params.RecurrenceByWeekday) &&
		sameCount(event.RecurrenceCount, params.RecurrenceCount) && sameText(event.Priority, params.Priority)
}

// exceptionWrite is one occurrence exception to store.
type exceptionWrite struct {
	changes     []byte
	cancelledAt pgtype.Timestamptz
}

// copyExceptions are the occurrence exceptions that make the stored ones
// say what Google's do: Google's changes and skips, with the reminders of
// each occurrence kept as they are in Life OS.
func copyExceptions(stored []store.EventException, wanted map[string]sources.Exception) (map[string]exceptionWrite, []string, error) {
	have := map[string]map[string]json.RawMessage{}
	rows := map[string]store.EventException{}
	for _, row := range stored {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(row.Changes, &fields); err != nil {
			return nil, nil, fmt.Errorf("decode occurrence changes: %w", err)
		}
		have[row.RecurrenceID], rows[row.RecurrenceID] = fields, row
	}
	writes := map[string]exceptionWrite{}
	var drops []string
	keys := map[string]bool{}
	for key := range wanted {
		keys[key] = true
	}
	for key := range have {
		keys[key] = true
	}
	for key := range keys {
		next := map[string]json.RawMessage{}
		if exception, ok := wanted[key]; ok {
			encoded, err := exception.Changes.JSON()
			if err != nil {
				return nil, nil, err
			}
			if err := json.Unmarshal(encoded, &next); err != nil {
				return nil, nil, err
			}
		}
		delete(next, "reminders")
		delete(next, "useDefaultReminders")
		for _, name := range []string{"reminders", "useDefaultReminders"} {
			if value, ok := have[key][name]; ok {
				next[name] = value
			}
		}
		cancelled := wanted[key].Cancelled
		row, found := rows[key]
		if len(next) == 0 && !cancelled {
			if found {
				drops = append(drops, key)
			}
			continue
		}
		if found && row.CancelledAt.Valid == cancelled && reflect.DeepEqual(normalize(have[key]), normalize(next)) {
			continue
		}
		encoded, err := json.Marshal(next)
		if err != nil {
			return nil, nil, err
		}
		write := exceptionWrite{changes: encoded}
		if cancelled {
			write.cancelledAt = row.CancelledAt
			if !write.cancelledAt.Valid {
				write.cancelledAt = timestamptz(time.Now())
			}
		}
		writes[key] = write
	}
	slices.Sort(drops)
	return writes, drops, nil
}

// normalize decodes raw JSON values so equal values compare equal however
// they were written (instants in any offset are compared as instants).
func normalize(fields map[string]json.RawMessage) map[string]any {
	out := map[string]any{}
	for name, raw := range fields {
		var value any
		_ = json.Unmarshal(raw, &value)
		if text, ok := value.(string); ok && (name == "startAt" || name == "endAt") {
			if instant, err := time.Parse(time.RFC3339Nano, text); err == nil {
				value = instant.UTC().Format(time.RFC3339)
			}
		}
		out[name] = value
	}
	return out
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func pgID(id googleuuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }

// googleTime reads Google's updated time.
func googleTime(value string) pgtype.Timestamptz {
	instant, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return pgtype.Timestamptz{}
	}
	return timestamptz(instant)
}

// Activity of exported events changed or deleted in Google, attributed to
// SyncActor, and of conflicts.

// EventConflict is a change Life OS could not write because the event had
// changed in Google meanwhile: Google's version was kept.
const EventConflict = "calendar.google.conflict"

// Why a conflict happened.
const (
	conflictEdit   = "edit"
	conflictExport = "export"
)

func eventRef(event store.Event) (activity.Subject, map[string]any) {
	id := googleuuid.UUID(event.ID.Bytes).String()
	data := map[string]any{"title": activity.Preview(event.Title, 200), "allDay": event.AllDay, "status": event.Status, "sourceKind": event.SourceKind}
	if event.AllDay && event.StartDate.Valid {
		data["startDate"] = event.StartDate.Time.Format(time.DateOnly)
	} else if event.StartAt.Valid {
		data["startAt"] = event.StartAt.Time.UTC().Format(time.RFC3339)
	}
	if event.SourceID.Valid {
		data["sourceId"] = googleuuid.UUID(event.SourceID.Bytes).String()
	}
	return activity.Subject{Type: "calendar_event", ID: id}, data
}

// recordConflict records that a change to event could not be written to
// calendar (by name) because it changed there meanwhile; Google's version
// was kept.
func (s *Service) recordConflict(ctx context.Context, event store.Event, calendar, kind string, now time.Time) {
	subject, data := eventRef(event)
	data["calendar"], data["kind"] = calendar, kind
	trace := activity.TraceFrom(ctx)
	actor := trace.Actor
	if kind == conflictExport || actor.IsZero() {
		actor = SyncActor
	}
	summary := fmt.Sprintf("“%s” changed in Google (%s) meanwhile: Life OS kept Google's version", activity.Preview(event.Title, 120), calendar)
	if kind == conflictExport {
		summary = fmt.Sprintf("“%s” changed in Google (%s) before Life OS's change reached it: Google's version was kept", activity.Preview(event.Title, 120), calendar)
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

// recordCopyChange records an exported event changed in Google.
func (s *Service) recordCopyChange(ctx context.Context, before, after store.Event, now time.Time) {
	subject, data := eventRef(after)
	var changes []string
	moved := before.AllDay != after.AllDay || before.StartAt != after.StartAt || before.EndAt != after.EndAt ||
		before.StartDate != after.StartDate || before.EndDate != after.EndDate
	if before.Title != after.Title {
		changes = append(changes, "title")
		data["previousTitle"] = activity.Preview(before.Title, 200)
	}
	if !sameText(before.Description, after.Description) {
		changes = append(changes, "description")
	}
	if !sameText(before.Location, after.Location) {
		changes = append(changes, "location")
	}
	if moved {
		changes = append(changes, "time")
	}
	if !sameText(before.RecurrenceFrequency, after.RecurrenceFrequency) || before.RecurrenceUntil != after.RecurrenceUntil ||
		!slices.Equal(before.RecurrenceByWeekday, after.RecurrenceByWeekday) {
		changes = append(changes, "repetition")
	}
	eventType := "calendar.event.updated"
	if moved {
		eventType = "calendar.event.rescheduled"
	}
	data["changes"], data["changedIn"] = changes, "google"
	summary := fmt.Sprintf("Event “%s” changed in Google Calendar (Life OS calendar)", activity.Preview(after.Title, 120))
	if len(changes) > 0 {
		summary += ": " + strings.Join(changes, ", ")
	}
	s.emitSynced(ctx, eventType, subject, summary, data, after.UpdatedAt.Time, now)
}

// recordCopyDeleted records an exported event cancelled because its copy
// was deleted in Google.
func (s *Service) recordCopyDeleted(ctx context.Context, event store.Event, now time.Time) {
	subject, data := eventRef(event)
	data["changedIn"] = "google"
	summary := fmt.Sprintf("Event “%s” was deleted in Google Calendar (Life OS calendar): cancelled in Life OS", activity.Preview(event.Title, 120))
	s.emitSynced(ctx, "calendar.event.cancelled", subject, summary, data, event.UpdatedAt.Time, now)
}

func (s *Service) emitSynced(ctx context.Context, eventType string, subject activity.Subject, summary string, data map[string]any, version, now time.Time) {
	trace := activity.TraceFrom(ctx)
	s.activity.Emit(ctx, activity.Event{
		ID:            activity.EventID(eventType, subject.ID, version.UTC().Format(time.RFC3339Nano)),
		Type:          eventType,
		Subject:       subject,
		CorrelationID: trace.CorrelationID,
		CausationID:   trace.CausationID,
		Actor:         SyncActor,
		Outcome:       activity.Succeeded,
		Summary:       activity.Preview(summary, 500),
		Data:          data,
		OccurredAt:    now,
	})
}
