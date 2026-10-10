package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/singleflight"

	"github.com/GAMch1k/life-os/pkg/activity"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/sources"
)

// Sync timing.
const (
	// DefaultSyncInterval is how often each calendar is synced
	// (CALENDAR_GOOGLE_SYNC_SECONDS).
	DefaultSyncInterval = 2 * time.Minute
	// SyncLookback is how far back a full listing reaches; there is no
	// upper bound, so every future event is listed.
	SyncLookback = 365 * 24 * time.Hour
	// maxSyncBackoff caps the pause after failed syncs.
	maxSyncBackoff = time.Hour
	// rederiveAfter: a sync without changes still re-derives the events
	// this often, so occurrences expanded up to ExpansionHorizon ahead
	// follow the calendar's time window as days pass.
	rederiveAfter = 12 * time.Hour
	// maxEventPages bounds one listing (100 events a page).
	maxEventPages = 250
	// syncLease bounds a sync (the listing plus the transaction); a crashed
	// sync's lease expires after it. A sync that outlives its lease still
	// cannot write over a newer one: its transaction checks that the sync
	// token is still the one it started from.
	syncLease = 5 * time.Minute
)

// SyncActor is the system actor Google syncs are attributed to.
var SyncActor = activity.System("google-calendar")

// Activity event types of synced calendars, shared with feeds (subject
// calendar_source).
const (
	EventSourceRefreshed     = "calendar.source.refreshed"
	EventSourceRefreshFailed = "calendar.source.refresh_failed"
	EventSourceRemoved       = "calendar.source.removed"
)

// errNotSaved: a listing was read but could not be written.
var errNotSaved = errors.New("the events read from Google could not be saved in Calendar")

var (
	ErrSyncDisabled   = errors.New("this calendar does not sync; turn syncing on for it first")
	ErrSyncInProgress = errors.New("this calendar is syncing right now; try again shortly")
	// ErrSyncSuperseded: the calendar was switched off or resynced while it
	// was being synced; the result was discarded.
	ErrSyncSuperseded = errors.New("the calendar's sync changed meanwhile; it syncs again shortly")
)

// SyncTrigger says why a sync runs.
type SyncTrigger struct {
	// Manual: requested through the API, by Trace.Actor.
	Manual bool
	// Full lists every event again instead of the changes since the last
	// sync.
	Full  bool
	Trace activity.Trace
}

// Syncer imports the events of the calendars chosen to sync. Each calendar
// is synced every interval while the connection is usable: the first sync
// lists its events from SyncLookback ago on, later ones only what changed
// since (Google's sync token). One sync per calendar runs at a time: calls
// for a calendar already syncing in this process share its result, and a
// lease in the database keeps other processes (and a restarted one) out.
type Syncer struct {
	service  *Service
	owner    *time.Location
	interval time.Duration
	logger   *slog.Logger
	now      func() time.Time
	group    singleflight.Group
	wake     chan struct{}
}

// NewSyncer syncs through service's connection. owner is the owner's
// timezone; interval defaults to DefaultSyncInterval.
func NewSyncer(service *Service, owner *time.Location, interval time.Duration, logger *slog.Logger) *Syncer {
	if interval <= 0 {
		interval = DefaultSyncInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	if owner == nil {
		owner = time.UTC
	}
	syncer := &Syncer{service: service, owner: owner, interval: interval, logger: logger, now: service.now, wake: make(chan struct{}, 1)}
	service.syncer = syncer
	return syncer
}

// WithClock replaces the clock (tests).
func (s *Syncer) WithClock(now func() time.Time) *Syncer {
	s.now = now
	return s
}

// Wake makes the syncer look for due calendars now. It never blocks.
func (s *Syncer) Wake() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run syncs due calendars until ctx ends.
func (s *Syncer) Run(ctx context.Context) {
	s.logger.Info("Google Calendar sync scheduled", "every", s.interval.String())
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		s.SyncDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

// SyncDue syncs, one after another, the calendars whose sync is due. Nothing
// is asked of Google while it is not configured or the connection expired
// (the owner reconnects first).
func (s *Syncer) SyncDue(ctx context.Context) {
	if !s.service.Configured() {
		return
	}
	connection, found, err := s.service.Connection(ctx)
	if err != nil || !found || connection.Status == StatusExpired {
		if err != nil && ctx.Err() == nil {
			s.logger.Warn("Google connection not read for syncing", "error", err)
		}
		return
	}
	// A calendar synced at the last tick is due an interval after that sync
	// started, a moment after this tick: dueSlack counts it due now, so
	// each calendar syncs every interval rather than every other tick.
	due, err := s.service.queries.ListDueGoogleCalendars(ctx, store.ListDueGoogleCalendarsParams{ConnectionID: connection.ID, Now: timestamptz(s.now().UTC().Add(dueSlack(s.interval)))})
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("Google calendars due for sync not listed", "error", err)
		}
		return
	}
	for _, id := range due {
		if ctx.Err() != nil {
			return
		}
		calendar, err := s.Sync(ctx, googleuuid.UUID(id.Bytes), SyncTrigger{})
		switch {
		case errors.Is(err, ErrSyncInProgress), errors.Is(err, ErrSyncDisabled), errors.Is(err, ErrSyncSuperseded),
			errors.Is(err, ErrCalendarNotFound), errors.Is(err, ErrNotConnected):
			s.logger.Info("Google calendar sync skipped", "calendar", googleuuid.UUID(id.Bytes), "reason", err.Error())
		case err != nil:
			s.logger.Warn("Google calendar sync failed", "calendar", googleuuid.UUID(id.Bytes), "error", err)
		case calendar.LastSyncError != nil:
			s.logger.Warn("Google calendar sync failed", "calendar", googleuuid.UUID(id.Bytes), "error", *calendar.LastSyncError, "failures", calendar.SyncFailures)
		}
	}
}

