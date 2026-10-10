package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// Two-way sync of the calendars chosen to sync (P11-03). Google holds the
// current version of their events: a change made in Life OS is written to
// Google first, with the etag Calendar last stored (If-Match), and the
// event in Life OS then follows Google's answer, derived like a sync's. A
// concurrent change in Google (412) is never overwritten: Calendar reads
// Google's version, shows it, and records the conflict. Nothing changes in
// Life OS when Google cannot be written (unreachable, connection expired).

var (
	// ErrConflict: the event changed in Google since Calendar last read it;
	// Google's version is now shown.
	ErrConflict = errors.New("Changed in Google meanwhile; showing the latest version")
	// ErrDeletedInGoogle: the event no longer exists in Google.
	ErrDeletedInGoogle = errors.New("This event was deleted in Google meanwhile, so it was removed from Life OS")
	// ErrOccurrenceNotInGoogle: Google's series has no such occurrence.
	ErrOccurrenceNotInGoogle = errors.New("Google's repeating event has no such occurrence anymore; it changed in Google meanwhile")
	// errSyncNotRunning: no syncer to derive the events with.
	errSyncNotRunning = errors.New("Google Calendar sync is not running")
)

// ReadOnlyError says why a synced event cannot be changed from Life OS.
type ReadOnlyError struct{ Reason string }

func (e *ReadOnlyError) Error() string { return e.Reason }

// Writable reports whether an account with role may change a calendar's
// events: owner and writer may; reader and freeBusyReader may not.
func Writable(role string) bool { return role == "owner" || role == "writer" }

// ReaderReason is why the events of a calendar the account can only read
// are read-only in Life OS.
func ReaderReason(calendar, role string) string {
	what := "can only read"
	if role == "freeBusyReader" {
		what = "can only see as free or busy"
	}
	return fmt.Sprintf("“%s” is a Google calendar you %s, so its events can only be changed in Google Calendar by someone who may edit it. Its reminders can still be adjusted here.", calendar, what)
}

// ExpandedReason is why one occurrence of a repeating Google event whose
// rule Life OS cannot hold is read-only.
func ExpandedReason(calendar string) string {
	return fmt.Sprintf("This is one occurrence of a repeating event in “%s” whose rule Life OS can't edit, so it can only be changed in Google Calendar. Its reminders can still be adjusted here.", calendar)
}

// Target is a synced Google event that Life OS writes to.
type Target struct {
	// Local is the event in Life OS.
	Local store.Event
	// Calendar is its Google calendar.
	Calendar store.GoogleCalendar
	// Event is Google's event as last stored: a single event or a series'
	// master.
	Event Event
	// Deleted: Google deleted the event (Local was removed from its
	// source); only restoring it is possible.
	Deleted bool
}

