package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/sources"
)

// Exporting events created in Life OS (P11-03). Each change of a Life OS
// event is stored locally first and queued in google_exports; the export
// worker writes it to the Life OS calendar in Google, retrying with backoff
// while Google is unreachable, and records Google's etag so the inbound
// sync of that calendar recognizes its own writes. ICS events are never
// exported, and Google events live in their own calendars.

// LifeOSCalendarName is the name of the calendar Life OS creates.
const LifeOSCalendarName = "Life OS"

// Export timing.
const (
	// exportLease bounds one export; a crashed one is retried after it.
	exportLease = 2 * time.Minute
	// maxExportsPerRun bounds the exports of one run of the worker.
	maxExportsPerRun = 200
	// unusableRetry is when an export waiting for an expired connection is
	// looked at again (reconnecting makes it due at once).
	unusableRetry = time.Hour
)

// Activity event types of the Life OS calendar and exports.
const (
	EventCalendarCreated = "calendar.google.calendar_created"
	EventExportFailed    = "calendar.google.export_failed"
)

// QueueExport queues the export of a Life OS event's latest state after it
// was created or changed in Life OS. Nothing is queued while Google is not
// connected or export is off. A failure is logged: the event is stored.
func (s *Service) QueueExport(ctx context.Context, eventID pgtype.UUID) {
	if !s.Configured() {
		return
	}
	connection, found, err := s.Connection(ctx)
	if err != nil || !found || !connection.ExportEnabled {
		if err != nil {
			s.logger.Warn("export of a Life OS event not queued", "event", googleuuid.UUID(eventID.Bytes), "error", err)
		}
		return
	}
	if err := s.queries.EnqueueGoogleExport(ctx, store.EnqueueGoogleExportParams{EventID: eventID, ConnectionID: connection.ID, Now: timestamptz(s.now())}); err != nil {
		s.logger.Warn("export of a Life OS event not queued", "event", googleuuid.UUID(eventID.Bytes), "error", err)
		return
	}
	s.syncer.Wake()
}

// QueueDeletion queues deleting the copy of a Life OS event deleted in Life
// OS; an event never exported is only forgotten.
func (s *Service) QueueDeletion(ctx context.Context, eventID pgtype.UUID) {
	if !s.Configured() {
		return
	}
	forgotten, err := s.queries.DeleteUnexportedGoogleExport(ctx, eventID)
	if err == nil && forgotten == 0 {
		err = s.queries.MarkGoogleExportDeleted(ctx, store.MarkGoogleExportDeletedParams{EventID: eventID, Now: timestamptz(s.now())})
	}
	if err != nil {
		s.logger.Warn("deletion of an exported event not queued", "event", googleuuid.UUID(eventID.Bytes), "error", err)
		return
	}
	s.syncer.Wake()
}

// SetExportEnabled switches exporting Life OS events on or off. Switched
// on, every upcoming Life OS event is queued; switched off, nothing more is
// written to the Life OS calendar (what is there stays).
func (s *Service) SetExportEnabled(ctx context.Context, enabled bool) (store.GoogleConnection, error) {
	connection, found, err := s.Connection(ctx)
	if err != nil {
		return store.GoogleConnection{}, err
	}
	if !found {
		return store.GoogleConnection{}, ErrNotConnected
	}
	row, err := s.queries.SetGoogleExportEnabled(ctx, store.SetGoogleExportEnabledParams{ExportEnabled: enabled, ID: connection.ID})
	if err != nil {
		return store.GoogleConnection{}, err
	}
	if enabled && !connection.ExportEnabled {
		s.queueUpcoming(ctx, row)
	}
	return row, nil
}

// queueUpcoming queues every upcoming Life OS event and makes waiting
// exports due (after connecting, or switching export on).
func (s *Service) queueUpcoming(ctx context.Context, connection store.GoogleConnection) {
	if !connection.ExportEnabled {
		return
	}
	now := timestamptz(s.now())
	if _, err := s.queries.EnqueueUpcomingGoogleExports(ctx, store.EnqueueUpcomingGoogleExportsParams{ConnectionID: connection.ID, Now: now}); err != nil {
		s.logger.Warn("upcoming Life OS events not queued for export", "error", err)
	}
	if err := s.queries.MarkGoogleExportsDue(ctx, store.MarkGoogleExportsDueParams{ConnectionID: connection.ID, Now: now}); err != nil {
		s.logger.Warn("waiting exports not made due", "error", err)
	}
	s.syncer.Wake()
}

