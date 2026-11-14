package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/calendar"
	store "github.com/GAMch1k/life-os/services/google-calendar/internal/repository/generated"
)

// Each Google calendar chosen to sync is a source in Calendar, registered
// under the integration google-calendar with the calendar's Google id:
// provider Google, read-only unless the account may edit the calendar, its
// name the calendar's, and its default reminders the calendar's defaults
// (when a listing carried them). A calendar that stops syncing (switched
// off, unlisted, disconnected, or another account connected) has its
// source removed from Calendar with its events; the removal is queued in
// google_source_removals and retried until Calendar confirms.

// ProviderLabel is the provider Calendar shows for Google's sources.
const ProviderLabel = "Google"

// Why a synced calendar's source was removed.
const (
	removedSyncOff      = "sync_disabled"
	removedDisconnected = "disconnected"
	removedUnlisted     = "calendar_unlisted"
	removedReplaced     = "account_replaced"
)

// EventSourceRemoved is recorded when a synced calendar's source left
// Calendar (shared with feeds: subject calendar_source).
const EventSourceRemoved = "calendar.source.removed"

// registration is what a calendar is registered with.
type registration struct {
	Name             string          `json:"name"`
	ReadOnly         bool            `json:"readOnly"`
	DefaultReminders json.RawMessage `json:"defaultReminders,omitempty"`
}

// ensureSource registers calendar's source in Calendar when it is not, or
// when its name, access, or (with defaults, from a listing) default
// reminders changed, after removing a source still queued for removal
// under the same id. It returns the calendar with its source.
func (s *Syncer) ensureSource(ctx context.Context, calendarRow store.GoogleCalendar, defaults []Reminder) (store.GoogleCalendar, error) {
	if pending, err := s.service.queries.GetSourceRemoval(ctx, calendarRow.GoogleCalendarID); err == nil {
		// Switched off and on again before Calendar removed the old
		// source: it goes first, so the calendar starts afresh.
		if err := s.service.removeSource(ctx, pending); err != nil {
			return calendarRow, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return calendarRow, err
	}
	wanted := registration{Name: calendarRow.Summary, ReadOnly: !Writable(calendarRow.AccessRole)}
	var last registration
	if calendarRow.Registration != nil {
		_ = json.Unmarshal(calendarRow.Registration, &last)
	}
	wanted.DefaultReminders = last.DefaultReminders
	if defaults != nil {
		encoded := json.RawMessage("null")
		if reminders := relativeReminders(defaults); len(reminders) > 0 {
			raw, err := json.Marshal(reminders)
			if err != nil {
				return calendarRow, err
			}
			encoded = raw
		}
		wanted.DefaultReminders = encoded
	}
	if calendarRow.SourceID.Valid && calendarRow.Registration != nil && wanted.Name == last.Name && wanted.ReadOnly == last.ReadOnly &&
		bytes.Equal(wanted.DefaultReminders, last.DefaultReminders) {
		return calendarRow, nil
	}
	source, err := s.service.calendar.RegisterSource(ctx, calendarRow.GoogleCalendarID, calendar.Registration{
		Name: wanted.Name, Provider: ProviderLabel, ReadOnly: wanted.ReadOnly, DefaultReminders: wanted.DefaultReminders,
	})
	if err != nil {
		return calendarRow, fmt.Errorf("register the calendar with Calendar: %w", err)
	}
	id, err := googleuuid.Parse(source.ID)
	if err != nil {
		return calendarRow, fmt.Errorf("Calendar returned a source without an id: %w", err)
	}
	encoded, err := json.Marshal(wanted)
	if err != nil {
		return calendarRow, err
	}
	return s.service.queries.SetGoogleCalendarSource(ctx, store.SetGoogleCalendarSourceParams{
		SourceID: pgtype.UUID{Bytes: id, Valid: true}, Registration: encoded, ID: calendarRow.ID,
	})
}

// queueRemovals queues removing from Calendar the sources of the
// connection's synced calendars (narrowed to calendars, when given), with
// the number of events Calendar was told of for each.
func queueRemovals(ctx context.Context, q *store.Queries, connection pgtype.UUID, calendars []pgtype.UUID, reason string, now time.Time) error {
	rows, err := q.ListGoogleCalendars(ctx, connection)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if calendars != nil && !containsID(calendars, row.ID) {
			continue
		}
		if !row.SyncEnabled && !row.SourceID.Valid {
			continue // never registered
		}
		events, err := q.CountSentEvents(ctx, row.ID)
		if err != nil {
			return err
		}
		if err := q.QueueSourceRemoval(ctx, store.QueueSourceRemovalParams{
			ExternalID: row.GoogleCalendarID, Name: row.Summary, Events: events, Reason: reason, Now: timestamptz(now),
		}); err != nil {
			return err
		}
	}
	return nil
}