// Target finds what a change to the synced event local writes to. It is a
// ReadOnlyError when the calendar is read-only for the account, or local is
// an occurrence expanded from a rule Life OS cannot hold (or a lone
// instance).
func (s *Service) Target(ctx context.Context, local store.Event) (Target, error) {
	if local.SourceKind != "google" || !local.SourceID.Valid || local.ExternalUid == nil {
		return Target{}, fmt.Errorf("event %x is not a synced Google event", local.ID.Bytes)
	}
	calendar, err := s.queries.GetGoogleCalendarBySource(ctx, local.SourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Target{}, ErrNotConnected
	}
	if err != nil {
		return Target{}, err
	}
	if !Writable(calendar.AccessRole) {
		return Target{}, &ReadOnlyError{Reason: ReaderReason(calendar.Summary, calendar.AccessRole)}
	}
	uid := *local.ExternalUid
	row, err := s.queries.GetStoredGoogleEvent(ctx, store.GetStoredGoogleEventParams{CalendarID: calendar.ID, EventID: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		if local.RemovedFromSourceAt.Valid && !strings.Contains(uid, "_") {
			return Target{Local: local, Calendar: calendar, Event: Event{ID: uid}, Deleted: true}, nil
		}
		return Target{}, &ReadOnlyError{Reason: ExpandedReason(calendar.Summary)}
	}
	if err != nil {
		return Target{}, err
	}
	if row.RecurringEventID != nil {
		return Target{}, &ReadOnlyError{Reason: ExpandedReason(calendar.Summary)}
	}
	var event Event
	if err := json.Unmarshal(row.Data, &event); err != nil {
		return Target{}, fmt.Errorf("decode stored Google event: %w", err)
	}
	return Target{Local: local, Calendar: calendar, Event: event}, nil
}

// KeptRecurrence are the lines of the event's recurrence a new rule keeps:
// its EXDATEs (the occurrences skipped in Google).
func (t Target) KeptRecurrence() []string { return exdateLines(t.Event) }

// withToken calls Google with a usable access token, refreshing it once if
// Google no longer accepts it.
func (s *Service) withToken(ctx context.Context, call func(token string) error) error {
	for attempt := 0; ; attempt++ {
		token, err := s.AccessToken(ctx)
		if err != nil {
			return err
		}
		err = call(token)
		if attempt == 0 && isUnauthorized(err) {
			s.forget()
			continue
		}
		return err
	}
}

func (s *Service) getEvent(ctx context.Context, calendarID, eventID string) (Event, error) {
	var event Event
	err := s.withToken(ctx, func(token string) (err error) {
		event, err = s.api.GetEvent(ctx, token, calendarID, eventID)
		return err
	})
	return event, err
}

func (s *Service) insertEvent(ctx context.Context, calendarID string, body map[string]any) (Event, error) {
	var event Event
	err := s.withToken(ctx, func(token string) (err error) {
		event, err = s.api.InsertEvent(ctx, token, calendarID, body)
		return err
	})
	return event, err
}

func (s *Service) patchEvent(ctx context.Context, calendarID, eventID, etag string, body map[string]any) (Event, error) {
	var event Event
	err := s.withToken(ctx, func(token string) (err error) {
		event, err = s.api.PatchEvent(ctx, token, calendarID, eventID, etag, body)
		return err
	})
	return event, err
}

func (s *Service) deleteEvent(ctx context.Context, calendarID, eventID, etag string) error {
	return s.withToken(ctx, func(token string) error {
		return s.api.DeleteEvent(ctx, token, calendarID, eventID, etag)
	})
}

// instanceOf reads the instance of a recurring event that originally starts
// at key: by the id Google gives instances, else through instances.
func (s *Service) instanceOf(ctx context.Context, calendarID, eventID, key string, allDay bool) (Event, error) {
	instance, err := s.getEvent(ctx, calendarID, instanceID(eventID, key, allDay))
	if err == nil || !IsGone(err) {
		return instance, err
	}
	err = s.withToken(ctx, func(token string) (err error) {
		instance, err = s.api.Instance(ctx, token, calendarID, eventID, originalStart(key))
		return err
	})
	return instance, err
}

// UpdateEvent writes a change of a single event, or of a whole series (All
// events), as the PATCH body.
func (s *Service) UpdateEvent(ctx context.Context, t Target, body map[string]any) error {
	if s.syncer == nil {
		return errSyncNotRunning
	}
	etag := t.Event.ETag
	if t.Deleted {
		etag = ""
	}
	updated, err := s.patchEvent(ctx, t.Calendar.GoogleCalendarID, t.Event.ID, etag, body)
	if err != nil {
		return s.writeFailed(ctx, t, t.Event.ID, err)
	}
	return s.syncer.applyWrite(ctx, t.Calendar.ID, []Event{updated}, nil, nil)
}

// DeleteEvent deletes the event in Google (a series with every occurrence).
// Life OS keeps it as cancelled history (removed from its source).
func (s *Service) DeleteEvent(ctx context.Context, t Target) error {
	if s.syncer == nil {
		return errSyncNotRunning
	}
	if !t.Deleted {
		err := s.deleteEvent(ctx, t.Calendar.GoogleCalendarID, t.Event.ID, t.Event.ETag)
		if err != nil && !IsGone(err) {
			return s.writeFailed(ctx, t, t.Event.ID, err)
		}
	}
	return s.syncer.applyWrite(ctx, t.Calendar.ID, nil, []string{t.Event.ID}, nil)
}

// UpdateOccurrence writes a change of one occurrence of a series (This
// event) as the PATCH body of its instance: {"status": "cancelled"} skips
// it.
func (s *Service) UpdateOccurrence(ctx context.Context, t Target, key string, allDay bool, body map[string]any) error {
	if s.syncer == nil {
		return errSyncNotRunning
	}
	instance, err := s.storedInstance(ctx, t, key)
	if err != nil {
		return err
	}
	if instance.ID == "" {
		instance, err = s.instanceOf(ctx, t.Calendar.GoogleCalendarID, t.Event.ID, key, allDay)
		if IsGone(err) {
			return ErrOccurrenceNotInGoogle
		}
		if err != nil {
			return s.writeFailed(ctx, t, t.Event.ID, err)
		}
	}
	updated, err := s.patchEvent(ctx, t.Calendar.GoogleCalendarID, instance.ID, instance.ETag, body)
	if err != nil {
		return s.writeFailed(ctx, t, instance.ID, err)
	}
	return s.syncer.applyWrite(ctx, t.Calendar.ID, []Event{updated}, nil, nil)
}

// storedInstance is the series' stored instance at key (changed or skipped
// before), if any.
func (s *Service) storedInstance(ctx context.Context, t Target, key string) (Event, error) {
	instances, err := s.storedInstances(ctx, t)
	if err != nil {
		return Event{}, err
	}
	for _, instance := range instances {
		if id, ok := instanceRecurrenceID(instance); ok && id == key {
			return instance, nil
		}
	}
	return Event{}, nil
}

func (s *Service) storedInstances(ctx context.Context, t Target) ([]Event, error) {
	rows, err := s.queries.ListStoredGoogleInstances(ctx, store.ListStoredGoogleInstancesParams{CalendarID: t.Calendar.ID, RecurringEventID: &t.Event.ID})
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(rows))
	for _, row := range rows {
		var event Event
		if err := json.Unmarshal(row.Data, &event); err != nil {
			return nil, fmt.Errorf("decode stored Google event: %w", err)
		}
		out = append(out, event)
	}
	return out, nil
}

