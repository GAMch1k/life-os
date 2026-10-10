package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/calendar/internal/ics"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// Refresh outcomes, as stored in calendar_sources.last_outcome.
const (
	OutcomeImported    = "imported"
	OutcomeNotModified = "not_modified"
	OutcomeFailed      = "failed"
)

var (
	ErrNotFound   = errors.New("source not found")
	ErrDisabled   = errors.New("the source is disabled")
	ErrInProgress = errors.New("a refresh of this source is already running")
	// ErrSuperseded: the source's URL or filter changed while it was being
	// refreshed; the result was discarded and a new refresh is due now.
	ErrSuperseded = errors.New("the source changed during the refresh; it will refresh again shortly")
)

// Trigger says why a refresh runs.
type Trigger struct {
	// Manual: requested through the API ("Refresh now"), by Trace.Actor.
	Manual bool
	Trace  activity.Trace
}

// Refresher refreshes sources. One refresh per source runs at a time: calls
// for a source already refreshing in this process share its result, and a
// lease in the database keeps other processes (and a restarted one) out
// until it is released or expires.
type Refresher struct {
	pool     *pgxpool.Pool
	queries  *store.Queries
	fetcher  *ics.Fetcher
	schedule Schedule
	owner    *time.Location
	emitter  *activity.Emitter
	logger   *slog.Logger
	now      func() time.Time
	group    singleflight.Group
	// lease bounds a refresh (download plus reconcile); a crashed refresh's
	// lease expires after it.
	lease time.Duration
}

// NewRefresher refreshes sources stored in pool. emitter may be nil.
func NewRefresher(pool *pgxpool.Pool, fetcher *ics.Fetcher, schedule Schedule, emitter *activity.Emitter, logger *slog.Logger) *Refresher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Refresher{
		pool: pool, queries: store.New(pool), fetcher: fetcher, schedule: schedule, owner: schedule.Location,
		emitter: emitter, logger: logger, now: time.Now, lease: fetcher.Timeout + 2*time.Minute,
	}
}

// SetClock replaces the refresher's clock (tests).
func (r *Refresher) SetClock(now func() time.Time) { r.now = now }

// Schedule is the daily refresh slot.
func (r *Refresher) Schedule() Schedule { return r.schedule }

// Fetcher validates and downloads feeds.
func (r *Refresher) Fetcher() *ics.Fetcher { return r.fetcher }

// Refresh downloads the source's feed, reconciles its events, records the
// outcome, and returns the source. A failed download or parse is not an
// error: it is recorded on the source (and retried with backoff) while the
// events from the last good refresh stay. The refresh outlives a cancelled
// caller, so a refresh that has started completes.
func (r *Refresher) Refresh(ctx context.Context, id googleuuid.UUID, trigger Trigger) (store.CalendarSource, error) {
	result, err, _ := r.group.Do(id.String(), func() (any, error) {
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.lease)
		defer cancel()
		return r.refresh(work, id, trigger)
	})
	if err != nil {
		return store.CalendarSource{}, err
	}
	return result.(store.CalendarSource), nil
}

// outcome is what a refresh found, before it is written.
type outcome struct {
	kind               string
	err                error
	counts             *Counts
	etag, lastModified *string
}

