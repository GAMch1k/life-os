package google

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/services/google-calendar/internal/calendar"
	store "github.com/GAMch1k/life-os/services/google-calendar/internal/repository/generated"
)

// Calendar's change feed (GET /v1/integrations/changes, scope own) brings
// what this service writes to Google: every change of a Life OS event (to
// export to the Life OS calendar) and the owner's edits of the events of
// the Google calendars the account may edit (to write to Google). Each page
// is handled in one transaction with the position it leaves the feed at, so
// a change is queued exactly once: Life OS events in google_exports, edits
// in google_pushes, each with the event as the feed gave it. Entries that
// are only this service's own writes are skipped (an exported event's
// payload is still refreshed). A new connection, an expired cursor, and
// switching export on read the feed from a snapshot: the upcoming Life OS
// events are exported, those already exported are only remembered, and an
// export whose event the snapshot did not list is deleted from Google.

// feedPageSize is how many changes one feed request asks for.
const feedPageSize = 200

// maxFeedPages bounds one drain (a large snapshot finishes next round).
const maxFeedPages = 100

// DrainFeed reads the change feed until it has nothing more now.
func (s *Syncer) DrainFeed(ctx context.Context) error {
	for range maxFeedPages {
		connection, found, err := s.service.Connection(ctx)
		if err != nil || !found {
			return err
		}
		cursor := deref(connection.FeedCursor)
		page, err := s.service.calendar.Changes(ctx, cursor, feedPageSize)
		if calendar.HasCode(err, http.StatusGone, "cursor_expired") {
			s.logger.Warn("Calendar's change feed cursor expired; reading it again from a snapshot")
			if err := s.service.queries.RestartFeed(ctx, connection.ID); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			s.recordFeedError(ctx, connection, err)
			return err
		}
		if err := s.handlePage(ctx, connection, page); err != nil {
			s.recordFeedError(ctx, connection, err)
			return err
		}
		if page.Phase == calendar.PhaseChanges && !page.HasMore {
			return nil
		}
	}
	return nil
}

func (s *Syncer) recordFeedError(ctx context.Context, connection store.GoogleConnection, cause error) {
	message := feedErrorMessage(cause)
	if err := s.service.queries.SetFeedError(context.WithoutCancel(ctx), store.SetFeedErrorParams{Error: &message, Now: timestamptz(s.now()), ID: connection.ID}); err != nil {
		s.logger.Warn("change feed failure not recorded", "error", err)
	}
}

func feedErrorMessage(err error) string {
	var answer *calendar.StatusError
	switch {
	case calendar.IsUnavailable(err):
		return "Calendar could not be reached; Life OS events and edits reach Google once it answers again"
	case errors.As(err, &answer):
		return "Calendar refused to list its changes: " + answer.Message
	}
	return "Calendar's changes could not be handled"
}

// handlePage handles one page and stores the position after it.
func (s *Syncer) handlePage(ctx context.Context, connection store.GoogleConnection, page calendar.ChangePage) error {
	now := s.now()
	snapshot := page.Phase == calendar.PhaseSnapshot
	started := connection.FeedSnapshotStartedAt
	if snapshot && !started.Valid {
		started = timestamptz(now)
	}
	calendars, err := s.service.queries.ListGoogleCalendars(ctx, connection.ID)
	if err != nil {
		return err
	}
	byGoogleID := make(map[string]store.GoogleCalendar, len(calendars))
	for _, row := range calendars {
		byGoogleID[row.GoogleCalendarID] = row
	}
	finished := false
	err = s.service.inTx(ctx, func(q *store.Queries) error {
		for _, change := range page.Items {
			if err := s.handleChange(ctx, q, connection, byGoogleID, change, snapshot, now); err != nil {
				return err
			}
		}
		next := pgtype.Timestamptz{}
		if snapshot {
			if page.HasMore {
				next = started
			} else {
				if err := s.finishSnapshot(ctx, q, connection, started, now); err != nil {
					return err
				}
				finished = true
			}
		}
		cursor := page.NextCursor
		return q.SetFeedPosition(ctx, store.SetFeedPositionParams{Cursor: &cursor, SnapshotStartedAt: next, Now: timestamptz(now), ID: connection.ID})
	})
	if err != nil {
		return err
	}
	if finished {
		s.logger.Info("Calendar's change feed snapshot read")
	}
	return nil
}