// instanceRecurrenceID is an instance's original start as a recurrence id:
// a date, or an RFC 3339 instant in UTC.
func instanceRecurrenceID(instance Event) (string, bool) {
	original := instance.OriginalStartTime
	switch {
	case original == nil:
		return "", false
	case original.Date != "":
		return original.Date, true
	}
	start, err := time.Parse(time.RFC3339, original.DateTime)
	if err != nil {
		return "", false
	}
	return recurrenceID(start, time.Time{}, false), true
}

// Carried is one occurrence change of a split series that moves to the new
// series: the PATCH body of its instance there.
type Carried struct {
	Key  string
	Body map[string]any
}

// SplitSeries changes this and following occurrences: it creates the new
// series (started, carrying the occurrence changes from the split on), then
// ends the series with ended (its recurrence lines, with UNTIL before from,
// the split occurrence's recurrence id). The new series is deleted again
// when the old one cannot be ended. It returns the new series' Google id.
func (s *Service) SplitSeries(ctx context.Context, t Target, ended []string, started map[string]any, carried []Carried, from string) (string, error) {
	if s.syncer == nil {
		return "", errSyncNotRunning
	}
	calendarID := t.Calendar.GoogleCalendarID
	created, err := s.insertEvent(ctx, calendarID, started)
	if err != nil {
		return "", s.writeFailed(ctx, t, t.Event.ID, err)
	}
	upserts := []Event{created}
	allDay := created.Start != nil && created.Start.Date != ""
	for _, change := range carried {
		instance, err := s.instanceOf(ctx, calendarID, created.ID, change.Key, allDay)
		if err == nil {
			instance, err = s.patchEvent(ctx, calendarID, instance.ID, "", change.Body)
		}
		if err != nil {
			s.logger.Warn("occurrence change not carried to the new Google series", "calendar", calendarID, "occurrence", change.Key, "error", err)
			continue
		}
		upserts = append(upserts, instance)
	}
	updated, err := s.patchEvent(ctx, calendarID, t.Event.ID, t.Event.ETag, map[string]any{"recurrence": ended})
	if err != nil {
		if undo := s.deleteEvent(ctx, calendarID, created.ID, ""); undo != nil {
			s.logger.Warn("new Google series of a failed split not deleted", "calendar", calendarID, "event", created.ID, "error", undo)
		}
		return "", s.writeFailed(ctx, t, t.Event.ID, err)
	}
	upserts = append(upserts, updated)
	stale, err := s.dropInstancesFrom(ctx, t, from)
	if err != nil {
		return "", err
	}
	return created.ID, s.syncer.applyWrite(ctx, t.Calendar.ID, upserts, nil, stale)
}

// TruncateSeries deletes this and following occurrences: the series ends
// with ended (its recurrence lines, UNTIL before from).
func (s *Service) TruncateSeries(ctx context.Context, t Target, ended []string, from string) error {
	if s.syncer == nil {
		return errSyncNotRunning
	}
	updated, err := s.patchEvent(ctx, t.Calendar.GoogleCalendarID, t.Event.ID, t.Event.ETag, map[string]any{"recurrence": ended})
	if err != nil {
		return s.writeFailed(ctx, t, t.Event.ID, err)
	}
	stale, err := s.dropInstancesFrom(ctx, t, from)
	if err != nil {
		return err
	}
	return s.syncer.applyWrite(ctx, t.Calendar.ID, []Event{updated}, nil, stale)
}