func (r *Refresher) refresh(ctx context.Context, id googleuuid.UUID, trigger Trigger) (store.CalendarSource, error) {
	now := r.now().UTC()
	sourceID := pgtype.UUID{Bytes: id, Valid: true}
	source, err := r.queries.ClaimSourceRefresh(ctx, store.ClaimSourceRefreshParams{
		ID: sourceID, Now: at(now), LeaseUntil: at(now.Add(r.lease)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		current, err := r.queries.GetSource(ctx, sourceID)
		switch {
		case errors.Is(err, pgx.ErrNoRows), err == nil && current.Kind != "ics":
			// A Google calendar's source syncs through the Google connection.
			return store.CalendarSource{}, ErrNotFound
		case err != nil:
			return store.CalendarSource{}, err
		case !current.Enabled:
			return store.CalendarSource{}, ErrDisabled
		}
		return store.CalendarSource{}, ErrInProgress
	}
	if err != nil {
		return store.CalendarSource{}, err
	}
	updated, found, err := r.complete(ctx, source, now)
	if err != nil {
		// Leave the source as it was for the next attempt.
		if releaseErr := r.release(sourceID); releaseErr != nil {
			r.logger.Warn("calendar source lease not released", "source", id, "error", releaseErr)
		}
		return store.CalendarSource{}, err
	}
	r.record(ctx, updated, found, trigger, now)
	return updated, nil
}

func (r *Refresher) release(id pgtype.UUID) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.queries.ReleaseSourceRefresh(ctx, id)
}

// complete downloads and parses the feed, then, in one transaction holding
// the source row, reconciles the events and writes the outcome.
func (r *Refresher) complete(ctx context.Context, source store.CalendarSource, now time.Time) (store.CalendarSource, outcome, error) {
	var feed ics.Feed
	result, err := r.fetcher.Fetch(ctx, source.Url, deref(source.Etag), deref(source.LastModified))
	if err == nil && !result.NotModified {
		feed, err = ics.Parse(result.Body, ics.Options{Owner: r.owner, Now: now})
		feed.Items = presetItems(source.Preset, feed.Items)
	}
	found := outcome{kind: OutcomeImported, err: err, etag: optional(result.ETag), lastModified: optional(result.LastModified)}
	switch {
	case err != nil:
		found.kind = OutcomeFailed
	case result.NotModified:
		found.kind = OutcomeNotModified
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return store.CalendarSource{}, found, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	queries := r.queries.WithTx(tx)
	current, err := queries.LockSource(ctx, source.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.CalendarSource{}, found, ErrNotFound
	}
	if err != nil {
		return store.CalendarSource{}, found, err
	}
	if current.Url != source.Url || !slices.Equal(current.FilterInclude, source.FilterInclude) || !slices.Equal(current.FilterExclude, source.FilterExclude) {
		_, err := queries.FinishSourceRefresh(ctx, store.FinishSourceRefreshParams{
			ID: current.ID, LastOutcome: current.LastOutcome, LastError: current.LastError, LastCounts: current.LastCounts,
			LastSuccessAt: current.LastSuccessAt, ConsecutiveFailures: current.ConsecutiveFailures,
			Etag: current.Etag, LastModified: current.LastModified, NextRefreshAt: at(now),
		})
		if err == nil {
			err = tx.Commit(ctx)
		}
		return store.CalendarSource{}, found, errors.Join(ErrSuperseded, err)
	}
	if found.kind == OutcomeImported {
		existing, err := queries.ListSourceEvents(ctx, current.ID)
		if err != nil {
			return store.CalendarSource{}, found, err
		}
		if feed.Components == 0 && hasActiveFuture(existing, now, r.owner) {
			// An empty calendar where there were upcoming events is far more
			// likely an upstream fault than every event being cancelled.
			found.kind, found.err = OutcomeFailed, &ics.Error{Message: "the feed contains no events; keeping the events from the last refresh"}
		} else {
			filter := ics.Filter{Include: current.FilterInclude, Exclude: current.FilterExclude}
			changes := reconcile(existing, filter.Apply(feed.Items), now, r.owner)
			changes.counts.FeedEvents, changes.counts.Skipped = len(feed.Items), feed.Skipped
			if err := apply(ctx, queries, current, changes, now); err != nil {
				return store.CalendarSource{}, found, err
			}
			found.counts = &changes.counts
		}
	}
	params := store.FinishSourceRefreshParams{
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
				return store.CalendarSource{}, found, err
			}
			params.LastCounts = raw
		}
	}
	updated, err := queries.FinishSourceRefresh(ctx, params)
	if err != nil {
		return store.CalendarSource{}, found, err
	}
	return updated, found, tx.Commit(ctx)
}