// handleChange queues what one entry asks of Google.
func (s *Syncer) handleChange(ctx context.Context, q *store.Queries, connection store.GoogleConnection, calendars map[string]store.GoogleCalendar, change calendar.Change, snapshot bool, now time.Time) error {
	id, err := googleuuid.Parse(change.EventID)
	if err != nil {
		return fmt.Errorf("change feed entry with an unreadable event id: %w", err)
	}
	if change.Source == nil {
		return s.handleLifeOSChange(ctx, q, connection, pgID(id), change, snapshot, now)
	}
	if snapshot || change.Own() || change.Source.Integration != calendar.Integration {
		return nil // an edit is only what changes after the snapshot
	}
	calendarRow, ok := calendars[change.Source.ExternalID]
	if !ok || !calendarRow.SyncEnabled {
		return nil
	}
	if change.Deleted {
		return q.QueuePush(ctx, store.QueuePushParams{EventID: pgID(id), CalendarID: calendarRow.ID, ExternalUid: change.Source.ExternalUID, Deleted: true, Now: timestamptz(now)})
	}
	if change.Event == nil || change.Event.ReadOnlyReason != nil {
		return nil // only its reminders or priority changed in Life OS, kept there
	}
	encoded, err := encodePayload(*change.Event, change.Exceptions)
	if err != nil {
		return err
	}
	return q.QueuePush(ctx, store.QueuePushParams{EventID: pgID(id), CalendarID: calendarRow.ID, ExternalUid: change.Source.ExternalUID, Payload: encoded, Now: timestamptz(now)})
}

// handleLifeOSChange queues the export of a Life OS event's change, or
// the deletion of its copy.
func (s *Syncer) handleLifeOSChange(ctx context.Context, q *store.Queries, connection store.GoogleConnection, id pgtype.UUID, change calendar.Change, snapshot bool, now time.Time) error {
	if change.Deleted {
		if change.Own() {
			return nil
		}
		forgotten, err := q.DeleteUnexportedGoogleExport(ctx, id)
		if err != nil || forgotten > 0 {
			return err
		}
		return q.MarkGoogleExportDeleted(ctx, store.MarkGoogleExportDeletedParams{EventID: id, Now: timestamptz(now)})
	}
	if change.Event == nil {
		return nil
	}
	encoded, err := encodePayload(*change.Event, change.Exceptions)
	if err != nil {
		return err
	}
	existing, err := q.GetGoogleExport(ctx, id)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	switch {
	case change.Own(), snapshot && exists && existing.ConnectionID == connection.ID, !connection.ExportEnabled:
		// This service's own write (such as a change made in Google
		// applied to the event), an event the snapshot finds already
		// exported, or export off: the event is remembered, not exported.
		if exists {
			_, err := q.SetGoogleExportPayload(ctx, store.SetGoogleExportPayloadParams{Payload: encoded, Now: timestamptz(now), EventID: id})
			return err
		}
		return nil
	case snapshot && !upcoming(*change.Event, now):
		return nil // past events stay in Life OS
	}
	return q.QueueGoogleExport(ctx, store.QueueGoogleExportParams{EventID: id, ConnectionID: connection.ID, Payload: encoded, Now: timestamptz(now)})
}

// finishSnapshot: every export whose event a complete snapshot did not list
// no longer has an event; its copy is deleted from Google. The upcoming
// Life OS events were queued for export as the snapshot listed them.
func (s *Syncer) finishSnapshot(ctx context.Context, q *store.Queries, connection store.GoogleConnection, started pgtype.Timestamptz, now time.Time) error {
	unseen, err := q.ListUnseenGoogleExports(ctx, store.ListUnseenGoogleExportsParams{ConnectionID: connection.ID, StartedAt: started})
	if err != nil {
		return err
	}
	for _, export := range unseen {
		forgotten, err := q.DeleteUnexportedGoogleExport(ctx, export.EventID)
		if err != nil {
			return err
		}
		if forgotten == 0 {
			if err := q.MarkGoogleExportDeleted(ctx, store.MarkGoogleExportDeletedParams{EventID: export.EventID, Now: timestamptz(now)}); err != nil {
				return err
			}
		}
	}
	if connection.ExportEnabled {
		return q.MarkGoogleExportBackfilled(ctx, store.MarkGoogleExportBackfilledParams{ID: connection.ID, Now: timestamptz(now)})
	}
	return nil
}

// upcoming reports whether an active Life OS event is not over at now: a
// single event that ends after now, a series without an end or whose last
// occurrence may end after now (a series ending after a count of
// occurrences is taken as lasting count times its interval at most).
func upcoming(event calendar.Event, now time.Time) bool {
	if event.Status != "active" {
		return false
	}
	var start, end time.Time
	if event.AllDay {
		first, last := parseDate(event.StartDate), parseDate(event.EndDate)
		start = first
		end = last.AddDate(0, 0, 1).Add(14 * time.Hour) // the last day is over everywhere
	} else if event.StartAt != nil && event.EndAt != nil {
		start, end = *event.StartAt, *event.EndAt
	} else {
		return true
	}
	rule := event.Recurrence
	if rule == nil {
		return end.After(now)
	}
	length := end.Sub(start)
	switch {
	case rule.Count != nil:
		var unit time.Duration
		switch rule.Frequency {
		case "daily":
			unit = 24 * time.Hour
		case "weekly":
			unit = 7 * 24 * time.Hour
		case "monthly":
			unit = 31 * 24 * time.Hour
		default:
			unit = 366 * 24 * time.Hour
		}
		return end.Add(unit * time.Duration(*rule.Count*max(rule.Interval, 1))).After(now)
	case rule.Until != nil:
		return rule.Until.Add(length).Add(24 * time.Hour).After(now)
	}
	return true
}
