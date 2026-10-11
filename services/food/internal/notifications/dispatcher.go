package notifications

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/notify"
	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

// Dispatcher bounds.
const (
	// maxCatchUp bounds how far back a run looks after a long outage:
	// reminders due before it are not examined (and not recorded as missed
	// one by one).
	maxCatchUp = 7 * 24 * time.Hour
	// logRetention keeps handled reminders well past maxCatchUp.
	logRetention = 30 * 24 * time.Hour
	pruneEvery   = time.Hour
	maxErrorText = 500
)

// missedType records a reminder that was due while Food was down and is
// past the grace window.
const missedType = "food.notification.missed"

// Notifier sends notifications (*notify.Client).
type Notifier interface {
	Enabled() bool
	Send(ctx context.Context, n notify.Notification) error
}

// Config configures a Dispatcher.
type Config struct {
	// Interval is how often due reminders are looked for (default 30 s).
	Interval time.Duration
	// Grace is how late a reminder may still be sent after Food was down
	// (default 2 h); older ones are recorded as missed.
	Grace time.Duration
	// Owner is the owner's timezone (LIFE_OS_TIMEZONE).
	Owner *time.Location
	// ExpiryTime is when the daily expiry reminder is due on the owner's
	// wall clock (DefaultExpiryTime when zero).
	ExpiryTime Clock
}

// Dispatcher sends the owner's expiry reminders. Every Interval it works out
// which reminders came due since the last run (the cursor, kept in
// food_notification_dispatch) up to now, and sends each one not yet handled
// (food_notification_log) through Notifications, whose dedupe key makes a
// repeated send harmless. One process runs at a time (a lease on the state
// row); a send that fails for now holds the cursor back so it is retried.
type Dispatcher struct {
	queries   *store.Queries
	notifier  Notifier
	activity  *activity.Emitter
	owner     *time.Location
	expiry    Clock
	interval  time.Duration
	grace     time.Duration
	lateAfter time.Duration
	lease     time.Duration
	now       func() time.Time
	id        string
	logger    *slog.Logger
	lastPrune time.Time
}

// New returns a Dispatcher over pool. emitter (Activity, for missed
// reminders) may be nil; a notifier that is not Enabled leaves expiry
// reminders off.
func New(pool *pgxpool.Pool, notifier Notifier, emitter *activity.Emitter, config Config, logger *slog.Logger) *Dispatcher {
	if config.Interval <= 0 {
		config.Interval = 30 * time.Second
	}
	if config.Grace <= 0 {
		config.Grace = 2 * time.Hour
	}
	if config.Owner == nil {
		config.Owner = time.UTC
	}
	if config.ExpiryTime == (Clock{}) {
		config.ExpiryTime = DefaultExpiryTime
	}
	if logger == nil {
		logger = slog.Default()
	}
	d := &Dispatcher{
		notifier: notifier, activity: emitter, owner: config.Owner, expiry: config.ExpiryTime,
		interval: config.Interval, grace: config.Grace,
		// A reminder sent more than two intervals after it was due was not
		// sent by the regular schedule.
		lateAfter: max(2*config.Interval, time.Minute),
		lease:     max(2*time.Minute, 4*config.Interval),
		now:       time.Now, id: instanceID(), logger: logger,
	}
	if pool != nil {
		d.queries = store.New(pool)
	}
	return d
}

// WithClock replaces the clock (for tests).
func (d *Dispatcher) WithClock(now func() time.Time) *Dispatcher {
	d.now = now
	return d
}

// Enabled reports whether reminders are sent: Notifications is configured.
func (d *Dispatcher) Enabled() bool {
	return d.notifier != nil && d.notifier.Enabled()
}