// ExportSummary is how exporting goes, for the connection's status.
type ExportSummary = store.GoogleExportSummaryRow

// ExportSummary counts the waiting and failing exports of the connection.
func (s *Service) ExportSummary(ctx context.Context, connection store.GoogleConnection) (ExportSummary, error) {
	return s.queries.GoogleExportSummary(ctx, connection.ID)
}

// LifeOSCalendar is the Life OS calendar's row in the calendar list, if it
// is listed.
func (s *Service) LifeOSCalendar(ctx context.Context, connection store.GoogleConnection) (store.GoogleCalendar, bool, error) {
	if connection.LifeosCalendarID == nil {
		return store.GoogleCalendar{}, false, nil
	}
	row, err := s.queries.GetGoogleCalendarByGoogleID(ctx, store.GetGoogleCalendarByGoogleIDParams{ConnectionID: connection.ID, GoogleCalendarID: *connection.LifeosCalendarID})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.GoogleCalendar{}, false, nil
	}
	return row, err == nil, err
}

// ExportDue makes sure the Life OS calendar exists and writes the due
// exports, one after another, while export is on and the connection is
// usable.
func (s *Syncer) ExportDue(ctx context.Context) {
	if !s.service.Configured() {
		return
	}
	connection, found, err := s.service.Connection(ctx)
	if err != nil || !found || connection.Status == StatusExpired || !connection.ExportEnabled {
		return
	}
	if _, err := s.ensureLifeOSCalendar(ctx, connection); err != nil {
		if ctx.Err() == nil && !errors.Is(err, ErrExpired) {
			s.logger.Warn("Life OS calendar not created in Google", "error", err)
		}
		return
	}
	for range maxExportsPerRun {
		if ctx.Err() != nil {
			return
		}
		now := s.now().UTC()
		claimed, err := s.service.queries.ClaimDueGoogleExport(ctx, store.ClaimDueGoogleExportParams{Now: timestamptz(now), LeaseUntil: timestamptz(now.Add(exportLease))})
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("due exports not read", "error", err)
			}
			return
		}
		if err := s.export(ctx, claimed); err != nil {
			s.failExport(ctx, claimed, err)
		}
	}
}

// ensureLifeOSCalendar returns the Life OS calendar's Google id, adopting
// an owned calendar named "Life OS" or creating one (with the owner's
// timezone) the first time. It joins the calendar list with sync on, so
// events created in it in Google are imported and changes to the copies
// come back.
func (s *Syncer) ensureLifeOSCalendar(ctx context.Context, connection store.GoogleConnection) (string, error) {
	if connection.LifeosCalendarID != nil {
		return *connection.LifeosCalendarID, nil
	}
	now := s.now()
	adopted, err := s.service.queries.FindLifeOSCalendarCandidate(ctx, store.FindLifeOSCalendarCandidateParams{ConnectionID: connection.ID, Summary: LifeOSCalendarName})
	if err == nil {
		if _, err := s.service.queries.SetLifeOSCalendar(ctx, store.SetLifeOSCalendarParams{LifeosCalendarID: &adopted.GoogleCalendarID, CreatedAt: timestamptz(now), ID: connection.ID}); err != nil {
			return "", err
		}
		if !adopted.SyncEnabled {
			if _, err := s.service.SetCalendarSync(ctx, googleuuid.UUID(adopted.ID.Bytes), true); err != nil {
				return "", err
			}
		}
		return adopted.GoogleCalendarID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	entry, err := s.createLifeOSCalendar(ctx)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			// Such as a missing permission: shown with the connection.
			note := "Life OS could not create its calendar in Google: " + err.Error()
			if noteErr := s.service.queries.SetLifeOSCalendarNote(ctx, store.SetLifeOSCalendarNoteParams{Note: &note, ID: connection.ID}); noteErr != nil {
				s.logger.Warn("Life OS calendar failure not recorded", "error", noteErr)
			}
		}
		return "", err
	}
	err = s.service.inTx(ctx, func(q *store.Queries) error {
		if _, err := q.SetLifeOSCalendar(ctx, store.SetLifeOSCalendarParams{LifeosCalendarID: &entry.ID, CreatedAt: timestamptz(now), ID: connection.ID}); err != nil {
			return err
		}
		return addLifeOSCalendar(ctx, q, connection, entry, now)
	})
	if err != nil {
		return "", err
	}
	s.service.recordCalendarCreated(ctx, connection, entry, "", now)
	s.Wake()
	return entry.ID, nil
}