// Reconcile makes a source's stored events match items, inside the
// caller's transaction (which holds the source row), and returns what it
// changed. A feed's refresh reconciles with FeedRules, a Google calendar's
// sync with SyncRules.
func Reconcile(ctx context.Context, queries *store.Queries, source store.CalendarSource, items []Item, rules Rules, now time.Time) (Counts, error) {
	existing, err := queries.ListSourceEvents(ctx, source.ID)
	if err != nil {
		return Counts{}, err
	}
	var series []pgtype.UUID
	for _, event := range existing {
		if event.RecurrenceFrequency != nil {
			series = append(series, event.ID)
		}
	}
	exceptions := map[[16]byte][]store.EventException{}
	if len(series) > 0 {
		rows, err := queries.ListEventExceptions(ctx, series)
		if err != nil {
			return Counts{}, err
		}
		for _, row := range rows {
			exceptions[row.EventID.Bytes] = append(exceptions[row.EventID.Bytes], row)
		}
	}
	changes := reconcileItems(existing, exceptions, items, rules)
	if err := apply(ctx, queries, source, changes, now); err != nil {
		return Counts{}, err
	}
	return changes.counts, nil
}

// apply writes a plan inside the refresh transaction.
func apply(ctx context.Context, queries *store.Queries, source store.CalendarSource, changes plan, now time.Time) error {
	for _, item := range changes.inserts {
		fields := importedFields(item, pgtype.Timestamptz{}, now)
		uid := item.ExternalUID
		params := store.InsertImportedEventParams{
			Title: fields.title, Description: item.Description, Location: item.Location, AllDay: item.AllDay,
			StartAt: fields.startAt, EndAt: fields.endAt, StartDate: fields.startDate, EndDate: fields.endDate, TimeZone: item.TimeZone,
			RecurrenceFrequency: fields.rule.frequency, RecurrenceInterval: fields.rule.interval, RecurrenceUntil: fields.rule.until,
			RecurrenceByWeekday: fields.rule.byWeekday, RecurrenceCount: fields.rule.count,
			Reminders: []byte("[]"), UseDefaultReminders: true,
			Status: fields.status, CancelledAt: fields.cancelledAt, SourceKind: source.Kind, SourceID: source.ID, ExternalUid: &uid,
		}
		if item.Reminders != nil {
			own, whole, err := item.Reminders.encode()
			if err != nil {
				return err
			}
			params.Reminders, params.UseDefaultReminders, params.SourceReminders = own, item.Reminders.UseDefault, whole
		}
		id, err := queries.InsertImportedEvent(ctx, params)
		if err != nil {
			return fmt.Errorf("insert imported event: %w", err)
		}
		writes := make(map[string]exceptionWrite, len(item.Exceptions))
		for key, exception := range item.Exceptions {
			writes[key] = exceptionWrite{exception: exception}
		}
		if err := writeExceptions(ctx, queries, id, writes, nil, now); err != nil {
			return err
		}
	}
	for _, change := range changes.updates {
		fields := importedFields(change.item, change.cancelledAt, now)
		params := store.UpdateImportedEventParams{
			ID: change.id, SourceID: source.ID, Title: fields.title, Description: change.item.Description, Location: change.item.Location,
			AllDay: change.item.AllDay, StartAt: fields.startAt, EndAt: fields.endAt, StartDate: fields.startDate, EndDate: fields.endDate,
			TimeZone: change.item.TimeZone, Status: fields.status, CancelledAt: fields.cancelledAt,
			RecurrenceFrequency: fields.rule.frequency, RecurrenceInterval: fields.rule.interval, RecurrenceUntil: fields.rule.until,
			RecurrenceByWeekday: fields.rule.byWeekday, RecurrenceCount: fields.rule.count,
		}
		if change.item.Reminders != nil {
			own, whole, err := change.item.Reminders.encode()
			if err != nil {
				return err
			}
			params.SourceReminders = whole
			if change.ownReminders {
				useDefault := change.item.Reminders.UseDefault
				params.Reminders, params.UseDefaultReminders = own, &useDefault
			}
		}
		if err := queries.UpdateImportedEvent(ctx, params); err != nil {
			return fmt.Errorf("update imported event: %w", err)
		}
		if err := writeExceptions(ctx, queries, change.id, change.exceptions, change.dropExceptions, now); err != nil {
			return err
		}
	}
	for _, id := range changes.removals {
		if err := queries.MarkImportedEventRemoved(ctx, store.MarkImportedEventRemovedParams{ID: id, SourceID: source.ID, Now: at(now)}); err != nil {
			return fmt.Errorf("mark imported event removed: %w", err)
		}
	}
	return nil
}