// Run sends due reminders every Interval until ctx ends, starting with any
// that came due while Food was down. It returns at once when expiry
// reminders are off.
func (d *Dispatcher) Run(ctx context.Context) {
	if !d.Enabled() {
		d.logger.Warn("expiry reminders are off: NOTIFICATIONS_SERVICE_URL is not set")
		return
	}
	d.logger.Info("expiry reminders started", "interval", d.interval.String(), "grace", d.grace.String(),
		"timeZone", d.owner.String(), "expiryTime", d.expiry.String())
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		if result, err := d.RunOnce(ctx); err != nil && ctx.Err() == nil {
			d.logger.Warn("expiry reminder run failed", "error", err)
		} else if result.Sent+result.Missed+result.Failed > 0 {
			d.logger.Info("expiry reminders handled", "sent", result.Sent, "late", result.Late, "missed", result.Missed, "failed", result.Failed, "empty", result.Empty)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Result counts what one run did.
type Result struct {
	// Busy: another process holds the lease; nothing was done.
	Busy bool
	// Sent reminders, of them Late; Missed (recorded in Activity); Failed
	// (refused by Notifications); Empty (nothing expired soon, not sent);
	// Handled: already handled before.
	Sent, Late, Missed, Failed, Empty, Handled int
	// From and Through are the run's window: reminders due in
	// (From, Through] were considered.
	From, Through time.Time
}

// RunOnce handles the reminders due since the last run. The first run ever
// only starts the cursor: nothing before it is sent.
func (d *Dispatcher) RunOnce(ctx context.Context) (Result, error) {
	now := d.now().UTC().Truncate(time.Microsecond)
	state, err := d.queries.ClaimNotificationRun(ctx, store.ClaimNotificationRunParams{Owner: d.id, LeaseUntil: at(now.Add(d.lease)), Now: at(now)})
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{Busy: true}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("claim the notification run: %w", err)
	}
	if !state.LastRunAt.Valid {
		d.logger.Info("expiry reminders start now; earlier ones are not sent", "from", now)
		return Result{From: now, Through: now}, d.finish(ctx, now, now, nil, false)
	}
	cursor := state.LastRunAt.Time.UTC()
	if !now.After(cursor) {
		// This clock is behind the last run's: nothing is due yet.
		return Result{From: cursor, Through: cursor}, d.finish(ctx, now, cursor, nil, false)
	}
	from := cursor
	if floor := now.Add(-maxCatchUp); from.Before(floor) {
		d.logger.Warn("expiry reminders due before the catch-up limit are not examined", "since", cursor, "until", floor)
		from = floor
	}
	result := Result{From: from, Through: now}
	next, sent, runErr := d.handle(ctx, now, from, &result)
	if err := d.finish(ctx, now, next, runErr, sent); err != nil {
		return result, err
	}
	d.prune(ctx, now)
	return result, runErr
}

// handle sends or records every reminder due in (from, now] not handled
// yet, in due order, and returns the cursor to keep: now, or just before a
// reminder whose send failed for now (stopping there, to retry it next run),
// and whether anything was sent.
func (d *Dispatcher) handle(ctx context.Context, now, from time.Time, result *Result) (time.Time, bool, error) {
	due := dueBetween(d.expiry, d.owner, from, now)
	if len(due) == 0 {
		return now, false, nil
	}
	keys := make([]string, 0, len(due))
	for _, item := range due {
		keys = append(keys, item.Key)
	}
	handled, err := d.queries.ListHandledNotifications(ctx, keys)
	if err != nil {
		return from, false, fmt.Errorf("list handled notifications: %w", err)
	}
	done := make(map[string]bool, len(handled))
	for _, key := range handled {
		done[key] = true
	}
	sent := false
	var errs []error
	for _, item := range due {
		if done[item.Key] {
			result.Handled++
			continue
		}
		hold := item.At.Add(-time.Microsecond)
		missed := now.Sub(item.At) > d.grace
		late := now.Sub(item.At) > d.lateAfter
		// A day with nothing expiring sends nothing, and misses nothing
		// either: the owner lost nothing.
		message, ok, err := d.message(ctx, item, now)
		if err != nil {
			return hold, sent, errors.Join(append(errs, err)...)
		}
		if !ok {
			if _, err := d.record(ctx, item, "empty", now); err != nil {
				return hold, sent, errors.Join(append(errs, err)...)
			}
			result.Empty++
			continue
		}
		if missed {
			recorded, err := d.record(ctx, item, "missed", now)
			if err != nil {
				return hold, sent, errors.Join(append(errs, err)...)
			}
			if recorded {
				d.recordMissed(ctx, item, now)
				result.Missed++
			}
			continue
		}
		err = d.notifier.Send(ctx, message)
		if err != nil && !permanent(err) {
			// Notifications is unreachable or failing: retry from here.
			return hold, sent, errors.Join(append(errs, err)...)
		}
		outcome := "sent"
		if err != nil {
			outcome = "failed"
			errs = append(errs, err)
			d.logger.Warn("expiry reminder refused by notifications", "key", item.Key, "error", err)
		}
		if _, recordErr := d.record(ctx, item, outcome, now); recordErr != nil {
			// Sent but not recorded: the next run sends it again and
			// Notifications deduplicates it.
			return hold, sent, errors.Join(append(errs, recordErr)...)
		}
		if outcome == "failed" {
			result.Failed++
			continue
		}
		sent = true
		result.Sent++
		if late {
			result.Late++
		}
		d.logger.Info("expiry reminder sent", "key", item.Key, "dueAt", item.At, "late", late)
	}
	return now, sent, errors.Join(errs...)
}

// message builds the reminder for item from the pantry as it is now; false
// when nothing expires on its day or the next.
func (d *Dispatcher) message(ctx context.Context, item Due, now time.Time) (notify.Notification, bool, error) {
	rows, err := d.queries.ListPantryItemsExpiringBetween(ctx, store.ListPantryItemsExpiringBetweenParams{
		FromDate: pgDate(item.Day), ThroughDate: pgDate(item.Day.AddDate(0, 0, 1)),
	})
	if err != nil {
		return notify.Notification{}, false, fmt.Errorf("list expiring pantry items: %w", err)
	}
	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, Item{Name: row.Name, Normalized: row.NormalizedName, ExpiresOn: row.ExpiresOn.Time.UTC()})
	}
	message, ok := expiryMessage(item, items, now, d.owner)
	return message, ok, nil
}