func containsID(ids []pgtype.UUID, id pgtype.UUID) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// removalRetry is the pause after a failed source removal.
const removalRetry = time.Minute

// RemoveSources removes the queued sources from Calendar; one Calendar
// refuses or cannot be reached for is retried later.
func (s *Service) RemoveSources(ctx context.Context) {
	due, err := s.queries.ListDueSourceRemovals(ctx, timestamptz(s.now()))
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("source removals not read", "error", err)
		}
		return
	}
	for _, removal := range due {
		if err := s.removeSource(ctx, removal); err != nil && ctx.Err() == nil {
			s.logger.Warn("calendar source not removed from Calendar yet", "calendar", idHash(removal.ExternalID), "attempts", removal.Attempts, "error", logCode(err))
		}
	}
}

// removeSource deletes one queued source in Calendar and records it.
func (s *Service) removeSource(ctx context.Context, removal store.GoogleSourceRemoval) error {
	source, found, err := s.calendar.GetSource(ctx, removal.ExternalID)
	if err == nil && found {
		err = s.calendar.DeleteSource(ctx, removal.ExternalID)
	}
	if err != nil {
		message := err.Error()
		next := s.now().Add(SyncBackoff(removalRetry, int(removal.Attempts)+1))
		if failErr := s.queries.FailSourceRemoval(ctx, store.FailSourceRemovalParams{LastError: &message, NextAttemptAt: timestamptz(next), ExternalID: removal.ExternalID}); failErr != nil {
			s.logger.Warn("failed source removal not recorded", "error", failErr)
		}
		return err
	}
	if err := s.queries.DeleteSourceRemoval(ctx, removal.ExternalID); err != nil {
		return err
	}
	if found {
		s.recordSourceRemoved(ctx, source, removal)
	}
	return nil
}

// recordSourceRemoved records that a synced calendar's events left Life OS
// (calendar.source.removed, attributed to the request's actor, else this
// service).
func (s *Service) recordSourceRemoved(ctx context.Context, source calendar.Source, removal store.GoogleSourceRemoval) {
	trace := activity.TraceFrom(ctx)
	actor := trace.Actor
	if actor.IsZero() {
		actor = SyncActor
	}
	events := int(removal.Events)
	count := fmt.Sprintf("%d %s", events, plural(events, "event", "events"))
	name := source.Name
	var summary string
	switch removal.Reason {
	case removedSyncOff:
		summary = fmt.Sprintf("%s no longer syncs from Google: %s removed from Life OS", name, count)
	case removedDisconnected:
		summary = fmt.Sprintf("Google Calendar disconnected: %s's %s removed from Life OS", name, count)
	case removedUnlisted:
		summary = fmt.Sprintf("Google no longer lists %s: its %s removed from Life OS", name, count)
	default:
		summary = fmt.Sprintf("Another Google account was connected: %s's %s removed from Life OS", name, count)
	}
	s.activity.Emit(ctx, activity.Event{
		ID:            activity.EventID(EventSourceRemoved, source.ID),
		Type:          EventSourceRemoved,
		Subject:       activity.Subject{Type: "calendar_source", ID: source.ID},
		CorrelationID: trace.CorrelationID,
		CausationID:   trace.CausationID,
		Actor:         actor,
		Outcome:       activity.Succeeded,
		Summary:       activity.Preview(summary, 500),
		Data:          map[string]any{"sourceId": source.ID, "sourceName": name, "sourceKind": "external", "provider": ProviderLabel, "reason": removal.Reason, "removedEvents": events},
	})
}
