package google

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/singleflight"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/calendar"
	store "github.com/GAMch1k/life-os/services/google-calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/sources"
)

// Sync timing.
const (
	// DefaultSyncInterval is how often each calendar is synced
	// (GOOGLE_CALENDAR_SYNC_SECONDS).
	DefaultSyncInterval = 2 * time.Minute
	// DefaultFeedInterval is how often Calendar's change feed is read
	// (GOOGLE_CALENDAR_FEED_SECONDS), so an edit made in Life OS reaches
	// Google within seconds.
	DefaultFeedInterval = 15 * time.Second
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

// SyncActor is the system actor this service's work is attributed to.
var SyncActor = activity.System(calendar.Integration)

// Activity event types of synced calendars, shared with feeds (subject
// calendar_source).
const (
	EventSourceRefreshed     = "calendar.source.refreshed"
	EventSourceRefreshFailed = "calendar.source.refresh_failed"
)

// errNotSaved: a listing was read but could not be written.
var errNotSaved = errors.New("the events read from Google could not be saved")

// errUnhandled: Calendar refused the sync because an edit made in Life OS
// is still to be written to Google (unhandled_changes).
var errUnhandled = errors.New("an edit made in Life OS is still to be written to Google")

var (
	ErrSyncDisabled   = errors.New("this calendar does not sync; turn syncing on for it first")
	ErrSyncInProgress = errors.New("this calendar is syncing right now; try again shortly")
	// ErrSyncSuperseded: the calendar was switched off or resynced while it
	// was being synced; the result was discarded.
	ErrSyncSuperseded = errors.New("the calendar's sync changed meanwhile; it syncs again shortly")
	// ErrSyncDeferred: edits made in Life OS to the calendar's events are
	// written to Google first (or Calendar's change feed is not read yet),
	// so a sync cannot overwrite them; it syncs right after.
	ErrSyncDeferred = errors.New("edits made in Life OS are being written to Google first; the calendar syncs right after")
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

// Syncer imports the events of the calendars chosen to sync into Calendar,
// exports Life OS events, and writes the owner's edits to Google. Each
// calendar is synced every interval while the connection is usable: the
// first sync lists its events from SyncLookback ago on, later ones only
// what changed since (Google's sync token). One sync per calendar runs at a
// time: calls for a calendar already syncing in this process share its
// result, and a lease in the database keeps other processes (and a
// restarted one) out.
type Syncer struct {
	service  *Service
	owner    *time.Location
	interval time.Duration
	feed     time.Duration
	logger   *slog.Logger
	now      func() time.Time
	group    singleflight.Group
	wake     chan struct{}
}

// NewSyncer syncs through service's connection. owner is the owner's
// timezone; interval defaults to DefaultSyncInterval, feed (how often
// Calendar's change feed is read) to DefaultFeedInterval.
func NewSyncer(service *Service, owner *time.Location, interval, feed time.Duration, logger *slog.Logger) *Syncer {
	if interval <= 0 {
		interval = DefaultSyncInterval
	}
	if feed <= 0 {
		feed = DefaultFeedInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	if owner == nil {
		owner = time.UTC
	}
	syncer := &Syncer{service: service, owner: owner, interval: interval, feed: feed, logger: logger, now: service.now, wake: make(chan struct{}, 1)}
	service.syncer = syncer
	return syncer
}

// WithClock replaces the clock (tests).
func (s *Syncer) WithClock(now func() time.Time) *Syncer {
	s.now = now
	return s
}

// Wake makes the syncer run now. It never blocks.
func (s *Syncer) Wake() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run works until ctx ends: every feed interval (and when woken) it reads
// Calendar's change feed, writes the owner's edits and the Life OS events
// to Google, removes the sources of calendars that stopped syncing, and
// syncs the calendars that are due.
func (s *Syncer) Run(ctx context.Context) {
	s.logger.Info("Google Calendar sync scheduled", "every", s.interval.String(), "changesEvery", s.feed.String())
	ticker := time.NewTicker(s.feed)
	defer ticker.Stop()
	for {
		s.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

// RunOnce is one round of Run.
func (s *Syncer) RunOnce(ctx context.Context) {
	s.service.RemoveSources(ctx)
	if !s.usable(ctx) {
		return
	}
	s.followChanges(ctx)
	s.ExportDue(ctx)
	s.SyncDue(ctx)
}

// usable reports whether the service can work for a connection: Google is
// configured and connected.
func (s *Syncer) usable(ctx context.Context) bool {
	if !s.service.Configured() {
		return false
	}
	_, found, err := s.service.Connection(ctx)
	if err != nil && ctx.Err() == nil {
		s.logger.Warn("Google connection not read", "error", err)
	}
	return err == nil && found
}

// followChanges reads Calendar's change feed and writes the owner's edits
// of Google events to Google.
func (s *Syncer) followChanges(ctx context.Context) {
	if err := s.DrainFeed(ctx); err != nil && ctx.Err() == nil {
		s.logger.Warn("Calendar's change feed not read", "error", err)
	}
	s.PushDue(ctx)
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
		calendarRow, err := s.Sync(ctx, googleuuid.UUID(id.Bytes), SyncTrigger{})
		if errors.Is(err, errUnhandled) {
			// An edit arrived between reading the feed and the sync: it is
			// written to Google first, then the sync is tried again.
			s.followChanges(ctx)
			calendarRow, err = s.Sync(ctx, googleuuid.UUID(id.Bytes), SyncTrigger{})
		}
		switch {
		case errors.Is(err, ErrSyncInProgress), errors.Is(err, ErrSyncDisabled), errors.Is(err, ErrSyncSuperseded),
			errors.Is(err, ErrCalendarNotFound), errors.Is(err, ErrNotConnected), errors.Is(err, ErrSyncDeferred), errors.Is(err, errUnhandled):
			s.logger.Info("Google calendar sync skipped", "calendar", googleuuid.UUID(id.Bytes), "reason", logCode(err))
		case err != nil:
			s.logger.Warn("Google calendar sync failed", "calendar", googleuuid.UUID(id.Bytes), "error", logCode(err))
		case calendarRow.LastSyncError != nil:
			s.logger.Warn("Google calendar sync failed", "calendar", googleuuid.UUID(id.Bytes), "failures", calendarRow.SyncFailures)
		}
	}
}

// Sync syncs one calendar now and returns it with its sync state. A failure
// at Google or Calendar is not an error: it is recorded on the calendar
// (and retried with backoff) while the events from the last good sync stay.
// The sync outlives a cancelled caller.
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
	cursor, err := s.syncCursor(ctx, claimed)
	if err != nil {
		retry := now.Add(DefaultFeedInterval)
		s.release(claimed.ID, &retry)
		return store.GoogleCalendar{}, err
	}
	found, err := s.list(ctx, claimed, now)
	if errors.Is(err, ErrNotConnected) || errors.Is(err, ErrNotConfigured) {
		s.release(claimed.ID, nil)
		return store.GoogleCalendar{}, err
	}
	if err != nil {
		return s.fail(ctx, claimed, err, trigger, now)
	}
	registered, err := s.ensureSource(ctx, claimed, found.defaults)
	if err != nil {
		return s.fail(ctx, claimed, err, trigger, now)
	}
	updated, counts, err := s.store(ctx, registered, found, cursor, now)
	switch {
	case errors.Is(err, ErrSyncSuperseded), errors.Is(err, ErrCalendarNotFound):
		due := now
		s.release(claimed.ID, &due)
		return store.GoogleCalendar{}, err
	case errors.Is(err, errUnhandled):
		due := now
		s.release(claimed.ID, &due)
		return store.GoogleCalendar{}, err
	case err != nil:
		// The listing could not be saved, or Calendar did not take it (the
		// sync token stays, so the next attempt lists the same changes): a
		// failure like Google's, with backoff, so a lasting fault neither
		// hammers Google nor goes unseen.
		var calendarErr *calendar.StatusError
		var unavailable *calendar.UnavailableError
		if !errors.As(err, &calendarErr) && !errors.As(err, &unavailable) && !errors.Is(err, calendar.ErrNotConfigured) {
			s.logger.Error("Google calendar sync not saved", "calendar", id, "error", err)
			err = errNotSaved
		}
		return s.fail(ctx, registered, err, trigger, now)
	}
	// The first sync, a sync that changed events, and the first good one
	// after failures are recorded; a sync that changed nothing is not.
	if counts != nil && (counts.Changed() || !claimed.LastSyncedAt.Valid) || claimed.SyncFailures > 0 {
		recorded := sources.Counts{}
		if counts != nil {
			recorded = *counts
		}
		s.recordSynced(ctx, updated, recorded, found.full, trigger, now)
	}
	return updated, nil
}

// syncCursor is the change-feed cursor a sync of a calendar the account may
// edit sends Calendar (feedCursor): the position the edits waiting for
// Google were read up to. Such a calendar does not sync while an edit of
// its events waits to be written to Google, or before the feed was read
// (ErrSyncDeferred). A read-only calendar's events are not in the feed.
func (s *Syncer) syncCursor(ctx context.Context, calendarRow store.GoogleCalendar) (string, error) {
	if !Writable(calendarRow.AccessRole) {
		return "", nil
	}
	pending, err := s.service.queries.CountCalendarPushes(ctx, calendarRow.ID)
	if err != nil {
		return "", err
	}
	connection, found, err := s.service.Connection(ctx)
	switch {
	case err != nil:
		return "", err
	case !found:
		return "", ErrNotConnected
	case pending > 0 || connection.FeedCursor == nil || connection.FeedSnapshotStartedAt.Valid:
		return "", ErrSyncDeferred
	}
	return *connection.FeedCursor, nil
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
func (s *Syncer) list(ctx context.Context, calendarRow store.GoogleCalendar, now time.Time) (listing, error) {
	token := ""
	if calendarRow.SyncToken != nil {
		token = *calendarRow.SyncToken
	}
	found, err := s.fetch(ctx, calendarRow.GoogleCalendarID, token, now)
	if token != "" && IsSyncTokenGone(err) {
		s.logger.Info("Google sync token expired; listing the calendar in full", "calendar", googleuuid.UUID(calendarRow.ID.Bytes))
		return s.fetch(ctx, calendarRow.GoogleCalendarID, "", now)
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
	var page EventsPage
	err := s.service.withToken(ctx, func(token string) (err error) {
		page, err = s.service.api.Events(ctx, token, calendarID, query)
		return err
	})
	return page, err
}

// store writes a listing in one transaction holding the calendar: the
// stored Google events, the events sent to Calendar (when something
// changed, after a full listing, or rederiveAfter since the last
// derivation: only what differs from what Calendar was last told), and the
// outcome. A calendar switched off, or resynced, since the listing started
// discards it (ErrSyncSuperseded). Calendar refusing the sync because of an
// edit not yet written to Google is errUnhandled, and nothing is stored.
func (s *Syncer) store(ctx context.Context, claimed store.GoogleCalendar, found listing, cursor string, now time.Time) (store.GoogleCalendar, *sources.Counts, error) {
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
	if !current.SyncEnabled || deref(current.SyncToken) != deref(claimed.SyncToken) {
		return store.GoogleCalendar{}, nil, ErrSyncSuperseded
	}
	changed := found.full
	if found.full {
		if err := q.DeleteGoogleEvents(ctx, current.ID); err != nil {
			return store.GoogleCalendar{}, nil, err
		}
	}
	keep, forget := partition(found.items)
	if !found.full {
		// This service's own writes come back in the next listing with the
		// etag stored when they were made: already applied, not a change.
		if keep, err = withoutOwnWrites(ctx, q, current.ID, keep); err != nil {
			return store.GoogleCalendar{}, nil, err
		}
	}
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
	stale, err := s.staleSends(ctx, q, current.ID)
	if err != nil {
		return store.GoogleCalendar{}, nil, err
	}
	if changed || stale || !current.DerivedAt.Valid || now.Sub(current.DerivedAt.Time) >= rederiveAfter {
		listed := listingChanges{changed: map[string]bool{}, gone: forget, full: found.full, startedAt: now}
		for _, item := range keep {
			listed.changed[item.ID] = true
		}
		result, err := s.derive(ctx, q, current, windowStart, zoneName, now, &listed, cursor)
		if err != nil {
			return store.GoogleCalendar{}, nil, err
		}
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

// staleSends reports whether Calendar holds an event of the calendar whose
// version is not known (handed over, or after Google refused an edit made
// in Life OS): it is sent again.
func (s *Syncer) staleSends(ctx context.Context, q *store.Queries, calendarID pgtype.UUID) (bool, error) {
	rows, err := q.ListSentEvents(ctx, calendarID)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.Item == nil {
			return true, nil
		}
	}
	return false, nil
}

// listingChanges is what a listing reported: the events changed in Google
// (changed: not this service's own writes coming back), and the deleted
// ones, those reported cancelled (gone) or, for a full listing (full),
// every event it did not list that existed before it started (startedAt).
type listingChanges struct {
	changed   map[string]bool
	gone      []string
	full      bool
	startedAt time.Time
}

// storedEvents reads a calendar's stored Google events.
func storedEvents(ctx context.Context, q *store.Queries, calendarID pgtype.UUID) ([]Event, error) {
	rows, err := q.ListGoogleEvents(ctx, calendarID)
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(rows))
	for _, row := range rows {
		var event Event
		if err := json.Unmarshal(row.Data, &event); err != nil {
			return nil, fmt.Errorf("decode stored Google event: %w", err)
		}
		events = append(events, event)
	}
	return events, nil
}

// mapperFor is the mapping of a calendar's events at now.
func (s *Syncer) mapperFor(calendarRow store.GoogleCalendar, windowStart pgtype.Timestamptz, zoneName string, now time.Time) mapper {
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
	return mapper{owner: s.owner, zone: zone, now: now, windowStart: derivedFrom}
}

// derive derives the calendar's events from every stored Google event and
// sends Calendar what changed since it was last told. In the Life OS
// calendar, the copies of exported Life OS events are not imported: a
// change made to one in Google is applied to its Life OS event instead
// (see linked.go), as far as listed says.
func (s *Syncer) derive(ctx context.Context, q *store.Queries, current store.GoogleCalendar, windowStart pgtype.Timestamptz, zoneName string, now time.Time, listed *listingChanges, cursor string) (sources.Counts, error) {
	events, err := storedEvents(ctx, q, current.ID)
	if err != nil {
		return sources.Counts{}, err
	}
	m := s.mapperFor(current, windowStart, zoneName, now)
	lifeOS, err := s.isLifeOSCalendar(ctx, q, current)
	if err != nil {
		return sources.Counts{}, err
	}
	var copies []Event
	if lifeOS {
		events, copies = splitCopies(events)
	}
	items, skipped := m.derive(events)
	if lifeOS && listed != nil {
		if err := s.followCopies(ctx, q, current, m, copies, listed, now); err != nil {
			return sources.Counts{}, err
		}
	}
	result, err := s.send(ctx, q, current, items, m.windowStart, cursor)
	if err != nil {
		return sources.Counts{}, err
	}
	result.FeedEvents, result.Skipped = len(events), skipped
	return result, nil
}

// send tells Calendar what changed among the calendar's items since it was
// last told, and records what Calendar now holds.
func (s *Syncer) send(ctx context.Context, q *store.Queries, current store.GoogleCalendar, items []sources.Item, windowStart time.Time, cursor string) (sources.Counts, error) {
	reader := ""
	if !Writable(current.AccessRole) {
		reader = ReaderReason(current.Summary, current.AccessRole)
	}
	events := make([]calendar.SourceEvent, 0, len(items))
	for _, item := range items {
		reason := reader
		if item.Expanded && reason == "" {
			reason = ExpandedReason(current.Summary)
		}
		events = append(events, sourceEvent(item, reason))
	}
	sentRows, err := q.ListSentEvents(ctx, current.ID)
	if err != nil {
		return sources.Counts{}, err
	}
	previous := make(map[string][]byte, len(sentRows))
	for _, row := range sentRows {
		previous[row.ExternalUid] = row.Item
	}
	rekeyRows, err := q.ListRekeys(ctx, current.ID)
	if err != nil {
		return sources.Counts{}, err
	}
	rekeys := map[string]string{}
	for _, row := range rekeyRows {
		rekeys[row.GoogleEventID] = row.PreviousUid
	}
	changes, err := compare(previous, events, rekeys)
	if err != nil {
		return sources.Counts{}, err
	}
	counts := sources.Counts{Imported: len(events)}
	if len(changes.items) > 0 || len(changes.deleted) > 0 {
		start := windowStart.UTC()
		for _, batch := range changes.batches() {
			batch.Window, batch.FeedCursor = &calendar.Window{Start: &start}, cursor
			result, refused, err := s.syncBatch(ctx, current, batch)
			if err != nil {
				return sources.Counts{}, err
			}
			for _, uid := range refused {
				delete(changes.sent, uid) // sent again next time
				counts.Skipped++
			}
			counts.Inserted += result.Inserted
			counts.Updated += result.Updated
			counts.Removed += result.Removed
			counts.Restored += result.Restored
		}
	}
	counts.Unchanged = max(counts.Imported-counts.Inserted-counts.Updated-counts.Restored, 0)
	if len(changes.deleted) > 0 {
		if err := q.DeleteSentEvents(ctx, store.DeleteSentEventsParams{CalendarID: current.ID, Uids: changes.deleted}); err != nil {
			return sources.Counts{}, err
		}
	}
	if len(changes.items) > 0 {
		rows := make([]map[string]any, 0, len(changes.items))
		for _, item := range changes.items {
			if sent, ok := changes.sent[item.ExternalUID]; ok {
				rows = append(rows, map[string]any{"uid": item.ExternalUID, "item": sent})
			}
		}
		raw, err := json.Marshal(rows)
		if err != nil {
			return sources.Counts{}, err
		}
		if err := q.UpsertSentEvents(ctx, store.UpsertSentEventsParams{CalendarID: current.ID, Items: raw}); err != nil {
			return sources.Counts{}, err
		}
	}
	var used []string
	for uid := range rekeys {
		if _, sent := changes.sent[uid]; sent {
			used = append(used, uid)
		}
	}
	if len(used) > 0 {
		if err := q.DeleteRekeys(ctx, store.DeleteRekeysParams{CalendarID: current.ID, GoogleEventIds: used}); err != nil {
			return sources.Counts{}, err
		}
	}
	return counts, nil
}

// maxRefusedItems bounds the events of one batch Calendar may refuse before
// the sync fails.
const maxRefusedItems = 20

// itemIndex finds the index of the item Calendar names in a refusal
// ("items[3] (externalUid ...): ...").
var itemIndex = regexp.MustCompile(`^items\[(\d+)\]`)

// syncBatch sends one batch. An event Calendar refuses (invalid_item,
// invalid_exception: something the mapping produced that Calendar does not
// hold) is left out and the batch sent again, so one event never stops a
// calendar from syncing; refused are their uids.
func (s *Syncer) syncBatch(ctx context.Context, current store.GoogleCalendar, batch calendar.SyncRequest) (calendar.SyncResult, []string, error) {
	var refused []string
	for {
		result, err := s.service.calendar.Sync(ctx, current.GoogleCalendarID, batch)
		if calendar.HasCode(err, http.StatusConflict, "unhandled_changes") {
			return calendar.SyncResult{}, nil, errUnhandled
		}
		var answer *calendar.StatusError
		if errors.As(err, &answer) && answer.Status == http.StatusBadRequest && (answer.Code == "invalid_item" || answer.Code == "invalid_exception") && len(refused) < maxRefusedItems {
			if match := itemIndex.FindStringSubmatch(answer.Message); match != nil {
				if index, convErr := strconv.Atoi(match[1]); convErr == nil && index < len(batch.Items) {
					uid := batch.Items[index].ExternalUID
					s.logger.Warn("Google event left out: Calendar refused it", "calendar", googleuuid.UUID(current.ID.Bytes), "reason", answer.Code, "item", index)
					refused = append(refused, uid)
					batch.Items = append(append([]calendar.SourceEvent{}, batch.Items[:index]...), batch.Items[index+1:]...)
					continue
				}
			}
		}
		return result, refused, err
	}
}

// withoutOwnWrites drops the listed events whose etag is the stored one:
// this service wrote them itself and stored what Google answered.
func withoutOwnWrites(ctx context.Context, q *store.Queries, calendarID pgtype.UUID, keep []storedEvent) ([]storedEvent, error) {
	ids := make([]string, 0, len(keep))
	for _, item := range keep {
		if item.Data.ETag != "" {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) == 0 {
		return keep, nil
	}
	rows, err := q.ListGoogleEventETags(ctx, store.ListGoogleEventETagsParams{CalendarID: calendarID, EventIds: ids})
	if err != nil {
		return nil, err
	}
	stored := make(map[string]string, len(rows))
	for _, row := range rows {
		stored[row.EventID] = row.Etag
	}
	out := keep[:0]
	for _, item := range keep {
		if etag, ok := stored[item.ID]; ok && etag != "" && etag == item.Data.ETag {
			continue
		}
		out = append(out, item)
	}
	return out, nil
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

// fail records a failed sync: the error, one more failure, and the next
// attempt after a backoff. Events stay as they are. Activity records the
// start of a failure streak, and a failure that differs from the last one;
// an expired connection is recorded once by the connection itself.
func (s *Syncer) fail(ctx context.Context, calendarRow store.GoogleCalendar, cause error, trigger SyncTrigger, now time.Time) (store.GoogleCalendar, error) {
	message := syncErrorMessage(cause)
	failures := int(calendarRow.SyncFailures) + 1
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	updated, err := s.service.queries.FailGoogleCalendarSync(ctx, store.FailGoogleCalendarSyncParams{
		ID: calendarRow.ID, LastSyncError: &message, NextSyncAt: timestamptz(now.Add(SyncBackoff(s.interval, failures))),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Switched off (or disconnected) meanwhile: nothing to record.
		s.release(calendarRow.ID, nil)
		return store.GoogleCalendar{}, ErrSyncSuperseded
	}
	if err != nil {
		s.release(calendarRow.ID, nil)
		return store.GoogleCalendar{}, err
	}
	if !errors.Is(cause, ErrExpired) && (calendarRow.SyncFailures == 0 || deref(calendarRow.LastSyncError) != message) {
		s.recordFailed(ctx, updated, message, trigger, now)
	}
	return updated, nil
}

// dueSlack is how early SyncDue takes a calendar as due: a tenth of the
// interval, at most 30 seconds.
func dueSlack(interval time.Duration) time.Duration {
	return min(interval/10, 30*time.Second)
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
// and Calendar's own messages never carry tokens.
func syncErrorMessage(err error) string {
	var apiErr *APIError
	var oauth *OAuthError
	var unavailable *UnavailableError
	var calendarErr *calendar.StatusError
	switch {
	case errors.Is(err, ErrExpired):
		return "Google access expired or was revoked; reconnect Google in Calendar"
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound:
		return "Google no longer has this calendar, or this account cannot see it; refresh the calendar list"
	case errors.As(err, &apiErr), errors.As(err, &oauth), errors.As(err, &unavailable):
		return err.Error()
	case calendar.HasCode(err, http.StatusConflict, "source_disabled"):
		return "the calendar is turned off in Life OS's Calendar sources; turn it on there to sync it again"
	case calendar.IsUnavailable(err):
		return "Calendar could not be reached; the sync is retried automatically"
	case errors.As(err, &calendarErr):
		return "Calendar refused the sync: " + calendarErr.Message
	case errors.Is(err, errNotSaved):
		return errNotSaved.Error()
	case errors.Is(err, ErrUnreadableToken):
		return "the stored Google token cannot be decrypted with LIFE_OS_GOOGLE_TOKEN_KEY (was the key changed?); reconnect Google in Calendar"
	}
	return "the sync failed"
}

// logCode is what the service's log says went wrong: a code, never Google's
// or Calendar's own words, which can quote an event, nor a Google calendar's
// id, which is often an email address (docs/architecture/multi-user.md#logs).
// The full message stays where the owner reads it (the sync's last error).
func logCode(err error) string {
	var apiErr *APIError
	var oauth *OAuthError
	var unavailable *UnavailableError
	var calendarErr *calendar.StatusError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrExpired):
		return "google_expired"
	case errors.Is(err, ErrSyncInProgress):
		return "sync_in_progress"
	case errors.Is(err, ErrSyncDisabled):
		return "sync_disabled"
	case errors.Is(err, ErrSyncSuperseded):
		return "sync_superseded"
	case errors.Is(err, ErrCalendarNotFound):
		return "calendar_not_found"
	case errors.Is(err, ErrNotConnected):
		return "not_connected"
	case errors.Is(err, ErrSyncDeferred):
		return "sync_deferred"
	case errors.Is(err, errUnhandled):
		return "unhandled"
	case errors.Is(err, errCalendarGone):
		return "calendar_gone"
	case errors.Is(err, errNotSaved):
		return "not_saved"
	case errors.Is(err, ErrUnreadableToken):
		return "token_unreadable"
	case errors.As(err, &apiErr):
		return fmt.Sprintf("google_http_%d", apiErr.Status)
	case errors.As(err, &oauth):
		return "google_oauth_" + safeCode(oauth.Code)
	case errors.As(err, &unavailable):
		return "google_unavailable"
	case calendar.IsUnavailable(err):
		return "calendar_unavailable"
	case errors.As(err, &calendarErr):
		return fmt.Sprintf("calendar_http_%d_%s", calendarErr.Status, safeCode(calendarErr.Code))
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "failed"
}

// safeCode keeps a provider's error code only when it has a code's form.
func safeCode(code string) string {
	if code == "" || len(code) > 64 || strings.Trim(code, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return "other"
	}
	return code
}

// idHash names an id that may be personal (a Google calendar's id is often
// an email address) in the log: the first 12 hex digits of its SHA-256.
func idHash(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:6])
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// sourceSubject is the Activity subject of a calendar's syncs: its source
// in Calendar once registered.
func sourceSubject(calendarRow store.GoogleCalendar) (activity.Subject, string) {
	if calendarRow.SourceID.Valid {
		id := googleuuid.UUID(calendarRow.SourceID.Bytes).String()
		return activity.Subject{Type: "calendar_source", ID: id}, id
	}
	return activity.Subject{Type: "google_calendar", ID: googleuuid.UUID(calendarRow.ID.Bytes).String()}, ""
}

// recordSynced records a sync that changed events (or the first one, or
// the first after failures) as one calendar.source.refreshed, attributed to
// SyncActor; a sync that changed nothing records nothing.
func (s *Syncer) recordSynced(ctx context.Context, calendarRow store.GoogleCalendar, counts sources.Counts, full bool, trigger SyncTrigger, now time.Time) {
	data := s.sourceData(calendarRow, trigger)
	data["outcome"], data["counts"], data["full"] = "imported", counts, full
	summary := fmt.Sprintf("Synced %s from Google: %d new, %d changed, %d removed, %d restored", calendarRow.Summary, counts.Inserted, counts.Updated, counts.Removed, counts.Restored)
	s.emit(ctx, EventSourceRefreshed, calendarRow, activity.Succeeded, summary, data, trigger, now)
}

func (s *Syncer) recordFailed(ctx context.Context, calendarRow store.GoogleCalendar, message string, trigger SyncTrigger, now time.Time) {
	data := s.sourceData(calendarRow, trigger)
	data["outcome"], data["error"], data["failures"] = "failed", message, calendarRow.SyncFailures
	summary := fmt.Sprintf("Syncing %s from Google failed: %s", calendarRow.Summary, message)
	s.emit(ctx, EventSourceRefreshFailed, calendarRow, activity.Failed, summary, data, trigger, now)
}

func (s *Syncer) sourceData(calendarRow store.GoogleCalendar, trigger SyncTrigger) map[string]any {
	_, sourceID := sourceSubject(calendarRow)
	data := map[string]any{"sourceName": calendarRow.Summary, "sourceKind": "external", "provider": ProviderLabel, "trigger": "scheduled"}
	if sourceID != "" {
		data["sourceId"] = sourceID
	}
	if trigger.Manual {
		data["trigger"] = "manual"
		if !trigger.Trace.Actor.IsZero() {
			data["requestedBy"] = trigger.Trace.Actor
		}
	}
	return data
}

func (s *Syncer) emit(ctx context.Context, eventType string, calendarRow store.GoogleCalendar, outcome string, summary string, data map[string]any, trigger SyncTrigger, now time.Time) {
	subject, _ := sourceSubject(calendarRow)
	s.service.activity.Emit(ctx, activity.Event{
		ID:            activity.EventID(eventType, subject.ID, now.Format(time.RFC3339Nano)),
		Type:          eventType,
		Subject:       subject,
		CorrelationID: trigger.Trace.CorrelationID,
		CausationID:   trigger.Trace.CausationID,
		Actor:         SyncActor,
		Outcome:       outcome,
		Summary:       activity.Preview(summary, 500),
		Data:          data,
		OccurredAt:    now,
	})
}