// writeExceptions stores a series' occurrence exceptions and deletes drops.
func writeExceptions(ctx context.Context, queries *store.Queries, id pgtype.UUID, writes map[string]exceptionWrite, drops []string, now time.Time) error {
	if len(drops) > 0 {
		if _, err := queries.DeleteEventExceptions(ctx, store.DeleteEventExceptionsParams{EventID: id, RecurrenceIds: drops}); err != nil {
			return fmt.Errorf("delete occurrence exceptions: %w", err)
		}
	}
	keys := make([]string, 0, len(writes))
	for key := range writes {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		write := writes[key]
		changes, err := write.exception.Changes.JSON()
		if err != nil {
			return err
		}
		var cancelledAt pgtype.Timestamptz
		if write.exception.Cancelled {
			cancelledAt = write.cancelledAt
			if !cancelledAt.Valid {
				cancelledAt = at(now)
			}
		}
		if _, err := queries.UpsertEventException(ctx, store.UpsertEventExceptionParams{EventID: id, RecurrenceID: key, Changes: changes, CancelledAt: cancelledAt}); err != nil {
			return fmt.Errorf("write occurrence exception: %w", err)
		}
	}
	return nil
}

type fields struct {
	title              string
	startAt, endAt     pgtype.Timestamptz
	startDate, endDate pgtype.Date
	status             string
	cancelledAt        pgtype.Timestamptz
	rule               ruleColumns
}

// ruleColumns are a series' stored rule; all null for a single event.
type ruleColumns struct {
	frequency *string
	interval  *int32
	until     pgtype.Timestamptz
	byWeekday []string
	count     *int32
}

func importedFields(item Item, keepCancelledAt pgtype.Timestamptz, now time.Time) fields {
	out := fields{title: item.Title, status: "active"}
	if item.AllDay {
		out.startDate = pgtype.Date{Time: item.StartDate, Valid: true}
		out.endDate = pgtype.Date{Time: item.EndDate, Valid: true}
	} else {
		out.startAt, out.endAt = at(item.StartAt), at(item.EndAt)
	}
	if item.Cancelled {
		out.status, out.cancelledAt = "cancelled", keepCancelledAt
		if !out.cancelledAt.Valid {
			out.cancelledAt = at(now)
		}
	}
	if rule := item.Recurrence; rule != nil {
		frequency, interval := rule.Frequency, int32(rule.Interval)
		out.rule = ruleColumns{frequency: &frequency, interval: &interval, byWeekday: rule.ByWeekday}
		if rule.Until != nil {
			out.rule.until = at(*rule.Until)
		}
		if rule.Count > 0 {
			count := int32(rule.Count)
			out.rule.count = &count
		}
	}
	return out
}

// record emits the refresh outcome to Activity, attributed to the source.
func (r *Refresher) record(ctx context.Context, source store.CalendarSource, found outcome, trigger Trigger, now time.Time) {
	id := googleuuid.UUID(source.ID.Bytes).String()
	data := map[string]any{"sourceId": id, "sourceName": source.Name, "outcome": found.kind, "trigger": "scheduled"}
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
		summary = fmt.Sprintf("Refreshing %s failed: %s", source.Name, message)
	case found.kind == OutcomeNotModified:
		summary = fmt.Sprintf("%s is unchanged", source.Name)
	default:
		counts := found.counts
		if counts == nil {
			counts = &Counts{}
		}
		data["counts"] = counts
		summary = fmt.Sprintf("Refreshed %s: %d new, %d changed, %d removed, %d restored", source.Name, counts.Inserted, counts.Updated, counts.Removed, counts.Restored)
	}
	event := activity.Event{
		ID:            activity.EventID(eventType, id, now.Format(time.RFC3339Nano)),
		Type:          eventType,
		Subject:       activity.Subject{Type: "calendar_source", ID: id},
		CorrelationID: trigger.Trace.CorrelationID,
		CausationID:   trigger.Trace.CausationID,
		Actor:         SourceActor(source.Name),
		Outcome:       outcomeKind,
		Summary:       activity.Preview(summary, 500),
		Data:          data,
		OccurredAt:    now,
	}
	r.emitter.Emit(ctx, event)
}

// SourceActor is the system actor a source's writes are attributed to, such
// as system:ics-zaw for a source named "ZAW".
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

func at(value time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: value, Valid: true} }

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