// record stores a reminder's outcome; false when it was already recorded
// (by an overlapping run).
func (d *Dispatcher) record(ctx context.Context, item Due, outcome string, now time.Time) (bool, error) {
	rows, err := d.queries.RecordNotification(ctx, store.RecordNotificationParams{DedupeKey: item.Key, DueAt: at(item.At), Outcome: outcome, RecordedAt: at(now)})
	if err != nil {
		return false, fmt.Errorf("record notification: %w", err)
	}
	return rows == 1, nil
}

// recordMissed records in Activity a reminder Food could not send in time.
// Its id is derived from the dedupe key, so it is recorded once.
func (d *Dispatcher) recordMissed(ctx context.Context, item Due, now time.Time) {
	due := item.At.In(d.owner)
	zone, _ := due.Zone()
	when := fmt.Sprintf("%s, %s %s", due.Format("Mon 2 Jan 2006"), due.Format("15:04"), zone)
	d.activity.Emit(ctx, activity.Event{
		ID:      activity.EventID(missedType, item.Key),
		Type:    missedType,
		Subject: activity.Subject{Type: "food_notification", ID: item.Key},
		Outcome: activity.Failed,
		Summary: fmt.Sprintf("Expiry reminder due %s was missed: Food was not running", when),
		Data: map[string]any{"kind": "expiry", "day": item.Day.Format(time.DateOnly), "dueAt": item.At.UTC(),
			"dedupeKey": item.Key},
		OccurredAt: item.At.UTC(),
	})
	d.logger.Info("expiry reminder missed", "key", item.Key, "dueAt", item.At, "detectedAt", now)
}

// finish moves the cursor to next and releases the lease, recording the
// run's error or success. It runs even when ctx was cancelled (shutdown),
// so the lease is not left to expire.
func (d *Dispatcher) finish(ctx context.Context, now, next time.Time, runErr error, sent bool) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	params := store.FinishNotificationRunParams{LastRunAt: at(next), Now: at(now), Owner: d.id}
	if runErr != nil {
		message := clip(runErr.Error(), maxErrorText)
		params.Error = &message
	}
	if sent {
		params.SentAt = at(now)
	}
	rows, err := d.queries.FinishNotificationRun(ctx, params)
	if err != nil {
		return fmt.Errorf("finish the notification run: %w", err)
	}
	if rows == 0 {
		// The lease expired and another process took over; it handles the
		// same reminders again without repeating any.
		d.logger.Warn("expiry reminder run finished after its lease expired")
	}
	return nil
}

// prune forgets handled reminders long past any run's window, at most once
// an hour.
func (d *Dispatcher) prune(ctx context.Context, now time.Time) {
	if now.Sub(d.lastPrune) < pruneEvery {
		return
	}
	d.lastPrune = now
	if _, err := d.queries.PruneNotificationLog(ctx, at(now.Add(-logRetention))); err != nil && ctx.Err() == nil {
		d.logger.Warn("handled expiry reminders not pruned", "error", err)
	}
}

// Status is the expiry reminders' health for GET /v1/notifications/status.
func (d *Dispatcher) Status(ctx context.Context) (generated.FoodNotificationStatus, error) {
	status := generated.FoodNotificationStatus{
		Enabled: d.Enabled(), IntervalSeconds: int(d.interval / time.Second), GraceMinutes: int(d.grace / time.Minute),
		TimeZone: d.owner.String(), ExpiryTime: d.expiry.String(),
	}
	if d.queries == nil {
		return status, nil
	}
	row, err := d.queries.GetNotificationDispatch(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return generated.FoodNotificationStatus{}, err
	}
	status.HandledThrough, status.LastRunAt, status.LastSuccessAt = optional(row.LastRunAt), optional(row.LastAttemptAt), optional(row.LastSuccessAt)
	status.LastError, status.LastErrorAt, status.LastSentAt = row.LastError, optional(row.LastErrorAt), optional(row.LastSentAt)
	return status, nil
}

// permanent reports whether a send failed for good: Notifications refused
// the notification (4xx) or it was invalid. Anything else may succeed later.
func permanent(err error) bool {
	var status *notify.StatusError
	return errors.Is(err, notify.ErrInvalid) || (errors.As(err, &status) && !status.Temporary())
}

func at(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func pgDate(day time.Time) pgtype.Date { return pgtype.Date{Time: day, Valid: true} }

func optional(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time.UTC()
	return &out
}

// instanceID names this process as the lease owner.
func instanceID() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "food"
	}
	random := make([]byte, 4)
	_, _ = rand.Read(random)
	return host + "-" + hex.EncodeToString(random)
}
