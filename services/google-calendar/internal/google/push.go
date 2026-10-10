package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/services/google-calendar/internal/calendar"
	store "github.com/GAMch1k/life-os/services/google-calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/sources"
)

// Two-way sync of the calendars chosen to sync (P11-03). Google holds the
// current version of their events. The owner's edit of an event of a
// calendar the account may edit is stored in Life OS by Calendar, comes
// here through Calendar's change feed (google_pushes, see feed.go), and is
// written to Google a few seconds later: only what the edit changed, with
// the etag last stored (If-Match), so a concurrent change in Google (412)
// is never overwritten. Google's answer is stored and reaches Calendar with
// the calendar's next sync, which follows right away. When Google refuses
// an edit (changed meanwhile, deleted, or not allowed), Google's version is
// kept: the next sync gives it to Calendar again, and a conflict is
// recorded. While an edit of a calendar's events waits, that calendar is
// not synced from Google, so the edit is never overwritten first.

// ReadOnlyError says why a synced event cannot be changed from Life OS.
type ReadOnlyError struct{ Reason string }

func (e *ReadOnlyError) Error() string { return e.Reason }

// Writable reports whether an account with role may change a calendar's
// events: owner and writer may; reader and freeBusyReader may not.
func Writable(role string) bool { return role == "owner" || role == "writer" }

// ReaderReason is why the events of a calendar the account can only read
// are read-only in Life OS.
func ReaderReason(calendarName, role string) string {
	what := "can only read"
	if role == "freeBusyReader" {
		what = "can only see as free or busy"
	}
	return fmt.Sprintf("“%s” is a Google calendar you %s, so its events can only be changed in Google Calendar by someone who may edit it.", calendarName, what)
}

// ExpandedReason is why one occurrence of a repeating Google event whose
// rule Life OS cannot hold is read-only.
func ExpandedReason(calendarName string) string {
	return fmt.Sprintf("This is one occurrence of a repeating event in “%s” whose rule Life OS can't edit, so it can only be changed in Google Calendar.", calendarName)
}

// Push timing.
const (
	// pushLease bounds writing one edit; a crashed one is retried after it.
	pushLease = 2 * time.Minute
	// maxPushesPerRun bounds the edits written in one run.
	maxPushesPerRun = 200
	// pushRetry is the first pause after a failed attempt (doubling up to
	// an hour); an expired connection waits for the reconnect.
	pushRetry = 30 * time.Second
)

// errPushLater: the edit could not be written now (Google or Calendar
// unreachable, the connection expired); it is retried.
var errPushLater = errors.New("the edit is written to Google later")

// PushDue writes the waiting edits to Google, one after another.
func (s *Syncer) PushDue(ctx context.Context) {
	if !s.service.Configured() {
		return
	}
	for range maxPushesPerRun {
		if ctx.Err() != nil {
			return
		}
		now := s.now().UTC()
		claimed, err := s.service.queries.ClaimDuePush(ctx, store.ClaimDuePushParams{Now: timestamptz(now), LeaseUntil: timestamptz(now.Add(pushLease))})
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("waiting edits not read", "error", err)
			}
			return
		}
		err = s.push(ctx, claimed)
		if err != nil {
			s.failPush(ctx, claimed, err)
			continue
		}
		if err := s.service.queries.FinishPush(ctx, store.FinishPushParams{EventID: claimed.EventID, ClaimedRequestedAt: claimed.RequestedAt}); err != nil {
			s.logger.Warn("written edit not recorded", "error", err)
		}
		if err := s.service.queries.ReleasePush(ctx, claimed.EventID); err != nil {
			s.logger.Warn("edit lease not released", "error", err)
		}
	}
}