// addLifeOSCalendar lists a newly created Life OS calendar with sync on,
// due for its first sync now.
func addLifeOSCalendar(ctx context.Context, q *store.Queries, connection store.GoogleConnection, entry CalendarEntry, now time.Time) error {
	if err := q.UpsertGoogleCalendar(ctx, store.UpsertGoogleCalendarParams{
		ConnectionID: connection.ID, GoogleCalendarID: entry.ID, Summary: entry.Summary, AccessRole: "owner", InitialSyncEnabled: true,
	}); err != nil {
		return err
	}
	row, err := q.GetGoogleCalendarByGoogleID(ctx, store.GetGoogleCalendarByGoogleIDParams{ConnectionID: connection.ID, GoogleCalendarID: entry.ID})
	if err != nil {
		return err
	}
	_, err = q.ResyncGoogleCalendar(ctx, store.ResyncGoogleCalendarParams{ID: row.ID, Now: timestamptz(now)})
	return err
}

func (s *Syncer) createLifeOSCalendar(ctx context.Context) (CalendarEntry, error) {
	var entry CalendarEntry
	err := s.service.withToken(ctx, func(token string) (err error) {
		entry, err = s.service.api.InsertCalendar(ctx, token, LifeOSCalendarName, s.owner.String())
		return err
	})
	return entry, err
}