// Sync syncs one calendar now and returns it with its sync state. A failure
// at Google is not an error: it is recorded on the calendar (and retried
// with backoff) while the events from the last good sync stay. The sync
// outlives a cancelled caller.
func (s *Syncer) Sync(ctx context.Context, id googleuuid.UUID, trigger SyncTrigger) (store.GoogleCalendar, error) {
	// A full resync does not share a running incremental sync: it then
	// finds the calendar syncing (ErrSyncInProgress) and, having dropped
	// the sync token, makes that sync's result give way to a full listing.
	key := id.String()
	if trigger.Full {
		key += "/full"
	}
	result, err, _ := s.group.Do(key, func() (any, error) {
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), syncLease)
		defer cancel()
		return s.sync(work, id, trigger)
	})
	if err != nil {
		return store.GoogleCalendar{}, err
	}
	return result.(store.GoogleCalendar), nil
}

func (s *Syncer) sync(ctx context.Context, id googleuuid.UUID, trigger SyncTrigger) (store.GoogleCalendar, error) {
	now := s.now().UTC()
	calendarID := pgtype.UUID{Bytes: id, Valid: true}
	if trigger.Full {
		if _, err := s.service.queries.ResyncGoogleCalendar(ctx, store.ResyncGoogleCalendarParams{ID: calendarID, Now: timestamptz(now)}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return store.GoogleCalendar{}, err
		}
	}
	claimed, err := s.service.queries.ClaimGoogleCalendarSync(ctx, store.ClaimGoogleCalendarSyncParams{
		ID: calendarID, Now: timestamptz(now), LeaseUntil: timestamptz(now.Add(syncLease)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		current, err := s.service.queries.GetGoogleCalendar(ctx, calendarID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return store.GoogleCalendar{}, ErrCalendarNotFound
		case err != nil:
			return store.GoogleCalendar{}, err
		case !current.SyncEnabled:
			return store.GoogleCalendar{}, ErrSyncDisabled
		}
		return store.GoogleCalendar{}, ErrSyncInProgress
	}
	if err != nil {
		return store.GoogleCalendar{}, err
	}
	source, err := s.ensureSource(ctx, s.service.queries, claimed, nil)
	if err != nil {
		s.release(claimed.ID, nil)
		return store.GoogleCalendar{}, err
	}
	found, err := s.list(ctx, claimed, now)
	if errors.Is(err, ErrNotConnected) || errors.Is(err, ErrNotConfigured) {
		s.release(claimed.ID, nil)
		return store.GoogleCalendar{}, err
	}
	if err != nil {
		return s.fail(ctx, claimed, source, err, trigger, now)
	}
	updated, counts, err := s.store(ctx, claimed, found, now)
	switch {
	case errors.Is(err, ErrSyncSuperseded), errors.Is(err, ErrCalendarNotFound):
		due := now
		s.release(claimed.ID, &due)
		return store.GoogleCalendar{}, err
	case err != nil:
		// The listing could not be saved (the sync token stays, so the
		// next attempt lists the same changes): a failure like Google's,
		// with backoff, so a lasting fault neither hammers Google nor
		// goes unseen.
		s.logger.Error("Google calendar sync not saved", "calendar", id, "error", err)
		return s.fail(ctx, claimed, source, errNotSaved, trigger, now)
	}
	// The first sync, a sync that changed events, and the first good one
	// after failures are recorded; a sync that changed nothing is not.
	if counts != nil && (counts.Changed() || !claimed.LastSyncedAt.Valid) || claimed.SyncFailures > 0 {
		recorded := sources.Counts{}
		if counts != nil {
			recorded = *counts
		}
		s.recordSynced(ctx, source, recorded, found.full, trigger, now)
	}
	return updated, nil
}

func (s *Syncer) release(id pgtype.UUID, due *time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params := store.ReleaseGoogleCalendarSyncParams{ID: id}
	if due != nil {
		params.DueAt = timestamptz(*due)
	}
	if err := s.service.queries.ReleaseGoogleCalendarSync(ctx, params); err != nil {
		s.logger.Warn("Google calendar sync lease not released", "calendar", googleuuid.UUID(id.Bytes), "error", err)
	}
}

// listing is what one sync read from Google: every page of a full listing
// (from windowStart on) or of the changes since the calendar's sync token.
type listing struct {
	full        bool
	windowStart time.Time
	items       []Event
	token       string
	timeZone    string
	// defaults are the calendar's default reminders; nil when Google did
	// not send them.
	defaults []Reminder
}

// list reads the calendar's changes, or everything when it has no sync
// token or Google no longer accepts it (410: listed in full again).
func (s *Syncer) list(ctx context.Context, calendar store.GoogleCalendar, now time.Time) (listing, error) {
	token := ""
	if calendar.SyncToken != nil {
		token = *calendar.SyncToken
	}
	found, err := s.fetch(ctx, calendar.GoogleCalendarID, token, now)
	if token != "" && IsSyncTokenGone(err) {
		s.logger.Info("Google sync token expired; listing the calendar in full", "calendar", googleuuid.UUID(calendar.ID.Bytes))
		return s.fetch(ctx, calendar.GoogleCalendarID, "", now)
	}
	return found, err
}

func (s *Syncer) fetch(ctx context.Context, calendarID, token string, now time.Time) (listing, error) {
	out := listing{full: token == ""}
	query := EventsQuery{SyncToken: token}
	if out.full {
		out.windowStart = now.Add(-SyncLookback)
		query.TimeMin = out.windowStart
	}
	for page := 0; page < maxEventPages; page++ {
		result, err := s.page(ctx, calendarID, query)
		if err != nil {
			return listing{}, err
		}
		if page == 0 {
			out.timeZone, out.defaults = result.TimeZone, result.DefaultReminders
		}
		out.items = append(out.items, result.Items...)
		if result.NextPageToken == "" {
			if result.NextSyncToken == "" {
				return listing{}, &UnavailableError{Err: errors.New("Google returned no sync token")}
			}
			out.token = result.NextSyncToken
			return out, nil
		}
		query.PageToken = result.NextPageToken
	}
	return listing{}, &UnavailableError{Err: fmt.Errorf("the calendar has more than %d pages of events", maxEventPages)}
}

// page reads one page with a usable access token, refreshing it once if
// Google no longer accepts it.
func (s *Syncer) page(ctx context.Context, calendarID string, query EventsQuery) (EventsPage, error) {
	for attempt := 0; ; attempt++ {
		token, err := s.service.AccessToken(ctx)
		if err != nil {
			return EventsPage{}, err
		}
		page, err := s.service.api.Events(ctx, token, calendarID, query)
		if attempt == 0 && isUnauthorized(err) {
			s.service.forget()
			continue
		}
		return page, err
	}
}

// store writes a listing in one transaction holding the calendar: the
// stored Google events, the Calendar events derived from them (when
// something changed, after a full listing, or rederiveAfter since the last
// derivation), and the outcome. A calendar switched off, or resynced, since
// the listing started discards it (ErrSyncSuperseded).
func (s *Syncer) store(ctx context.Context, claimed store.GoogleCalendar, found listing, now time.Time) (store.GoogleCalendar, *sources.Counts, error) {
	tx, err := s.service.db.Begin(ctx)
	if err != nil {
		return store.GoogleCalendar{}, nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.service.queries.WithTx(tx)
	current, err := q.LockGoogleCalendar(ctx, claimed.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.GoogleCalendar{}, nil, ErrCalendarNotFound
	}
	if err != nil {
		return store.GoogleCalendar{}, nil, err
	}
	if !current.SyncEnabled {
		// Switched off meanwhile: a source this sync added goes again.
		if source, err := q.GetGoogleSource(ctx, current.ID); err == nil {
			if err := q.DeleteGoogleSource(ctx, source.ID); err != nil {
				return store.GoogleCalendar{}, nil, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return store.GoogleCalendar{}, nil, err
		}
		return store.GoogleCalendar{}, nil, ErrSyncSuperseded
	}
	if deref(current.SyncToken) != deref(claimed.SyncToken) {
		return store.GoogleCalendar{}, nil, ErrSyncSuperseded
	}
	source, err := s.ensureSource(ctx, q, current, &found)
	if err != nil {
		return store.GoogleCalendar{}, nil, err
	}
	changed := found.full
	if found.full {
		if err := q.DeleteGoogleEvents(ctx, current.ID); err != nil {
			return store.GoogleCalendar{}, nil, err
		}
	}
	keep, forget := partition(found.items)
	if len(keep) > 0 {
		raw, err := json.Marshal(keep)
		if err != nil {
			return store.GoogleCalendar{}, nil, err
		}
		if err := q.UpsertGoogleEvents(ctx, store.UpsertGoogleEventsParams{CalendarID: current.ID, Items: raw}); err != nil {
			return store.GoogleCalendar{}, nil, fmt.Errorf("store Google events: %w", err)
		}
		changed = true
	}
	if len(forget) > 0 && !found.full {
		if err := q.DeleteGoogleEventsByID(ctx, store.DeleteGoogleEventsByIDParams{CalendarID: current.ID, EventIds: forget}); err != nil {
			return store.GoogleCalendar{}, nil, err
		}
		changed = true
	}
	windowStart := current.SyncWindowStart
	if found.full {
		windowStart = timestamptz(found.windowStart)
	}
	zoneName := found.timeZone
	if zoneName == "" && current.TimeZone != nil {
		zoneName = *current.TimeZone
	}
	params := store.FinishGoogleCalendarSyncParams{
		ID: current.ID, SyncToken: &found.token, SyncWindowStart: windowStart, NextSyncAt: timestamptz(now.Add(s.interval)), Now: timestamptz(now),
	}
	if zoneName != "" {
		params.TimeZone = &zoneName
	}
	var counts *sources.Counts
	if changed || !current.DerivedAt.Valid || now.Sub(current.DerivedAt.Time) >= rederiveAfter {
		rows, err := q.ListGoogleEvents(ctx, current.ID)
		if err != nil {
			return store.GoogleCalendar{}, nil, err
		}
		events := make([]Event, 0, len(rows))
		for _, row := range rows {
			var event Event
			if err := json.Unmarshal(row.Data, &event); err != nil {
				return store.GoogleCalendar{}, nil, fmt.Errorf("decode stored Google event: %w", err)
			}
			events = append(events, event)
		}
		zone := loadZone(zoneName)
		if zone == nil {
			zone = s.owner
		}
		// Derived from the full listing's window start, but never more than
		// SyncLookback back: incremental syncs keep the window start for
		// months, and expanded occurrences would pile up. Older history
		// stays as it is.
		derivedFrom := windowStart.Time
		if floor := now.Add(-SyncLookback); derivedFrom.Before(floor) {
			derivedFrom = floor
		}
		m := mapper{owner: s.owner, zone: zone, now: now, windowStart: derivedFrom}
		items, skipped := m.derive(events)
		result, err := sources.Reconcile(ctx, q, source, items, sources.SyncRules(derivedFrom, s.owner), now)
		if err != nil {
			return store.GoogleCalendar{}, nil, err
		}
		result.FeedEvents, result.Skipped = len(events), skipped
		counts = &result
		if params.LastSyncCounts, err = json.Marshal(result); err != nil {
			return store.GoogleCalendar{}, nil, err
		}
		params.DerivedAt = timestamptz(now)
	}
	updated, err := q.FinishGoogleCalendarSync(ctx, params)
	if err != nil {
		return store.GoogleCalendar{}, nil, err
	}
	return updated, counts, tx.Commit(ctx)
}

// storedEvent is one row for UpsertGoogleEvents.
type storedEvent struct {
	ID               string `json:"id"`
	RecurringEventID string `json:"recurringEventId"`
	Data             Event  `json:"data"`
}

// partition splits a listing into the events to store and the ids to
// forget: a deleted event (with its instances, if it recurred) is
// forgotten; a cancelled instance is kept, as the skip of its occurrence.
func partition(items []Event) ([]storedEvent, []string) {
	var keep []storedEvent
	var forget []string
	for _, event := range items {
		if event.ID == "" {
			continue
		}
		if event.Cancelled() && event.RecurringEventID == "" {
			forget = append(forget, event.ID)
			continue
		}
		keep = append(keep, storedEvent{ID: event.ID, RecurringEventID: event.RecurringEventID, Data: event})
	}
	return keep, forget
}

// ensureSource returns the calendar's source, adding it the first time and
// following the calendar's name and (when a listing carries them) default
// reminders.
func (s *Syncer) ensureSource(ctx context.Context, q *store.Queries, calendar store.GoogleCalendar, found *listing) (store.CalendarSource, error) {
	var defaults []byte
	haveDefaults := found != nil && found.defaults != nil
	if haveDefaults {
		if reminders := relativeReminders(found.defaults); len(reminders) > 0 {
			encoded, err := json.Marshal(reminders)
			if err != nil {
				return store.CalendarSource{}, err
			}
			defaults = encoded
		}
	}
	source, err := q.GetGoogleSource(ctx, calendar.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		created, err := q.CreateGoogleSource(ctx, store.CreateGoogleSourceParams{
			Name: calendar.Summary, Url: s.service.eventsURL(calendar.GoogleCalendarID), DefaultReminders: defaults, GoogleCalendarID: calendar.ID,
		})
		if err != nil {
			return store.CalendarSource{}, fmt.Errorf("add the calendar's source: %w", err)
		}
		return created, nil
	}
	if err != nil {
		return store.CalendarSource{}, err
	}
	if !haveDefaults {
		defaults = source.DefaultReminders
	}
	if source.Name == calendar.Summary && sameJSON(source.DefaultReminders, defaults) {
		return source, nil
	}
	return q.UpdateGoogleSource(ctx, store.UpdateGoogleSourceParams{ID: source.ID, Name: calendar.Summary, DefaultReminders: defaults})
}

func sameJSON(a, b []byte) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	l, _ := json.Marshal(left)
	r, _ := json.Marshal(right)
	return string(l) == string(r)
}

// fail records a failed sync: the error, one more failure, and the next
// attempt after a backoff. Events stay as they are. Activity records the
// start of a failure streak, and a failure that differs from the last one;
// an expired connection is recorded once by the connection itself.
func (s *Syncer) fail(ctx context.Context, calendar store.GoogleCalendar, source store.CalendarSource, cause error, trigger SyncTrigger, now time.Time) (store.GoogleCalendar, error) {
	message := syncErrorMessage(cause)
	failures := int(calendar.SyncFailures) + 1
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	updated, err := s.service.queries.FailGoogleCalendarSync(ctx, store.FailGoogleCalendarSyncParams{
		ID: calendar.ID, LastSyncError: &message, NextSyncAt: timestamptz(now.Add(SyncBackoff(s.interval, failures))),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Switched off (or disconnected) meanwhile: nothing to record, and
		// a source this sync added goes again.
		s.release(calendar.ID, nil)
		s.dropSourceIfOff(ctx, calendar.ID)
		return store.GoogleCalendar{}, ErrSyncSuperseded
	}
	if err != nil {
		s.release(calendar.ID, nil)
		return store.GoogleCalendar{}, err
	}
	if !errors.Is(cause, ErrExpired) && (calendar.SyncFailures == 0 || deref(calendar.LastSyncError) != message) {
		s.recordFailed(ctx, updated, source, message, trigger, now)
	}
	return updated, nil
}

// dueSlack is how early SyncDue takes a calendar as due: a tenth of the
// interval, at most 30 seconds.
func dueSlack(interval time.Duration) time.Duration {
	return min(interval/10, 30*time.Second)
}

// dropSourceIfOff deletes the source of a calendar that no longer syncs.
func (s *Syncer) dropSourceIfOff(ctx context.Context, id pgtype.UUID) {
	err := s.service.inTx(ctx, func(q *store.Queries) error {
		current, err := q.LockGoogleCalendar(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil || current.SyncEnabled {
			return err
		}
		source, err := q.GetGoogleSource(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return q.DeleteGoogleSource(ctx, source.ID)
	})
	if err != nil {
		s.logger.Warn("source of a calendar no longer syncing not removed", "calendar", googleuuid.UUID(id.Bytes), "error", err)
	}
}

// SyncBackoff is the pause after failures failed syncs in a row: the
// interval, doubling up to an hour.
func SyncBackoff(interval time.Duration, failures int) time.Duration {
	delay := interval
	for range max(failures-1, 0) {
		delay *= 2
		if delay >= maxSyncBackoff {
			return maxSyncBackoff
		}
	}
	return min(delay, maxSyncBackoff)
}

// syncErrorMessage says why a sync failed, in words for the owner. Google's
// own messages never carry tokens.
func syncErrorMessage(err error) string {
	var apiErr *APIError
	var oauth *OAuthError
	var unavailable *UnavailableError
	switch {
	case errors.Is(err, ErrExpired):
		return "Google access expired or was revoked; reconnect Google in Calendar"
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound:
		return "Google no longer has this calendar, or this account cannot see it; refresh the calendar list"
	case errors.As(err, &apiErr), errors.As(err, &oauth), errors.As(err, &unavailable):
		return err.Error()
	case errors.Is(err, errNotSaved):
		return errNotSaved.Error()
	case errors.Is(err, ErrUnreadableToken):
		return "the stored Google token cannot be decrypted with LIFE_OS_GOOGLE_TOKEN_KEY (was the key changed?); reconnect Google in Calendar"
	}
	return "the sync failed"
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// eventsURL is a calendar's events endpoint at Google.
func (s *Service) eventsURL(calendarID string) string {
	return s.calendarAPI + "/calendars/" + url.PathEscape(calendarID) + "/events"
}

// recordSynced records a sync that changed events (or the first one, or
// the first after failures) as one calendar.source.refreshed, attributed to
// SyncActor; a sync that changed nothing records nothing.
func (s *Syncer) recordSynced(ctx context.Context, source store.CalendarSource, counts sources.Counts, full bool, trigger SyncTrigger, now time.Time) {
	data := s.sourceData(source, trigger)
	data["outcome"], data["counts"], data["full"] = "imported", counts, full
	summary := fmt.Sprintf("Synced %s from Google: %d new, %d changed, %d removed, %d restored", source.Name, counts.Inserted, counts.Updated, counts.Removed, counts.Restored)
	s.emit(ctx, EventSourceRefreshed, source, activity.Succeeded, summary, data, trigger, now)
}

func (s *Syncer) recordFailed(ctx context.Context, calendar store.GoogleCalendar, source store.CalendarSource, message string, trigger SyncTrigger, now time.Time) {
	data := s.sourceData(source, trigger)
	data["outcome"], data["error"], data["failures"] = "failed", message, calendar.SyncFailures
	summary := fmt.Sprintf("Syncing %s from Google failed: %s", source.Name, message)
	s.emit(ctx, EventSourceRefreshFailed, source, activity.Failed, summary, data, trigger, now)
}

func (s *Syncer) sourceData(source store.CalendarSource, trigger SyncTrigger) map[string]any {
	data := map[string]any{"sourceId": googleuuid.UUID(source.ID.Bytes).String(), "sourceName": source.Name, "sourceKind": "google", "trigger": "scheduled"}
	if trigger.Manual {
		data["trigger"] = "manual"
		if !trigger.Trace.Actor.IsZero() {
			data["requestedBy"] = trigger.Trace.Actor
		}
	}
	return data
}

func (s *Syncer) emit(ctx context.Context, eventType string, source store.CalendarSource, outcome string, summary string, data map[string]any, trigger SyncTrigger, now time.Time) {
	id := googleuuid.UUID(source.ID.Bytes).String()
	s.service.activity.Emit(ctx, activity.Event{
		ID:            activity.EventID(eventType, id, now.Format(time.RFC3339Nano)),
		Type:          eventType,
		Subject:       activity.Subject{Type: "calendar_source", ID: id},
		CorrelationID: trigger.Trace.CorrelationID,
		CausationID:   trigger.Trace.CausationID,
		Actor:         SyncActor,
		Outcome:       outcome,
		Summary:       activity.Preview(summary, 500),
		Data:          data,
		OccurredAt:    now,
	})
}
