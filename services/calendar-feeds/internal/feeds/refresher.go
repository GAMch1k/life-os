package feeds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/calendar"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/ics"
	store "github.com/GAMch1k/life-os/services/calendar-feeds/internal/repository/generated"
)

// Refresh outcomes, as stored in feeds.last_outcome.
const (
	OutcomeImported    = "imported"
	OutcomeNotModified = "not_modified"
	OutcomeFailed      = "failed"
)

var (
	ErrNotFound   = errors.New("feed not found")
	ErrDisabled   = errors.New("the feed's source is disabled in Calendar")
	ErrInProgress = errors.New("a refresh of this feed is already running")
	// ErrSuperseded: the feed's URL or filter changed while it was being
	// refreshed; the result was discarded and a new refresh is due now.
	ErrSuperseded = errors.New("the feed changed during the refresh; it will refresh again shortly")
)

// Counts summarizes a syncing refresh: what the feed held, what the filter
// kept, and what Calendar's sync changed.
type Counts struct {
	FeedEvents int `json:"feedEvents"`
	Imported   int `json:"imported"`
	Inserted   int `json:"inserted"`
	Updated    int `json:"updated"`
	Removed    int `json:"removed"`
	Restored   int `json:"restored"`
	Unchanged  int `json:"unchanged"`
	Skipped    int `json:"skipped"`
}

// Trigger says why a refresh runs.
type Trigger struct {
	// Manual: requested through the API ("Refresh now"), by Trace.Actor.
	Manual bool
	Trace  activity.Trace
}

// Refresher refreshes feeds: it downloads a feed, parses and filters its
// events, syncs them to the feed's Calendar source as one snapshot, and
// records the outcome. One refresh per feed runs at a time: calls for a feed
// already refreshing in this process share its result, and a lease in the
// database keeps other processes (and a restarted one) out until it is
// released or expires.
type Refresher struct {
	pool     *pgxpool.Pool
	queries  *store.Queries
	fetcher  *ics.Fetcher
	calendar *calendar.Client
	schedule Schedule
	owner    *time.Location
	emitter  *activity.Emitter
	logger   *slog.Logger
	now      func() time.Time
	group    singleflight.Group
	// lease bounds a refresh (download plus sync); a crashed refresh's lease
	// expires after it.
	lease time.Duration
}

// NewRefresher refreshes the feeds stored in pool into Calendar. emitter may
// be nil.
func NewRefresher(pool *pgxpool.Pool, fetcher *ics.Fetcher, client *calendar.Client, schedule Schedule, emitter *activity.Emitter, logger *slog.Logger) *Refresher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Refresher{
		pool: pool, queries: store.New(pool), fetcher: fetcher, calendar: client, schedule: schedule, owner: schedule.Location,
		emitter: emitter, logger: logger, now: time.Now, lease: fetcher.Timeout + 2*time.Minute,
	}
}

// SetClock replaces the refresher's clock (tests).
func (r *Refresher) SetClock(now func() time.Time) { r.now = now }

// Now is the refresher's clock.
func (r *Refresher) Now() time.Time { return r.now() }

// Schedule is the daily refresh slot.
func (r *Refresher) Schedule() Schedule { return r.schedule }

// Fetcher validates and downloads feeds.
func (r *Refresher) Fetcher() *ics.Fetcher { return r.fetcher }

// Calendar is the client of Calendar's integration API.
func (r *Refresher) Calendar() *calendar.Client { return r.calendar }

// Refresh downloads the feed, syncs its events to Calendar, records the
// outcome, and returns the feed. A failed download, parse, or sync is not an
// error: it is recorded on the feed (and retried with backoff) while the
// events from the last good refresh stay. The refresh outlives a cancelled
// caller, so a refresh that has started completes.
func (r *Refresher) Refresh(ctx context.Context, id googleuuid.UUID, trigger Trigger) (store.Feed, error) {
	result, err, _ := r.group.Do(id.String(), func() (any, error) {
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.lease)
		defer cancel()
		return r.refresh(work, id, trigger)
	})
	if err != nil {
		return store.Feed{}, err
	}
	return result.(store.Feed), nil
}

// outcome is what a refresh found, before it is written.
type outcome struct {
	kind               string
	err                error
	counts             *Counts
	etag, lastModified *string
}