// recreateLifeOSCalendar replaces a Life OS calendar the owner deleted in
// Google (gone) with a new one and queues every exported event again; the
// connection's note says so.
func (s *Syncer) recreateLifeOSCalendar(ctx context.Context, gone string) error {
	connection, found, err := s.service.Connection(ctx)
	if err != nil || !found {
		return err
	}
	if connection.LifeosCalendarID == nil || *connection.LifeosCalendarID != gone {
		return nil // replaced meanwhile
	}
	entry, err := s.createLifeOSCalendar(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	note := fmt.Sprintf("The Life OS calendar was deleted in Google; Life OS created it again on %s and exports your events to it again.", now.In(s.owner).Format("2 Jan 2006, 15:04 MST"))
	var removed []removedSource
	err = s.service.inTx(ctx, func(q *store.Queries) error {
		if _, err := q.SetLifeOSCalendar(ctx, store.SetLifeOSCalendarParams{LifeosCalendarID: &entry.ID, CreatedAt: timestamptz(now), Note: &note, ID: connection.ID}); err != nil {
			return err
		}
		if old, err := q.GetGoogleCalendarByGoogleID(ctx, store.GetGoogleCalendarByGoogleIDParams{ConnectionID: connection.ID, GoogleCalendarID: gone}); err == nil {
			if removed, err = linkedSources(ctx, q, connection.ID, []pgtype.UUID{old.ID}); err != nil {
				return err
			}
			if err := q.DeleteGoogleCalendar(ctx, old.ID); err != nil {
				return err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := addLifeOSCalendar(ctx, q, connection, entry, now); err != nil {
			return err
		}
		return q.ResetGoogleExports(ctx, store.ResetGoogleExportsParams{ConnectionID: connection.ID, Now: timestamptz(now)})
	})
	if err != nil {
		return err
	}
	s.service.recordSourcesRemoved(ctx, removed, removedUnlisted)
	s.service.recordCalendarCreated(ctx, connection, entry, note, now)
	s.Wake()
	return nil
}

// errCalendarGone: the Life OS calendar no longer exists in Google; it was
// created again and the export is retried.
var errCalendarGone = errors.New("the Life OS calendar was deleted in Google; it was created again")

// export writes one claimed export: the event's copy in the Life OS
// calendar created, changed, or deleted, then its changed occurrences.
func (s *Syncer) export(ctx context.Context, claimed store.GoogleExport) error {
	connection, found, err := s.service.Connection(ctx)
	if err != nil {
		return err
	}
	if !found || connection.ID != claimed.ConnectionID {
		return s.service.queries.DeleteGoogleExport(ctx, claimed.EventID)
	}
	if !connection.ExportEnabled || connection.Status == StatusExpired {
		return s.park(ctx, claimed, connection.ExportEnabled)
	}
	calendarID, err := s.ensureLifeOSCalendar(ctx, connection)
	if err != nil {
		return err
	}
	event, err := s.service.queries.GetEvent(ctx, claimed.EventID)
	missing := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !missing {
		return err
	}
	if missing || claimed.Deleted || event.SourceKind != "manual" || event.Status != "active" {
		return s.removeCopy(ctx, claimed, missing || claimed.Deleted || event.SourceKind != "manual")
	}
	exported := claimed.GoogleEventID != nil && claimed.GoogleCalendarID != nil && *claimed.GoogleCalendarID == calendarID
	fields, err := s.service.lifeOSFields(ctx, event)
	if err != nil {
		return err
	}
	body := ExportBody(googleuuid.UUID(event.ID.Bytes), fields.Fields, fields.effective, s.owner, fields.exdates)
	copyID := ExportID(googleuuid.UUID(event.ID.Bytes))
	var written Event
	if exported {
		written, err = s.service.patchEvent(ctx, calendarID, *claimed.GoogleEventID, deref(claimed.Etag), body)
		switch {
		case IsConflict(err):
			return s.exportConflict(ctx, claimed, event, calendarID, *claimed.GoogleEventID)
		case IsGone(err):
			exported = false // deleted in Google, or the whole calendar: written anew
		case err != nil:
			return err
		}
	}
	if !exported {
		if written, err = s.insertCopy(ctx, calendarID, copyID, body); err != nil {
			return err
		}
	}
	instances, keys, err := s.exportOccurrences(ctx, calendarID, written.ID, event, fields, claimed.ExportedExceptions)
	if err != nil {
		return err
	}
	if err := s.rememberCopies(ctx, connection, calendarID, append([]Event{written}, instances...)); err != nil {
		return err
	}
	_, err = s.service.queries.FinishGoogleExport(ctx, store.FinishGoogleExportParams{
		EventID: claimed.EventID, GoogleCalendarID: &calendarID, GoogleEventID: &written.ID, Etag: optional(written.ETag),
		GoogleUpdated: googleTime(written.Updated), ExportedExceptions: keys, Now: timestamptz(s.now()), ClaimedRequestedAt: claimed.RequestedAt,
	})
	return err
}

// insertCopy creates the copy with its fixed id. A copy that already exists
// (an earlier attempt that was not recorded, or one deleted in Google) is
// overwritten and restored. A calendar that no longer exists is created
// again (errCalendarGone: retried).
func (s *Syncer) insertCopy(ctx context.Context, calendarID, copyID string, body map[string]any) (Event, error) {
	insert := make(map[string]any, len(body)+1)
	for key, value := range body {
		insert[key] = value
	}
	insert["id"] = copyID
	written, err := s.service.insertEvent(ctx, calendarID, insert)
	switch {
	case err == nil:
		return written, nil
	case IsDuplicate(err):
		current, err := s.service.getEvent(ctx, calendarID, copyID)
		if err != nil {
			return Event{}, err
		}
		return s.service.patchEvent(ctx, calendarID, copyID, current.ETag, body)
	case IsGone(err):
		if err := s.recreateLifeOSCalendar(ctx, calendarID); err != nil {
			return Event{}, err
		}
		return Event{}, errCalendarGone
	}
	return Event{}, err
}

// removeCopy deletes the copy of an event deleted (forget: the export goes
// too), cancelled, or no longer a Life OS event.
func (s *Syncer) removeCopy(ctx context.Context, claimed store.GoogleExport, forget bool) error {
	if claimed.GoogleEventID != nil && claimed.GoogleCalendarID != nil {
		err := s.service.deleteEvent(ctx, *claimed.GoogleCalendarID, *claimed.GoogleEventID, "")
		if err != nil && !IsGone(err) {
			return err
		}
		connection, found, err := s.service.Connection(ctx)
		if err == nil && found {
			if row, err := s.service.queries.GetGoogleCalendarByGoogleID(ctx, store.GetGoogleCalendarByGoogleIDParams{ConnectionID: connection.ID, GoogleCalendarID: *claimed.GoogleCalendarID}); err == nil {
				if err := s.service.queries.DeleteGoogleEventsByID(ctx, store.DeleteGoogleEventsByIDParams{CalendarID: row.ID, EventIds: []string{*claimed.GoogleEventID}}); err != nil {
					return err
				}
			}
		}
	}
	if forget {
		return s.service.queries.DeleteGoogleExport(ctx, claimed.EventID)
	}
	_, err := s.service.queries.FinishGoogleExport(ctx, store.FinishGoogleExportParams{
		EventID: claimed.EventID, ExportedExceptions: []string{}, Now: timestamptz(s.now()), ClaimedRequestedAt: claimed.RequestedAt,
	})
	return err
}

// park gives a claimed export back without trying: export is off (retried
// when it is switched on) or the connection expired (retried later, and at
// once after reconnecting).
func (s *Syncer) park(ctx context.Context, claimed store.GoogleExport, exportOn bool) error {
	params := store.ParkGoogleExportParams{EventID: claimed.EventID}
	if exportOn {
		params.NextAttemptAt = timestamptz(s.now().Add(unusableRetry))
	}
	return s.service.queries.ParkGoogleExport(ctx, params)
}

// failExport records a failed attempt: retried after the sync backoff
// (the interval, doubling up to an hour); Activity records the start of a
// failure streak and a changed error.
func (s *Syncer) failExport(ctx context.Context, claimed store.GoogleExport, cause error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	now := s.now()
	if errors.Is(cause, ErrExpired) {
		if err := s.park(ctx, claimed, true); err != nil {
			s.logger.Warn("export not parked", "event", googleuuid.UUID(claimed.EventID.Bytes), "error", err)
		}
		return
	}
	message := syncErrorMessage(cause)
	if errors.Is(cause, errCalendarGone) {
		message = cause.Error()
	}
	delay := SyncBackoff(s.interval, int(claimed.Attempts)+1)
	if errors.Is(cause, errCalendarGone) {
		delay = 0
	}
	updated, err := s.service.queries.FailGoogleExport(ctx, store.FailGoogleExportParams{EventID: claimed.EventID, LastError: &message, NextAttemptAt: timestamptz(now.Add(delay))})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.logger.Warn("failed export not recorded", "event", googleuuid.UUID(claimed.EventID.Bytes), "error", err)
		}
		return
	}
	s.logger.Warn("Life OS event not exported to Google", "event", googleuuid.UUID(claimed.EventID.Bytes), "error", message, "attempts", updated.Attempts)
	if !errors.Is(cause, errCalendarGone) && (claimed.Attempts == 0 || deref(claimed.LastError) != message) {
		s.service.recordExportFailed(ctx, updated, message, now)
	}
}

// exportConflict handles a copy changed in Google since the last export:
// Google's version is kept and applied to the Life OS event (or, deleted in
// Google, the event is cancelled), and the conflict recorded.
func (s *Syncer) exportConflict(ctx context.Context, claimed store.GoogleExport, event store.Event, calendarID, copyID string) error {
	current, err := s.service.getEvent(ctx, calendarID, copyID)
	if err != nil && !IsGone(err) {
		return err
	}
	now := s.now()
	connection, _, err2 := s.service.Connection(ctx)
	if err2 != nil {
		return err2
	}
	if IsGone(err) || current.Cancelled() {
		if err := s.service.inTx(ctx, func(q *store.Queries) error { return s.copyDeleted(ctx, q, claimed, now) }); err != nil {
			return err
		}
		s.service.recordConflict(ctx, event, LifeOSCalendarName, conflictExport, now)
		return nil
	}
	if err := s.rememberCopies(ctx, connection, calendarID, []Event{current}); err != nil {
		return err
	}
	item, err := s.copyItem(ctx, connection, calendarID, current)
	if err != nil {
		return err
	}
	var after store.Event
	var changed bool
	err = s.service.inTx(ctx, func(q *store.Queries) error {
		latest, err := q.GetEvent(ctx, event.ID)
		if err != nil {
			return err
		}
		if after, changed, err = applyCopy(ctx, q, latest, item, s.owner); err != nil {
			return err
		}
		return q.LinkGoogleExport(ctx, store.LinkGoogleExportParams{EventID: claimed.EventID, Etag: optional(current.ETag), GoogleUpdated: googleTime(current.Updated), Settle: true})
	})
	if err != nil {
		return err
	}
	// The lease goes with the settled export.
	if err := s.service.queries.ParkGoogleExport(ctx, store.ParkGoogleExportParams{EventID: claimed.EventID}); err != nil {
		return err
	}
	if changed {
		s.service.recordCopyChange(ctx, event, after, now)
	}
	s.service.recordConflict(ctx, after, LifeOSCalendarName, conflictExport, now)
	return nil
}

// copyItem maps a copy (with its stored changed instances) as the inbound
// sync does.
func (s *Syncer) copyItem(ctx context.Context, connection store.GoogleConnection, calendarID string, master Event) (sources.Item, error) {
	events := []Event{master}
	zone := s.owner
	if row, err := s.service.queries.GetGoogleCalendarByGoogleID(ctx, store.GetGoogleCalendarByGoogleIDParams{ConnectionID: connection.ID, GoogleCalendarID: calendarID}); err == nil {
		if loc := loadZone(deref(row.TimeZone)); loc != nil {
			zone = loc
		}
		rows, err := s.service.queries.ListStoredGoogleInstances(ctx, store.ListStoredGoogleInstancesParams{CalendarID: row.ID, RecurringEventID: &master.ID})
		if err != nil {
			return sources.Item{}, err
		}
		for _, stored := range rows {
			var instance Event
			if err := json.Unmarshal(stored.Data, &instance); err == nil {
				events = append(events, instance)
			}
		}
	}
	now := s.now()
	m := mapper{owner: s.owner, zone: zone, now: now, windowStart: now.Add(-SyncLookback)}
	items, _ := m.derive(events)
	for _, item := range items {
		if item.ExternalUID == master.ID {
			return item, nil
		}
	}
	return sources.Item{}, fmt.Errorf("Google's version of %s could not be read", master.ID)
}

// rememberCopies stores what Google answered for the copies, so the inbound
// sync of the Life OS calendar recognizes them as Calendar's own writes.
func (s *Syncer) rememberCopies(ctx context.Context, connection store.GoogleConnection, calendarID string, events []Event) error {
	row, err := s.service.queries.GetGoogleCalendarByGoogleID(ctx, store.GetGoogleCalendarByGoogleIDParams{ConnectionID: connection.ID, GoogleCalendarID: calendarID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rows := make([]storedEvent, 0, len(events))
	for _, event := range events {
		if event.ID != "" {
			rows = append(rows, storedEvent{ID: event.ID, RecurringEventID: event.RecurringEventID, Data: event})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	return s.service.queries.UpsertGoogleEvents(ctx, store.UpsertGoogleEventsParams{CalendarID: row.ID, Items: raw})
}

// exportOccurrences writes a series' changed occurrences as changed
// instances of its copy (copyID), and puts back the instances of
// occurrences that no longer have changes (previous). It returns the
// instances Google answered and the recurrence ids now exported.
func (s *Syncer) exportOccurrences(ctx context.Context, calendarID, copyID string, event store.Event, fields lifeOSFields, previous []string) ([]Event, []string, error) {
	keys := []string{}
	var written []Event
	if event.RecurrenceFrequency == nil {
		return nil, keys, nil
	}
	write := func(key string, body map[string]any) error {
		instance, err := s.service.instanceOf(ctx, calendarID, copyID, key, event.AllDay)
		if IsGone(err) {
			return nil // the series no longer has it
		}
		if err != nil {
			return err
		}
		updated, err := s.service.patchEvent(ctx, calendarID, instance.ID, "", body)
		if err != nil {
			return err
		}
		written = append(written, updated)
		return nil
	}
	for _, key := range sortedKeys(fields.occurrences) {
		occurrence := fields.occurrences[key]
		if err := write(key, instanceBody(occurrence.Fields, occurrence.effective, s.owner)); err != nil {
			return nil, nil, err
		}
		keys = append(keys, key)
	}
	for _, key := range previous {
		if _, still := fields.occurrences[key]; still || slices.Contains(fields.skipped, key) {
			continue
		}
		natural, ok := fields.natural(key)
		if !ok {
			continue
		}
		if err := write(key, instanceBody(natural, fields.effective, s.owner)); err != nil {
			return nil, nil, err
		}
	}
	return written, keys, nil
}

// lifeOSFields is a Life OS event ready to export: its fields, the
// reminders that apply to it, its skips as EXDATE lines, and its changed
// occurrences.
type lifeOSFields struct {
	Fields
	effective   []sources.Reminder
	exdates     []string
	skipped     []string
	occurrences map[string]occurrenceFields
	// duration and days place an occurrence by its recurrence id.
	duration time.Duration
	days     int
}

type occurrenceFields struct {
	Fields
	effective []sources.Reminder
}

// natural is the occurrence with recurrence id key as the series has it.
func (f lifeOSFields) natural(key string) (Fields, bool) {
	out := f.Fields
	out.Recurrence = nil
	if f.AllDay {
		day, err := time.Parse(time.DateOnly, key)
		if err != nil {
			return Fields{}, false
		}
		out.StartDate, out.EndDate = day, day.AddDate(0, 0, f.days)
		return out, true
	}
	start, err := time.Parse(time.RFC3339, key)
	if err != nil {
		return Fields{}, false
	}
	out.StartAt, out.EndAt = start, start.Add(f.duration)
	return out, true
}

// lifeOSFields reads a Life OS event for export, with the owner's default
// reminders and its occurrence exceptions.
func (s *Service) lifeOSFields(ctx context.Context, event store.Event) (lifeOSFields, error) {
	fields := eventFields(event)
	defaults, err := s.ownerDefaults(ctx, event.AllDay)
	if err != nil {
		return lifeOSFields{}, err
	}
	out := lifeOSFields{Fields: fields, effective: effective(fields, defaults), occurrences: map[string]occurrenceFields{}}
	if event.AllDay {
		out.days = int(event.EndDate.Time.Sub(event.StartDate.Time).Hours() / 24)
	} else {
		out.duration = event.EndAt.Time.Sub(event.StartAt.Time)
	}
	if event.RecurrenceFrequency == nil {
		return out, nil
	}
	rows, err := s.queries.ListEventExceptions(ctx, []pgtype.UUID{event.ID})
	if err != nil {
		return lifeOSFields{}, err
	}
	for _, row := range rows {
		if row.CancelledAt.Valid {
			if line, ok := ExDate(row.RecurrenceID, event.AllDay); ok {
				out.exdates = append(out.exdates, line)
				out.skipped = append(out.skipped, row.RecurrenceID)
			}
			continue
		}
		changes, err := decodeChanges(row.Changes)
		if err != nil {
			return lifeOSFields{}, err
		}
		natural, ok := out.natural(row.RecurrenceID)
		if !ok || changes.Empty() {
			continue
		}
		occurrence := changes.applyTo(natural)
		out.occurrences[row.RecurrenceID] = occurrenceFields{Fields: occurrence, effective: effective(occurrence, defaults)}
	}
	return out, nil
}

// ownerDefaults are the owner's default reminders for timed or all-day
// Life OS events.
func (s *Service) ownerDefaults(ctx context.Context, allDay bool) ([]sources.Reminder, error) {
	settings, err := s.queries.GetSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	raw := settings.TimedDefaultReminders
	if allDay {
		raw = settings.AllDayDefaultReminders
	}
	var list []sources.Reminder
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode default reminders: %w", err)
	}
	return list, nil
}

// effective are the reminders that apply: the defaults when used, then the
// event's own.
func effective(f Fields, defaults []sources.Reminder) []sources.Reminder {
	var out []sources.Reminder
	if f.UseDefaultReminders {
		out = append(out, defaults...)
	}
	return append(out, f.Reminders...)
}

// eventFields is a stored event as Fields.
func eventFields(event store.Event) Fields {
	fields := Fields{
		Title: event.Title, Description: event.Description, Location: event.Location, AllDay: event.AllDay,
		TimeZone: event.TimeZone, UseDefaultReminders: event.UseDefaultReminders, Priority: event.Priority,
	}
	if event.AllDay {
		fields.StartDate, fields.EndDate = event.StartDate.Time, event.EndDate.Time
	} else {
		fields.StartAt, fields.EndAt = event.StartAt.Time, event.EndAt.Time
	}
	_ = json.Unmarshal(event.Reminders, &fields.Reminders)
	if event.RecurrenceFrequency != nil && event.RecurrenceInterval != nil {
		rule := &sources.Recurrence{Frequency: *event.RecurrenceFrequency, Interval: int(*event.RecurrenceInterval), ByWeekday: event.RecurrenceByWeekday}
		if event.RecurrenceCount != nil {
			rule.Count = int(*event.RecurrenceCount)
		}
		if event.RecurrenceUntil.Valid {
			until := event.RecurrenceUntil.Time
			rule.Until = &until
		}
		fields.Recurrence = rule
	}
	return fields
}

// storedOccurrence are one occurrence's stored changes (the API's form,
// see the api package's occurrence exceptions).
type storedOccurrence struct {
	sources.Changes
}

// decodeChanges reads stored occurrence changes; a null description or
// location clears it.
func decodeChanges(raw []byte) (storedOccurrence, error) {
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil {
		return storedOccurrence{}, fmt.Errorf("decode occurrence changes: %w", err)
	}
	var out storedOccurrence
	text := func(name string) (*sources.Text, error) {
		value, ok := present[name]
		if !ok {
			return nil, nil
		}
		var text *string
		if err := json.Unmarshal(value, &text); err != nil {
			return nil, err
		}
		return &sources.Text{Value: text}, nil
	}
	var err error
	if value, ok := present["title"]; ok {
		var title string
		if err := json.Unmarshal(value, &title); err != nil {
			return storedOccurrence{}, err
		}
		out.Title = &title
	}
	if out.Description, err = text("description"); err != nil {
		return storedOccurrence{}, err
	}
	if out.Location, err = text("location"); err != nil {
		return storedOccurrence{}, err
	}
	instant := func(name string) (*time.Time, error) {
		value, ok := present[name]
		if !ok {
			return nil, nil
		}
		var at time.Time
		if err := json.Unmarshal(value, &at); err != nil {
			return nil, err
		}
		return &at, nil
	}
	date := func(name string) (*time.Time, error) {
		value, ok := present[name]
		if !ok {
			return nil, nil
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return nil, err
		}
		day, err := time.Parse(time.DateOnly, text)
		return &day, err
	}
	if out.StartAt, err = instant("startAt"); err != nil {
		return storedOccurrence{}, err
	}
	if out.EndAt, err = instant("endAt"); err != nil {
		return storedOccurrence{}, err
	}
	if out.StartDate, err = date("startDate"); err != nil {
		return storedOccurrence{}, err
	}
	if out.EndDate, err = date("endDate"); err != nil {
		return storedOccurrence{}, err
	}
	if value, ok := present["reminders"]; ok {
		var list []sources.Reminder
		if err := json.Unmarshal(value, &list); err != nil {
			return storedOccurrence{}, err
		}
		out.Reminders = &list
	}
	if value, ok := present["useDefaultReminders"]; ok {
		var use bool
		if err := json.Unmarshal(value, &use); err != nil {
			return storedOccurrence{}, err
		}
		out.UseDefaultReminders = &use
	}
	if value, ok := present["priority"]; ok {
		var priority string
		if err := json.Unmarshal(value, &priority); err != nil {
			return storedOccurrence{}, err
		}
		out.Priority = &priority
	}
	return out, nil
}

// applyTo is the occurrence (as its series has it) with the changes.
func (c storedOccurrence) applyTo(f Fields) Fields {
	if c.Title != nil {
		f.Title = *c.Title
	}
	if c.Description != nil {
		f.Description = c.Description.Value
	}
	if c.Location != nil {
		f.Location = c.Location.Value
	}
	if c.StartAt != nil && c.EndAt != nil && !f.AllDay {
		f.StartAt, f.EndAt = *c.StartAt, *c.EndAt
	}
	if c.StartDate != nil && c.EndDate != nil && f.AllDay {
		f.StartDate, f.EndDate = *c.StartDate, *c.EndDate
	}
	if c.Reminders != nil {
		f.Reminders = *c.Reminders
	}
	if c.UseDefaultReminders != nil {
		f.UseDefaultReminders = *c.UseDefaultReminders
	}
	if c.Priority != nil {
		f.Priority = c.Priority
	}
	return f
}

func (s *Service) recordCalendarCreated(ctx context.Context, connection store.GoogleConnection, entry CalendarEntry, note string, now time.Time) {
	summary := fmt.Sprintf("Created the “%s” calendar in Google for %s: events created in Life OS are exported to it", entry.Summary, connection.AccountEmail)
	if note != "" {
		summary = note
	}
	s.activity.Emit(ctx, activity.Event{
		ID:         activity.EventID(EventCalendarCreated, entry.ID),
		Type:       EventCalendarCreated,
		Subject:    connectionSubject(connection),
		Actor:      SyncActor,
		Outcome:    activity.Succeeded,
		Summary:    activity.Preview(summary, 500),
		Data:       map[string]any{"accountEmail": connection.AccountEmail, "googleCalendarId": entry.ID, "recreated": note != ""},
		OccurredAt: now,
	})
}

func (s *Service) recordExportFailed(ctx context.Context, export store.GoogleExport, message string, now time.Time) {
	id := googleuuid.UUID(export.EventID.Bytes).String()
	title := "a Life OS event"
	if event, err := s.queries.GetEvent(ctx, export.EventID); err == nil {
		title = "“" + activity.Preview(event.Title, 120) + "”"
	}
	s.activity.Emit(ctx, activity.Event{
		ID:         activity.EventID(EventExportFailed, id, now.UTC().Format(time.RFC3339Nano)),
		Type:       EventExportFailed,
		Subject:    activity.Subject{Type: "calendar_event", ID: id},
		Actor:      SyncActor,
		Outcome:    activity.Failed,
		Summary:    activity.Preview(fmt.Sprintf("Exporting %s to Google failed: %s. Retrying automatically.", title, message), 500),
		Data:       map[string]any{"error": message, "attempts": export.Attempts},
		OccurredAt: now,
	})
}