func (s *Syncer) failPush(ctx context.Context, claimed store.GooglePush, cause error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	message := syncErrorMessage(cause)
	delay := SyncBackoff(pushRetry, int(claimed.Attempts)+1)
	if errors.Is(cause, ErrExpired) {
		delay = time.Hour // reconnecting makes it due at once
	}
	updated, err := s.service.queries.FailPush(ctx, store.FailPushParams{LastError: &message, NextAttemptAt: timestamptz(s.now().Add(delay)), EventID: claimed.EventID})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.logger.Warn("failed edit not recorded", "error", err)
		}
		return
	}
	s.logger.Warn("edit not written to Google yet", "event", googleuuid.UUID(claimed.EventID.Bytes), "error", message, "attempts", updated.Attempts)
}

// pushTarget is what an edit writes to: the calendar, Google's event as
// last stored (a single event or a series' master), and the item derived
// from it.
type pushTarget struct {
	calendar store.GoogleCalendar
	event    Event
	item     sources.Item
	// deleted: Google no longer has the event (its Calendar event was
	// removed from its source); only restoring it is possible.
	deleted bool
}

// push writes one waiting edit to Google.
func (s *Syncer) push(ctx context.Context, claimed store.GooglePush) error {
	calendarRow, err := s.service.queries.GetGoogleCalendar(ctx, claimed.CalendarID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !calendarRow.SyncEnabled || !Writable(calendarRow.AccessRole) {
		return nil // no longer synced, or no longer writable: Google's version stays
	}
	uid := claimed.ExternalUid
	if strings.HasPrefix(uid, lifeOSUIDPrefix) {
		rekey, err := s.service.queries.GetRekeyByPreviousUID(ctx, store.GetRekeyByPreviousUIDParams{CalendarID: calendarRow.ID, PreviousUid: uid})
		switch {
		case err == nil:
			uid = rekey.GoogleEventID // created in Google, not re-keyed in Calendar yet
		case errors.Is(err, pgx.ErrNoRows):
			if claimed.Deleted {
				return nil // never reached Google
			}
			return s.createSeries(ctx, calendarRow, claimed)
		default:
			return err
		}
	}
	target, found, err := s.target(ctx, calendarRow, uid)
	if err != nil || !found {
		return err
	}
	if claimed.Deleted {
		return s.deleteInGoogle(ctx, target, uid)
	}
	event, _, err := decodePayload(claimed.Payload)
	if err != nil {
		return err
	}
	return s.updateInGoogle(ctx, target, uid, event)
}

// lifeOSUIDPrefix starts the external uid Calendar gives a series it
// creates in a source (the new series of a split) until it is re-keyed.
const lifeOSUIDPrefix = "lifeos:"

// target finds what an edit of the event uid writes to; found is false for
// an event that can only be changed in Google (one occurrence of a rule
// Life OS cannot hold, a lone instance), whose edit stays in Life OS.
func (s *Syncer) target(ctx context.Context, calendarRow store.GoogleCalendar, uid string) (pushTarget, bool, error) {
	events, err := storedEvents(ctx, s.service.queries, calendarRow.ID)
	if err != nil {
		return pushTarget{}, false, err
	}
	m := s.mapperFor(calendarRow, calendarRow.SyncWindowStart, deref(calendarRow.TimeZone), s.now().UTC())
	var master *Event
	for index := range events {
		if events[index].ID == uid {
			master = &events[index]
			break
		}
	}
	if master == nil {
		if strings.Contains(uid, "_") {
			return pushTarget{}, false, nil
		}
		return pushTarget{calendar: calendarRow, event: Event{ID: uid}, deleted: true}, true, nil
	}
	if master.RecurringEventID != "" {
		return pushTarget{}, false, nil
	}
	items, _ := m.derive(events)
	for _, item := range items {
		if item.ExternalUID == uid && !item.Expanded {
			return pushTarget{calendar: calendarRow, event: *master, item: item}, true, nil
		}
	}
	return pushTarget{}, false, nil
}

// updateInGoogle writes what the owner changed of an event (All events and
// its occurrences) to Google.
func (s *Syncer) updateInGoogle(ctx context.Context, t pushTarget, uid string, event payload) error {
	current := event.Event
	if t.deleted {
		if current.Status != "active" {
			return nil
		}
		// Restored in Life OS after Google deleted it: written whole.
		body, err := SeriesBody(pushFields(current), s.owner, nil)
		if err != nil {
			body, _ = SeriesBody(withoutReminders(pushFields(current)), s.owner, nil)
		}
		body["status"] = "confirmed"
		updated, err := s.service.patchEvent(ctx, t.calendar.GoogleCalendarID, uid, "", body)
		if err != nil {
			return s.writeFailed(ctx, t, uid, current, err)
		}
		return s.applyWrite(ctx, t.calendar.ID, []Event{updated}, nil, nil)
	}
	if current.Status == "cancelled" {
		return s.deleteInGoogle(ctx, t, uid)
	}
	fields := pushFields(current)
	present := changedFields(current, t.item, s.owner)
	body, err := EventPatch(fields, present, s.owner, t.KeptRecurrence())
	if errors.Is(err, ErrReminderNotInGoogle) || errors.Is(err, ErrTooManyGoogleReminders) {
		// Reminders Google cannot hold stay Life OS's own.
		delete(present, "reminders")
		body, err = EventPatch(fields, present, s.owner, t.KeptRecurrence())
	}
	if err != nil {
		return err
	}
	var upserts []Event
	if len(body) > 0 {
		updated, err := s.service.patchEvent(ctx, t.calendar.GoogleCalendarID, uid, t.event.ETag, body)
		if err != nil {
			return s.writeFailed(ctx, t, uid, current, err)
		}
		upserts = append(upserts, updated)
	}
	var stale []string
	if fields.Recurrence != nil {
		written, err := s.pushOccurrences(ctx, t, uid, current, event.Exceptions)
		if err != nil {
			return s.writeFailed(ctx, t, uid, current, err)
		}
		upserts = append(upserts, written...)
		if present["recurrence"] && fields.Recurrence.Until != nil {
			stale, err = s.dropInstancesAfter(ctx, t, uid, *fields.Recurrence.Until)
			if err != nil {
				return err
			}
		}
	}
	if len(upserts) == 0 && len(stale) == 0 {
		return nil
	}
	return s.applyWrite(ctx, t.calendar.ID, upserts, nil, stale)
}

// KeptRecurrence are the lines of the event's recurrence a new rule keeps:
// its EXDATEs (the occurrences skipped in Google).
func (t pushTarget) KeptRecurrence() []string { return exdateLines(t.event) }

// pushFields is a Calendar event as Fields for writing to Google: its own
// priority, the source's default written as none.
func pushFields(event calendar.Event) Fields {
	return eventFields(event)
}

func withoutReminders(f Fields) Fields {
	f.Reminders, f.UseDefaultReminders = nil, false
	return f
}

// changedFields names what the owner changed of an event: where Calendar's
// event differs from the item derived from Google's.
func changedFields(current calendar.Event, item sources.Item, owner *time.Location) map[string]bool {
	present := map[string]bool{}
	if current.Title != item.Title {
		present["title"] = true
	}
	if textValue(current.Description) != textValue(item.Description) {
		present["description"] = true
	}
	if textValue(current.Location) != textValue(item.Location) {
		present["location"] = true
	}
	if current.AllDay != item.AllDay {
		present["allDay"] = true
	} else if current.AllDay {
		if deref(current.StartDate) != item.StartDate.Format(time.DateOnly) || deref(current.EndDate) != item.EndDate.Format(time.DateOnly) {
			present["startDate"] = true
		}
	} else if current.StartAt == nil || current.EndAt == nil || !current.StartAt.Truncate(time.Second).Equal(item.StartAt.Truncate(time.Second)) ||
		!current.EndAt.Truncate(time.Second).Equal(item.EndAt.Truncate(time.Second)) {
		present["startAt"] = true
	} else if deref(current.TimeZone) != deref(item.TimeZone) {
		present["timeZone"] = true
	}
	if !sameRule(current.Recurrence, item.Recurrence, current.AllDay, owner) {
		present["recurrence"] = true
	}
	if item.Reminders != nil {
		own := sources.Reminders{UseDefault: current.UseDefaultReminders, Reminders: toReminders(current.Reminders)}
		if current.AllDay {
			own.UseDefault = false
		}
		if !own.Equal(*item.Reminders) {
			present["reminders"] = true
		}
	}
	googlePriority := current.DefaultPriority
	if item.Priority != nil && *item.Priority != "" {
		googlePriority = *item.Priority
	}
	if current.Priority != "" && current.Priority != googlePriority {
		present["priority"] = true
	}
	return present
}

// pushOccurrences writes the owner's changes of a series' occurrences
// (This event) to Google's instances: a skip cancels the instance, a
// change patches what differs from Google's, and an occurrence brought
// back is confirmed again.
func (s *Syncer) pushOccurrences(ctx context.Context, t pushTarget, uid string, current calendar.Event, exceptions []calendar.OccurrenceException) ([]Event, error) {
	have, err := exceptionsByKey(exceptions)
	if err != nil {
		return nil, err
	}
	series, err := lifeOSFieldsOf(payload{Event: current, Exceptions: exceptions})
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for key := range have {
		keys[key] = true
	}
	for key := range t.item.Exceptions {
		keys[key] = true
	}
	var written []Event
	for _, key := range sortedKeys(keys) {
		mine, google := have[key], t.item.Exceptions[key]
		var body map[string]any
		switch {
		case mine.Cancelled && google.Cancelled:
			continue
		case mine.Cancelled:
			body = map[string]any{"status": "cancelled"}
		default:
			natural, ok := series.natural(key)
			if !ok {
				continue
			}
			occurrence := natural
			if !mine.Changes.Empty() {
				occurrence = storedOccurrence{Changes: mine.Changes}.applyTo(natural)
			}
			present := occurrenceChanged(mine.Changes, google.Changes)
			if len(present) == 0 && !google.Cancelled {
				continue
			}
			if body, err = EventPatch(occurrence, present, s.owner, nil); err != nil {
				delete(present, "reminders")
				if body, err = EventPatch(occurrence, present, s.owner, nil); err != nil {
					return nil, err
				}
			}
			if google.Cancelled {
				body["status"] = "confirmed"
			}
			if len(body) == 0 {
				continue
			}
		}
		instance, err := s.service.instanceOf(ctx, t.calendar.GoogleCalendarID, uid, key, current.AllDay)
		if IsGone(err) {
			continue // Google's series has no such occurrence (anymore)
		}
		if err != nil {
			return nil, err
		}
		updated, err := s.service.patchEvent(ctx, t.calendar.GoogleCalendarID, instance.ID, instance.ETag, body)
		if err != nil {
			return nil, err
		}
		written = append(written, updated)
	}
	return written, nil
}

// occurrenceChanged names the fields in which an occurrence's own changes
// in Life OS (mine) differ from Google's (google).
func occurrenceChanged(mine, google sources.Changes) map[string]bool {
	present := map[string]bool{}
	if !sameText(mine.Title, google.Title) {
		present["title"] = true
	}
	if !sameChangeText(mine.Description, google.Description) {
		present["description"] = true
	}
	if !sameChangeText(mine.Location, google.Location) {
		present["location"] = true
	}
	if !sameInstant(mine.StartAt, google.StartAt) || !sameInstant(mine.EndAt, google.EndAt) {
		present["startAt"] = true
	}
	if !sameDay(mine.StartDate, google.StartDate) || !sameDay(mine.EndDate, google.EndDate) {
		present["startDate"] = true
	}
	if !sameText(mine.Priority, google.Priority) {
		present["priority"] = true
	}
	if mine.Reminders != nil && (google.Reminders == nil || !(sources.Reminders{Reminders: *mine.Reminders}).Equal(sources.Reminders{Reminders: *google.Reminders})) ||
		mine.UseDefaultReminders != nil && (google.UseDefaultReminders == nil || *mine.UseDefaultReminders != *google.UseDefaultReminders) {
		present["reminders"] = true
	}
	return present
}

// deleteInGoogle deletes the event in Google (a series with every
// occurrence); Life OS keeps or deletes it as the owner did.
func (s *Syncer) deleteInGoogle(ctx context.Context, t pushTarget, uid string) error {
	if !t.deleted {
		err := s.service.deleteEvent(ctx, t.calendar.GoogleCalendarID, uid, t.event.ETag)
		if err != nil && !IsGone(err) {
			return s.writeFailed(ctx, t, uid, calendar.Event{Title: t.item.Title}, err)
		}
	}
	return s.applyWrite(ctx, t.calendar.ID, nil, []string{uid}, nil)
}

// createSeries creates in Google a series Calendar created in the source
// (the new series of a "this and following" change, under a lifeos: uid):
// its skips and changed occurrences go with it. The next sync gives
// Calendar's event its Google id.
func (s *Syncer) createSeries(ctx context.Context, calendarRow store.GoogleCalendar, claimed store.GooglePush) error {
	event, known, err := decodePayload(claimed.Payload)
	if err != nil || !known || event.Event.Status != "active" {
		return err
	}
	fields, err := lifeOSFieldsOf(event)
	if err != nil {
		return err
	}
	body, err := SeriesBody(fields.Fields, s.owner, fields.exdates)
	if err != nil {
		body, err = SeriesBody(withoutReminders(fields.Fields), s.owner, fields.exdates)
		if err != nil {
			return err
		}
	}
	created, err := s.service.insertEvent(ctx, calendarRow.GoogleCalendarID, body)
	if err != nil {
		if errors.Is(pushError(err), errPushLater) {
			return pushError(err)
		}
		// Google refused the new series: it stays in Life OS only.
		s.logger.Warn("new series of a split not created in Google", "calendar", calendarRow.GoogleCalendarID, "error", err)
		return nil
	}
	upserts := []Event{created}
	for _, key := range sortedKeys(fields.occurrences) {
		occurrence := fields.occurrences[key]
		instanceBody, err := InstanceBody(occurrence.Fields, s.owner)
		if err != nil {
			instanceBody, _ = InstanceBody(withoutReminders(occurrence.Fields), s.owner)
		}
		instance, err := s.service.instanceOf(ctx, calendarRow.GoogleCalendarID, created.ID, key, fields.AllDay)
		if err == nil {
			instance, err = s.service.patchEvent(ctx, calendarRow.GoogleCalendarID, instance.ID, "", instanceBody)
		}
		if err != nil {
			s.logger.Warn("occurrence change not carried to the new Google series", "calendar", calendarRow.GoogleCalendarID, "occurrence", key, "error", err)
			continue
		}
		upserts = append(upserts, instance)
	}
	if err := s.service.queries.AddRekey(ctx, store.AddRekeyParams{CalendarID: calendarRow.ID, GoogleEventID: created.ID, PreviousUid: claimed.ExternalUid}); err != nil {
		return err
	}
	return s.applyWrite(ctx, calendarRow.ID, upserts, nil, nil)
}

// dropInstancesAfter deletes in Google (best effort) the series' stored
// changed or skipped instances after its new end (until), which the series
// no longer has, and returns their ids to forget.
func (s *Syncer) dropInstancesAfter(ctx context.Context, t pushTarget, uid string, until time.Time) ([]string, error) {
	rows, err := s.service.queries.ListStoredGoogleInstances(ctx, store.ListStoredGoogleInstancesParams{CalendarID: t.calendar.ID, RecurringEventID: &uid})
	if err != nil {
		return nil, err
	}
	var stale []string
	for _, row := range rows {
		var instance Event
		if err := json.Unmarshal(row.Data, &instance); err != nil {
			return nil, fmt.Errorf("decode stored Google event: %w", err)
		}
		key, ok := instanceRecurrenceID(instance)
		if !ok || !occursAfter(key, until, t.event.Start != nil && t.event.Start.Date != "", s.owner) {
			continue
		}
		stale = append(stale, instance.ID)
		if instance.Cancelled() {
			continue
		}
		if err := s.service.deleteEvent(ctx, t.calendar.GoogleCalendarID, instance.ID, ""); err != nil && !IsGone(err) {
			s.logger.Warn("changed occurrence after the end of a Google series not deleted", "calendar", t.calendar.GoogleCalendarID, "event", instance.ID, "error", err)
		}
	}
	return stale, nil
}

// occursAfter reports whether the occurrence with recurrence id key starts
// after until (an all-day series compares dates in the owner's timezone).
func occursAfter(key string, until time.Time, allDay bool, owner *time.Location) bool {
	if allDay {
		if owner == nil {
			owner = time.UTC
		}
		return key > until.In(owner).Format(time.DateOnly)
	}
	start, err := time.Parse(time.RFC3339, key)
	return err == nil && start.After(until)
}

// pushError sorts a failed write: Google or the connection not usable now
// is retried (errPushLater); anything else is Google refusing it.
func pushError(err error) error {
	var unavailable *UnavailableError
	if errors.As(err, &unavailable) || errors.Is(err, ErrExpired) || errors.Is(err, ErrNotConnected) || errors.Is(err, ErrNotConfigured) || IsUnauthorized(err) {
		return fmt.Errorf("%w: %w", errPushLater, err)
	}
	return err
}

// writeFailed turns Google's refusal of a write to eventID (the target or
// one of its instances) into what happens next. Not reachable now: retried.
// Otherwise Google's version is kept: a concurrent change (412) or a
// refusal is read back from Google, an event deleted meanwhile leaves Life
// OS, the next sync gives Calendar Google's version again, and the
// conflict is recorded.
func (s *Syncer) writeFailed(ctx context.Context, t pushTarget, uid string, current calendar.Event, err error) error {
	if errors.Is(pushError(err), errPushLater) {
		return pushError(err)
	}
	var upserts []Event
	var forget []string
	latest, getErr := s.service.getEvent(ctx, t.calendar.GoogleCalendarID, uid)
	switch {
	case getErr == nil && !latest.Cancelled():
		upserts = []Event{latest}
	case getErr == nil || IsGone(getErr):
		forget = []string{uid}
	default:
		s.logger.Warn("Google's version of a refused edit not read; the next full sync brings it", "calendar", t.calendar.GoogleCalendarID, "error", getErr)
	}
	if err := s.applyWrite(ctx, t.calendar.ID, upserts, forget, nil); err != nil {
		return err
	}
	if err := s.service.queries.ForgetSentEvent(ctx, store.ForgetSentEventParams{CalendarID: t.calendar.ID, ExternalUid: uid}); err != nil {
		return err
	}
	if current.ID != "" {
		s.service.recordConflict(ctx, current, t.calendar.Summary, conflictEdit, s.now())
	}
	s.logger.Warn("edit made in Life OS not written to Google: Google's version is kept", "calendar", t.calendar.GoogleCalendarID, "error", err)
	return nil
}

// applyWrite stores what Google answered to this service's writes in a
// synced calendar (upserts; forget deletes events with their instances,
// forgetInstances single instances) and makes the calendar due, so
// Calendar gets Google's version right away.
func (s *Syncer) applyWrite(ctx context.Context, calendarID pgtype.UUID, upserts []Event, forget, forgetInstances []string) error {
	ctx = context.WithoutCancel(ctx)
	err := s.service.inTx(ctx, func(q *store.Queries) error {
		current, err := q.LockGoogleCalendar(ctx, calendarID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // dropped meanwhile
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
		return q.MarkGoogleCalendarDue(ctx, store.MarkGoogleCalendarDueParams{ID: current.ID, Now: timestamptz(s.now())})
	})
	if err == nil {
		s.Wake()
	}
	return err
}

// withToken calls Google with a usable access token, refreshing it once if
// Google no longer accepts it.
func (s *Service) withToken(ctx context.Context, call func(token string) error) error {
	for attempt := 0; ; attempt++ {
		token, err := s.AccessToken(ctx)
		if err != nil {
			return err
		}
		err = call(token)
		if attempt == 0 && IsUnauthorized(err) {
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