func (r *Refresher) refresh(ctx context.Context, id googleuuid.UUID, trigger Trigger) (store.Feed, error) {
	now := r.now().UTC()
	feedID := pgtype.UUID{Bytes: id, Valid: true}
	feed, err := r.queries.ClaimFeedRefresh(ctx, store.ClaimFeedRefreshParams{
		ID: feedID, Now: at(now), LeaseUntil: at(now.Add(r.lease)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		current, err := r.queries.GetFeed(ctx, feedID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return store.Feed{}, ErrNotFound
		case err != nil:
			return store.Feed{}, err
		case !current.Enabled:
			return store.Feed{}, ErrDisabled
		}
		return store.Feed{}, ErrInProgress
	}
	if err != nil {
		return store.Feed{}, err
	}
	updated, found, err := r.complete(ctx, feed, now)
	if err != nil {
		// Leave the feed as it was for the next attempt.
		if releaseErr := r.release(feedID); releaseErr != nil {
			r.logger.Warn("calendar feed lease not released", "feed", id, "error", releaseErr)
		}
		return store.Feed{}, err
	}
	r.record(ctx, updated, found, trigger, now)
	return updated, nil
}

func (r *Refresher) release(id pgtype.UUID) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.queries.ReleaseFeedRefresh(ctx, id)
}

// complete downloads and parses the feed, syncs the events to Calendar, then,
// in one transaction holding the feed row, writes the outcome.
func (r *Refresher) complete(ctx context.Context, feed store.Feed, now time.Time) (store.Feed, outcome, error) {
	found := outcome{kind: OutcomeImported}
	// A source still to register (a new preset feed, or one whose name or
	// reminders the preset changed) is registered before its events are
	// synced; an unregistered feed cannot sync.
	if feed.RegisterPending || !feed.SourceID.Valid {
		registered, err := r.Register(ctx, feed)
		switch {
		case err == nil:
			feed = registered
		case !feed.SourceID.Valid:
			found.kind, found.err = OutcomeFailed, &ics.Error{Message: "Calendar has not registered the feed yet: " + calendarMessage(err)}
		default:
			r.logger.Warn("calendar feed source not registered again; retried later", "feed", uuidString(feed.ID), "error", calendarCode(err))
		}
	}

	var parsed ics.Feed
	if found.kind == OutcomeImported {
		result, err := r.fetcher.Fetch(ctx, feed.Url, deref(feed.Etag), deref(feed.LastModified))
		if err == nil && !result.NotModified {
			parsed, err = ics.Parse(result.Body, ics.Options{Owner: r.owner, Now: now})
			parsed.Items = presetItems(feed.Preset, parsed.Items)
		}
		found.err, found.etag, found.lastModified = err, optional(result.ETag), optional(result.LastModified)
		switch {
		case err != nil:
			found.kind = OutcomeFailed
		case result.NotModified:
			found.kind = OutcomeNotModified
		}
	}

	if found.kind == OutcomeImported {
		current, err := r.queries.GetFeed(ctx, feed.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.Feed{}, found, ErrNotFound
		}
		if err != nil {
			return store.Feed{}, found, err
		}
		if reconfigured(feed, current) {
			return store.Feed{}, found, r.supersede(ctx, feed.ID, now)
		}
		filter := ics.Filter{Include: feed.FilterInclude, Exclude: feed.FilterExclude}
		events, unsendable := SourceEvents(filter.Apply(parsed.Items))
		// Feeds often list only upcoming dates (window upcomingOnly), and an
		// empty calendar where there were upcoming events is far more likely
		// an upstream fault than every event being cancelled: Calendar
		// refuses such a snapshot (emptyGuard) and keeps the events.
		synced, err := r.calendar.SyncSource(ctx, uuidString(feed.ID), calendar.Sync{
			Mode: "snapshot", Items: events, Window: &calendar.Window{UpcomingOnly: true}, EmptyGuard: parsed.Components == 0,
		})
		switch {
		case err == nil:
			found.counts = &Counts{
				FeedEvents: len(parsed.Items), Imported: len(events), Skipped: parsed.Skipped + unsendable,
				Inserted: synced.Inserted, Updated: synced.Updated, Removed: synced.Removed, Restored: synced.Restored, Unchanged: synced.Unchanged,
			}
		case calendar.Is(err, http.StatusConflict, calendar.CodeEmptySnapshot):
			found.kind, found.err = OutcomeFailed, &ics.Error{Message: "the feed contains no events; keeping the events from the last refresh"}
		case calendar.Is(err, http.StatusConflict, calendar.CodeSourceDisabled):
			// The owner disabled the source in Calendar meanwhile: not a
			// failure of the feed. It is refreshed again once enabled.
			if err := r.queries.SetFeedEnabled(ctx, store.SetFeedEnabledParams{ID: feed.ID, Enabled: false}); err != nil {
				return store.Feed{}, found, err
			}
			return store.Feed{}, found, ErrDisabled
		case calendar.IsNotFound(err):
			// The source was deleted in Calendar (with its events): the feed
			// goes too. A preset feed comes back at the next start.
			if _, err := r.queries.DeleteFeed(ctx, feed.ID); err != nil {
				return store.Feed{}, found, err
			}
			r.logger.Info("calendar feed removed: its Calendar source was deleted", "feed", uuidString(feed.ID))
			return store.Feed{}, found, ErrNotFound
		default:
			found.kind, found.err = OutcomeFailed, &ics.Error{Message: "Calendar did not take the events: " + calendarMessage(err)}
		}
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return store.Feed{}, found, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	queries := r.queries.WithTx(tx)
	current, err := queries.LockFeed(ctx, feed.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Feed{}, found, ErrNotFound
	}
	if err != nil {
		return store.Feed{}, found, err
	}
	params := store.FinishFeedRefreshParams{
		ID: current.ID, LastOutcome: &found.kind, LastCounts: current.LastCounts, LastSuccessAt: current.LastSuccessAt,
		Etag: current.Etag, LastModified: current.LastModified,
	}
	switch found.kind {
	case OutcomeFailed:
		message := errorMessage(found.err)
		params.LastError, params.ConsecutiveFailures = &message, current.ConsecutiveFailures+1
		params.NextRefreshAt = at(r.schedule.retryAt(now, int(params.ConsecutiveFailures)))
	default:
		params.LastSuccessAt, params.NextRefreshAt = at(now), at(r.schedule.Next(now))
		params.Etag, params.LastModified = found.etag, found.lastModified
		if found.counts != nil {
			raw, err := json.Marshal(found.counts)
			if err != nil {
				return store.Feed{}, found, err
			}
			params.LastCounts = raw
		}
	}
	if reconfigured(feed, current) {
		// The URL or filter changed after the events were synced: the new
		// configuration needs a full download right away.
		params.Etag, params.LastModified, params.NextRefreshAt = current.Etag, current.LastModified, at(now)
	}
	updated, err := queries.FinishFeedRefresh(ctx, params)
	if err != nil {
		return store.Feed{}, found, err
	}
	return updated, found, tx.Commit(ctx)
}

// supersede discards a refresh whose feed was reconfigured meanwhile: the
// state stays, the lease is released, and the feed is due again now.
func (r *Refresher) supersede(ctx context.Context, id pgtype.UUID, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	queries := r.queries.WithTx(tx)
	current, err := queries.LockFeed(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = queries.FinishFeedRefresh(ctx, store.FinishFeedRefreshParams{
		ID: current.ID, LastOutcome: current.LastOutcome, LastError: current.LastError, LastCounts: current.LastCounts,
		LastSuccessAt: current.LastSuccessAt, ConsecutiveFailures: current.ConsecutiveFailures,
		Etag: current.Etag, LastModified: current.LastModified, NextRefreshAt: at(now),
	})
	if err == nil {
		err = tx.Commit(ctx)
	}
	return errors.Join(ErrSuperseded, err)
}

// reconfigured reports whether the feed's URL or filter differ now from
// those it was claimed with.
func reconfigured(claimed, current store.Feed) bool {
	return current.Url != claimed.Url || !slices.Equal(current.FilterInclude, claimed.FilterInclude) || !slices.Equal(current.FilterExclude, claimed.FilterExclude)
}

// record emits the refresh outcome to Activity, attributed to the feed.
func (r *Refresher) record(ctx context.Context, feed store.Feed, found outcome, trigger Trigger, now time.Time) {
	id := SubjectID(feed)
	data := map[string]any{"sourceId": id, "feedId": uuidString(feed.ID), "sourceName": feed.Name, "outcome": found.kind, "trigger": "scheduled"}
	if trigger.Manual {
		data["trigger"] = "manual"
		if !trigger.Trace.Actor.IsZero() {
			data["requestedBy"] = trigger.Trace.Actor
		}
	}
	eventType, outcomeKind := "calendar.source.refreshed", activity.Succeeded
	var summary string
	switch {
	case found.kind == OutcomeFailed:
		eventType, outcomeKind = "calendar.source.refresh_failed", activity.Failed
		message := errorMessage(found.err)
		data["error"] = message
		summary = fmt.Sprintf("Refreshing %s failed: %s", feed.Name, message)
	case found.kind == OutcomeNotModified:
		summary = fmt.Sprintf("%s is unchanged", feed.Name)
	default:
		counts := found.counts
		if counts == nil {
			counts = &Counts{}
		}
		data["counts"] = counts
		summary = fmt.Sprintf("Refreshed %s: %d new, %d changed, %d removed, %d restored", feed.Name, counts.Inserted, counts.Updated, counts.Removed, counts.Restored)
	}
	event := activity.Event{
		ID:            activity.EventID(eventType, id, now.Format(time.RFC3339Nano)),
		Type:          eventType,
		Subject:       activity.Subject{Type: "calendar_source", ID: id},
		CorrelationID: trigger.Trace.CorrelationID,
		CausationID:   trigger.Trace.CausationID,
		Actor:         SourceActor(feed.Name),
		Outcome:       outcomeKind,
		Summary:       activity.Preview(summary, 500),
		Data:          data,
		OccurredAt:    now,
	}
	r.emitter.Emit(ctx, event)
}

// SubjectID is the id a feed's Activity events are about: its Calendar
// source (calendar_source), else, before Calendar registered it, the feed.
func SubjectID(feed store.Feed) string {
	if feed.SourceID.Valid {
		return uuidString(feed.SourceID)
	}
	return uuidString(feed.ID)
}

// SourceActor is the system actor a feed's refreshes are attributed to, such
// as system:ics-zaw for a feed named "ZAW".
func SourceActor(name string) activity.Actor {
	slug := strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss").Replace(strings.ToLower(name))
	var b strings.Builder
	dash := false
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	cleaned := strings.Trim(b.String(), "-")
	if len(cleaned) > 40 {
		cleaned = strings.Trim(cleaned[:40], "-")
	}
	if cleaned == "" {
		return activity.System("ics-source")
	}
	return activity.System("ics-" + cleaned)
}

func errorMessage(err error) string {
	var own *ics.Error
	if errors.As(err, &own) {
		return own.Message
	}
	if err == nil {
		return "unknown error"
	}
	return "the refresh failed"
}

// calendarMessage says why a call to Calendar failed. Calendar never sees a
// feed's URL, so its answer cannot carry one.
// calendarCode is what the log says Calendar answered: its status and code,
// never its message, which can quote an event
// (docs/architecture/multi-user.md#logs).
func calendarCode(err error) string {
	var answer *calendar.StatusError
	switch {
	case errors.As(err, &answer):
		code := answer.Code
		if code == "" || len(code) > 64 || strings.Trim(code, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
			code = "other"
		}
		return fmt.Sprintf("calendar_http_%d_%s", answer.Status, code)
	case errors.Is(err, calendar.ErrNotConfigured):
		return "calendar_not_configured"
	case errors.Is(err, errUnknownSources):
		return "unknown_sources"
	}
	return "calendar_unreachable"
}

func calendarMessage(err error) string {
	var answer *calendar.StatusError
	switch {
	case errors.As(err, &answer) && answer.Message != "":
		return fmt.Sprintf("%s (%d %s)", answer.Message, answer.Status, answer.Code)
	case errors.As(err, &answer):
		return fmt.Sprintf("Calendar answered %d", answer.Status)
	case errors.Is(err, calendar.ErrNotConfigured), errors.Is(err, errUnknownSources):
		return err.Error()
	}
	return "Calendar could not be reached"
}

func at(value time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: value, Valid: true} }

func uuidString(id pgtype.UUID) string { return googleuuid.UUID(id.Bytes).String() }

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