// dropInstancesFrom deletes in Google (best effort) the series' stored
// changed or skipped instances from the occurrence from on, which the
// series no longer has, and returns their ids to forget.
func (s *Service) dropInstancesFrom(ctx context.Context, t Target, from string) ([]string, error) {
	instances, err := s.storedInstances(ctx, t)
	if err != nil {
		return nil, err
	}
	var stale []string
	for _, instance := range instances {
		key, ok := instanceRecurrenceID(instance)
		if !ok || key < from {
			continue
		}
		stale = append(stale, instance.ID)
		if instance.Cancelled() {
			continue
		}
		if err := s.deleteEvent(ctx, t.Calendar.GoogleCalendarID, instance.ID, ""); err != nil && !IsGone(err) {
			s.logger.Warn("changed occurrence after the end of a Google series not deleted", "calendar", t.Calendar.GoogleCalendarID, "event", instance.ID, "error", err)
		}
	}
	return stale, nil
}

// writeFailed turns Google's refusal of a write to eventID (t's event or
// one of its instances) into Calendar's answer. A concurrent change (412)
// shows Google's version and records the conflict; an event deleted
// meanwhile leaves Life OS; a refusal of a calendar the account cannot
// write is a ReadOnlyError.
func (s *Service) writeFailed(ctx context.Context, t Target, eventID string, err error) error {
	switch {
	case IsConflict(err):
		return s.conflict(ctx, t, eventID)
	case IsGone(err) && eventID != t.Event.ID:
		return s.conflict(ctx, t, t.Event.ID)
	case IsGone(err):
		if applyErr := s.syncer.applyWrite(ctx, t.Calendar.ID, nil, []string{t.Event.ID}, nil); applyErr != nil {
			return applyErr
		}
		return ErrDeletedInGoogle
	case IsForbidden(err):
		return &ReadOnlyError{Reason: fmt.Sprintf("Google refused the change: “%s” cannot be edited with this account (%s)", t.Calendar.Summary, err.Error())}
	}
	return err
}

// conflict reads eventID's current version (t's event or one of its
// instances) from Google, shows it in Life OS, records the conflict, and
// returns ErrConflict.
func (s *Service) conflict(ctx context.Context, t Target, eventID string) error {
	current, err := s.getEvent(ctx, t.Calendar.GoogleCalendarID, eventID)
	var upserts []Event
	var forget []string
	switch {
	case err == nil && (!current.Cancelled() || current.RecurringEventID != ""):
		upserts = []Event{current}
	case (err == nil || IsGone(err)) && eventID == t.Event.ID:
		forget = []string{t.Event.ID}
	default:
		s.logger.Warn("Google's version of a conflicting event not read; the next sync brings it", "calendar", t.Calendar.GoogleCalendarID, "error", err)
	}
	if len(upserts) > 0 || len(forget) > 0 {
		if err := s.syncer.applyWrite(ctx, t.Calendar.ID, upserts, forget, nil); err != nil {
			return err
		}
	}
	shown := t.Local
	if latest, err := s.queries.GetEvent(ctx, t.Local.ID); err == nil {
		shown = latest
	}
	s.recordConflict(ctx, shown, t.Calendar.Summary, conflictEdit, s.now())
	return ErrConflict
}

// applyWrite stores what Google answered to Calendar's writes in a synced
// calendar (upserts; forget deletes events with their instances,
// forgetInstances single instances) and derives the calendar's events
// again, in one transaction holding the calendar.
func (s *Syncer) applyWrite(ctx context.Context, calendarID pgtype.UUID, upserts []Event, forget, forgetInstances []string) error {
	ctx = context.WithoutCancel(ctx)
	return s.service.inTx(ctx, func(q *store.Queries) error {
		current, err := q.LockGoogleCalendar(ctx, calendarID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // dropped meanwhile: nothing to show
		}
		if err != nil {
			return err
		}
		if len(upserts) > 0 {
			rows := make([]storedEvent, 0, len(upserts))
			for _, event := range upserts {
				if event.ID != "" {
					rows = append(rows, storedEvent{ID: event.ID, RecurringEventID: event.RecurringEventID, Data: event})
				}
			}
			raw, err := json.Marshal(rows)
			if err != nil {
				return err
			}
			if err := q.UpsertGoogleEvents(ctx, store.UpsertGoogleEventsParams{CalendarID: current.ID, Items: raw}); err != nil {
				return err
			}
		}
		if len(forget) > 0 {
			if err := q.DeleteGoogleEventsByID(ctx, store.DeleteGoogleEventsByIDParams{CalendarID: current.ID, EventIds: forget}); err != nil {
				return err
			}
		}
		if len(forgetInstances) > 0 {
			if err := q.DeleteGoogleInstances(ctx, store.DeleteGoogleInstancesParams{CalendarID: current.ID, EventIds: forgetInstances}); err != nil {
				return err
			}
		}
		if !current.SyncEnabled {
			return nil
		}
		source, err := q.GetGoogleSource(ctx, current.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = s.derive(ctx, q, current, source, current.SyncWindowStart, deref(current.TimeZone), s.now().UTC(), nil)
		return err
	})
}
