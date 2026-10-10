package reminders

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/notify"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// Dispatcher bounds.
const (
	// maxCatchUp bounds how far back a run looks after a long outage:
	// reminders due before it are not examined (and not recorded as
	// missed one by one).
	maxCatchUp = 7 * 24 * time.Hour
	// logRetention keeps handled reminders well past maxCatchUp.
	logRetention = 30 * 24 * time.Hour
	pruneEvery   = time.Hour
	maxErrorText = 500
)

// Activity records a reminder that was due while Calendar was down and is
// past the grace window.
const missedType = "calendar.reminder.missed"

// Occurrences lists what reminders are computed from (api.Handler):
// every active event and occurrence starting in [from, to) with its
// effective reminders.
type Occurrences interface {
	ReminderOccurrences(ctx context.Context, from, to time.Time) ([]generated.CalendarEvent, error)
}

// Notifier sends notifications (*notify.Client).
type Notifier interface {
	Enabled() bool
	Send(ctx context.Context, n notify.Notification) error
}

// Config configures a Dispatcher.
type Config struct {
	// Interval is how often due reminders are looked for (default 30 s).
	Interval time.Duration
	// Grace is how late a reminder may still be sent after Calendar was
	// down (default 2 h); older ones are recorded as missed.
	Grace time.Duration
	// Owner is the owner's timezone (LIFE_OS_TIMEZONE).
	Owner *time.Location
}

// Dispatcher sends Calendar reminders. Every Interval it computes the
// effective reminders due since the last run (the cursor, kept in
// calendar_reminder_dispatch) up to now, over every active event and
// recurring occurrence, and sends each one not yet handled
// (calendar_reminder_log) through Notifications, whose dedupe key makes a
// repeated send harmless. One process runs at a time (a lease on the state
// row); a send that fails for now holds the cursor back so it is retried.
type Dispatcher struct {
	queries   *store.Queries
	events    Occurrences
	notifier  Notifier
	activity  *activity.Emitter
	owner     *time.Location
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
// reminders) may be nil; a notifier that is not Enabled leaves reminder
// delivery off.
func New(pool *pgxpool.Pool, events Occurrences, notifier Notifier, emitter *activity.Emitter, config Config, logger *slog.Logger) *Dispatcher {
	if config.Interval <= 0 {
		config.Interval = 30 * time.Second
	}
	if config.Grace <= 0 {
		config.Grace = 2 * time.Hour
	}
	if config.Owner == nil {
		config.Owner = time.UTC
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{
		queries: store.New(pool), events: events, notifier: notifier, activity: emitter, owner: config.Owner,
		interval: config.Interval, grace: config.Grace,
		// A reminder sent more than two intervals after it was due was not
		// sent by the regular schedule.
		lateAfter: max(2*config.Interval, time.Minute),
		lease:     max(2*time.Minute, 4*config.Interval),
		now:       time.Now, id: instanceID(), logger: logger,
	}
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
// that came due while Calendar was down. It returns at once when reminder
// delivery is off.
func (d *Dispatcher) Run(ctx context.Context) {
	if !d.Enabled() {
		d.logger.Warn("calendar reminder delivery is off: NOTIFICATIONS_SERVICE_URL is not set")
		return
	}
	d.logger.Info("calendar reminder delivery started", "interval", d.interval.String(), "grace", d.grace.String())
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		if result, err := d.RunOnce(ctx); err != nil && ctx.Err() == nil {
			d.logger.Warn("calendar reminder run failed", "error", err)
		} else if result.Sent+result.Missed+result.Failed > 0 {
			d.logger.Info("calendar reminders handled", "sent", result.Sent, "late", result.Late, "missed", result.Missed, "failed", result.Failed)
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
	// (refused by Notifications); Handled: already handled before.
	Sent, Late, Missed, Failed, Handled int
	// From and Through are the run's window: reminders due in
	// (From, Through] were considered.
	From, Through time.Time
}

// RunOnce handles the reminders due since the last run. The first run ever
// only starts the cursor: nothing before it is sent.
func (d *Dispatcher) RunOnce(ctx context.Context) (Result, error) {
	now := d.now().UTC().Truncate(time.Microsecond)
	state, err := d.queries.ClaimReminderRun(ctx, store.ClaimReminderRunParams{Owner: d.id, LeaseUntil: at(now.Add(d.lease)), Now: at(now)})
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{Busy: true}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("claim the reminder run: %w", err)
	}
	if !state.LastRunAt.Valid {
		d.logger.Info("calendar reminder delivery starts now; earlier reminders are not sent", "from", now)
		return Result{From: now, Through: now}, d.finish(ctx, now, now, nil, false)
	}
	cursor := state.LastRunAt.Time.UTC()
	if !now.After(cursor) {
		// This clock is behind the last run's: nothing is due yet.
		return Result{From: cursor, Through: cursor}, d.finish(ctx, now, cursor, nil, false)
	}
	from := cursor
	if floor := now.Add(-maxCatchUp); from.Before(floor) {
		d.logger.Warn("calendar reminders due before the catch-up limit are not examined", "since", cursor, "until", floor)
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
// reminder whose send failed for now (stopping there, to retry it next
// run), and whether anything was sent.
func (d *Dispatcher) handle(ctx context.Context, now, from time.Time, result *Result) (time.Time, bool, error) {
	items, err := d.events.ReminderOccurrences(ctx, from.Add(-scanBefore), now.Add(scanAhead))
	if err != nil {
		return from, false, fmt.Errorf("list occurrences: %w", err)
	}
	presets, err := d.presetSources(ctx)
	if err != nil {
		return from, false, err
	}
	due := dueBetween(items, d.owner, from, now)
	if len(due) == 0 {
		return now, false, nil
	}
	keys := make([]string, 0, len(due))
	for _, reminder := range due {
		keys = append(keys, reminder.Key)
	}
	handled, err := d.queries.ListHandledReminders(ctx, keys)
	if err != nil {
		return from, false, fmt.Errorf("list handled reminders: %w", err)
	}
	done := make(map[string]bool, len(handled))
	for _, key := range handled {
		done[key] = true
	}
	sent := false
	var errs []error
	for _, reminder := range due {
		if done[reminder.Key] {
			result.Handled++
			continue
		}
		hold := reminder.At.Add(-time.Microsecond)
		if now.Sub(reminder.At) > d.grace {
			recorded, err := d.record(ctx, reminder, "missed", now)
			if err != nil {
				return hold, sent, errors.Join(append(errs, err)...)
			}
			if recorded {
				d.recordMissed(ctx, reminder, now)
				result.Missed++
			}
			continue
		}
		late := now.Sub(reminder.At) > d.lateAfter
		showLocation := reminder.Event.Source.SourceId == nil || !presets[*reminder.Event.Source.SourceId]
		err := d.notifier.Send(ctx, notification(reminder, now, late, showLocation, d.owner))
		if err != nil && !permanent(err) {
			// Notifications is unreachable or failing: retry from here.
			return hold, sent, errors.Join(append(errs, err)...)
		}
		outcome := "sent"
		if err != nil {
			outcome = "failed"
			errs = append(errs, err)
			d.logger.Warn("calendar reminder refused by notifications", "event", reminder.Event.Id, "key", reminder.Key, "error", err)
		}
		if _, recordErr := d.record(ctx, reminder, outcome, now); recordErr != nil {
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
		d.logger.Info("calendar reminder sent", "event", reminder.Event.Id, "key", reminder.Key, "dueAt", reminder.At, "late", late)
	}
	return now, sent, errors.Join(errs...)
}

// presetSources are the sources Calendar configures itself (ZAW). Their
// events' location is the owner's own street, so reminders leave it out.
func (d *Dispatcher) presetSources(ctx context.Context) (map[googleuuid.UUID]bool, error) {
	ids, err := d.queries.ListPresetSourceIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list preset sources: %w", err)
	}
	out := make(map[googleuuid.UUID]bool, len(ids))
	for _, id := range ids {
		out[googleuuid.UUID(id.Bytes)] = true
	}
	return out, nil
}

// record stores a reminder's outcome; false when it was already recorded
// (by an overlapping run).
func (d *Dispatcher) record(ctx context.Context, reminder Due, outcome string, now time.Time) (bool, error) {
	rows, err := d.queries.RecordReminder(ctx, store.RecordReminderParams{DedupeKey: reminder.Key, DueAt: at(reminder.At), Outcome: outcome, RecordedAt: at(now)})
	if err != nil {
		return false, fmt.Errorf("record reminder: %w", err)
	}
	return rows == 1, nil
}

// recordMissed records in Activity a reminder Calendar could not send in
// time. Its id is derived from the dedupe key, so it is recorded once.
func (d *Dispatcher) recordMissed(ctx context.Context, reminder Due, now time.Time) {
	item := reminder.Event
	data := map[string]any{
		"title": item.Title, "dueAt": reminder.At.UTC(), "allDay": item.AllDay, "reminder": reminderData(reminder.Reminder),
		"dedupeKey": reminder.Key, "sourceKind": item.Source.Kind,
	}
	if item.AllDay {
		data["startDate"] = reminder.Start.In(d.owner).Format(time.DateOnly)
	} else {
		data["startAt"] = reminder.Start.UTC()
	}
	if item.Occurrence != nil {
		data["recurrenceId"] = item.Occurrence.RecurrenceId
	}
	if item.Source.SourceId != nil {
		data["sourceId"] = item.Source.SourceId.String()
	}
	due := reminder.At.In(d.owner)
	summary := fmt.Sprintf("Reminder for “%s” due %s, %s %s was missed: Calendar was not running",
		activity.Preview(item.Title, 120), due.Format("Mon 2 Jan 2006"), due.Format("15:04"), zoneName(due))
	d.activity.Emit(ctx, activity.Event{
		ID:         activity.EventID(missedType, reminder.Key),
		Type:       missedType,
		Subject:    activity.Subject{Type: "calendar_event", ID: item.Id.String()},
		Outcome:    activity.Failed,
		Summary:    activity.Preview(summary, 500),
		Data:       data,
		OccurredAt: reminder.At.UTC(),
	})
	d.logger.Info("calendar reminder missed", "event", item.Id, "key", reminder.Key, "dueAt", reminder.At, "detectedAt", now)
}

func reminderData(reminder generated.Reminder) map[string]any {
	data := map[string]any{"kind": reminder.Kind}
	if reminder.MinutesBefore != nil {
		data["minutesBefore"] = *reminder.MinutesBefore
	}
	if reminder.DaysBefore != nil {
		data["daysBefore"] = *reminder.DaysBefore
	}
	if reminder.LocalTime != nil {
		data["localTime"] = *reminder.LocalTime
	}
	return data
}

// zoneName is the zone abbreviation of t, such as CEST.
func zoneName(t time.Time) string {
	name, _ := t.Zone()
	return name
}

// finish moves the cursor to next and releases the lease, recording the
// run's error or success. It runs even when ctx was cancelled (shutdown),
// so the lease is not left to expire.
func (d *Dispatcher) finish(ctx context.Context, now, next time.Time, runErr error, sent bool) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	params := store.FinishReminderRunParams{LastRunAt: at(next), Now: at(now), Owner: d.id}
	if runErr != nil {
		message := clip(runErr.Error(), maxErrorText)
		params.Error = &message
	}
	if sent {
		params.SentAt = at(now)
	}
	rows, err := d.queries.FinishReminderRun(ctx, params)
	if err != nil {
		return fmt.Errorf("finish the reminder run: %w", err)
	}
	if rows == 0 {
		// The lease expired and another process took over; it handles the
		// same reminders again without repeating any.
		d.logger.Warn("calendar reminder run finished after its lease expired")
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
	if _, err := d.queries.PruneReminderLog(ctx, at(now.Add(-logRetention))); err != nil && ctx.Err() == nil {
		d.logger.Warn("handled calendar reminders not pruned", "error", err)
	}
}

// Status is reminder delivery's health for GET /v1/reminders/status.
func (d *Dispatcher) Status(ctx context.Context) (generated.ReminderDeliveryStatus, error) {
	status := generated.ReminderDeliveryStatus{
		Enabled: d.Enabled(), IntervalSeconds: int(d.interval / time.Second), GraceMinutes: int(d.grace / time.Minute),
	}
	row, err := d.queries.GetReminderDispatch(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return generated.ReminderDeliveryStatus{}, err
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
		host = "calendar"
	}
	random := make([]byte, 4)
	_, _ = rand.Read(random)
	return host + "-" + hex.EncodeToString(random)
}
