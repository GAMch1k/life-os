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
	"github.com/GAMch1k/life-os/services/tasks/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/tasks/internal/repository/generated"
)

// Dispatcher bounds.
const (
	// maxCatchUp bounds how far back a run looks after a long outage:
	// notifications due before it are not examined (and not recorded as
	// missed one by one).
	maxCatchUp = 7 * 24 * time.Hour
	// logRetention keeps handled notifications well past maxCatchUp.
	logRetention = 30 * 24 * time.Hour
	pruneEvery   = time.Hour
	maxErrorText = 500
)

// Activity records a notification that was due while Tasks was down and is
// past the grace window.
const missedType = "tasks.notification.missed"

// Notifier sends notifications (*notify.Client).
type Notifier interface {
	Enabled() bool
	Send(ctx context.Context, n notify.Notification) error
}

// Config configures a Dispatcher.
type Config struct {
	// Interval is how often due notifications are looked for (default 30 s).
	Interval time.Duration
	// Grace is how late a notification may still be sent after Tasks was
	// down (default 2 h); older ones are recorded as missed.
	Grace time.Duration
	// Owner is the owner's timezone (LIFE_OS_TIMEZONE).
	Owner *time.Location
	// Schedule is when the messages are due (DefaultSchedule when zero).
	Schedule Schedule
}

// Dispatcher sends the owner's Task notifications. Every Interval it works
// out what came due since the last run (the cursor, kept in
// task_notification_dispatch) up to now: the reminders of open timed Tasks
// and the daily summary, morning, and evening messages; and sends each one
// not yet handled (task_notification_log) through Notifications, whose
// dedupe key makes a repeated send harmless. One process runs at a time (a
// lease on the state row); a send that fails for now holds the cursor back
// so it is retried.
type Dispatcher struct {
	queries   *store.Queries
	notifier  Notifier
	events    Events
	activity  *activity.Emitter
	owner     *time.Location
	schedule  Schedule
	interval  time.Duration
	grace     time.Duration
	lateAfter time.Duration
	lease     time.Duration
	now       func() time.Time
	id        string
	logger    *slog.Logger
	lastPrune time.Time
}

// New returns a Dispatcher over pool. events (Calendar, for the summary) and
// emitter (Activity, for missed notifications) may be nil; a notifier that
// is not Enabled leaves Task notifications off.
func New(pool *pgxpool.Pool, notifier Notifier, events Events, emitter *activity.Emitter, config Config, logger *slog.Logger) *Dispatcher {
	if config.Interval <= 0 {
		config.Interval = 30 * time.Second
	}
	if config.Grace <= 0 {
		config.Grace = 2 * time.Hour
	}
	if config.Owner == nil {
		config.Owner = time.UTC
	}
	if config.Schedule == (Schedule{}) {
		config.Schedule = DefaultSchedule()
	}
	if logger == nil {
		logger = slog.Default()
	}
	d := &Dispatcher{
		notifier: notifier, events: events, activity: emitter, owner: config.Owner, schedule: config.Schedule,
		interval: config.Interval, grace: config.Grace,
		// A notification sent more than two intervals after it was due was
		// not sent by the regular schedule.
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

// Enabled reports whether notifications are sent: Notifications is
// configured.
func (d *Dispatcher) Enabled() bool {
	return d.notifier != nil && d.notifier.Enabled()
}

// Run sends due notifications every Interval until ctx ends, starting with
// any that came due while Tasks was down. It returns at once when Task
// notifications are off.
func (d *Dispatcher) Run(ctx context.Context) {
	if !d.Enabled() {
		d.logger.Warn("task notifications are off: NOTIFICATIONS_SERVICE_URL is not set")
		return
	}
	if d.events == nil || !d.events.Enabled() {
		d.logger.Warn("the morning summary lists no events: CALENDAR_SERVICE_URL is not set")
	}
	d.logger.Info("task notifications started", "interval", d.interval.String(), "grace", d.grace.String(),
		"timeZone", d.owner.String(), "summary", d.schedule.Summary.String(), "morning", d.schedule.Morning.String(),
		"evening", d.schedule.Evening.String(), "timedLead", d.schedule.TimedLead.String(), "merged", d.schedule.Merged())
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		if result, err := d.RunOnce(ctx); err != nil && ctx.Err() == nil {
			d.logger.Warn("task notification run failed", "error", err)
		} else if result.Sent+result.Missed+result.Failed > 0 {
			d.logger.Info("task notifications handled", "sent", result.Sent, "late", result.Late, "missed", result.Missed, "failed", result.Failed, "empty", result.Empty)
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
	// Sent notifications, of them Late; Missed (recorded in Activity);
	// Failed (refused by Notifications); Empty (a daily message with
	// nothing to list, not sent); Handled: already handled before.
	Sent, Late, Missed, Failed, Empty, Handled int
	// CalendarUnavailable: a summary was sent without events because
	// Calendar did not answer.
	CalendarUnavailable bool
	// From and Through are the run's window: notifications due in
	// (From, Through] were considered.
	From, Through time.Time
}

// RunOnce handles the notifications due since the last run. The first run
// ever only starts the cursor: nothing before it is sent.
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
		d.logger.Info("task notifications start now; earlier ones are not sent", "from", now)
		return Result{From: now, Through: now}, d.finish(ctx, now, now, nil, false)
	}
	cursor := state.LastRunAt.Time.UTC()
	if !now.After(cursor) {
		// This clock is behind the last run's: nothing is due yet.
		return Result{From: cursor, Through: cursor}, d.finish(ctx, now, cursor, nil, false)
	}
	from := cursor
	if floor := now.Add(-maxCatchUp); from.Before(floor) {
		d.logger.Warn("task notifications due before the catch-up limit are not examined", "since", cursor, "until", floor)
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

// handle sends or records every notification due in (from, now] not handled
// yet, in due order, and returns the cursor to keep: now, or just before a
// notification whose send failed for now (stopping there, to retry it next
// run), and whether anything was sent.
func (d *Dispatcher) handle(ctx context.Context, now, from time.Time, result *Result) (time.Time, bool, error) {
	lead := d.schedule.TimedLead
	rows, err := d.queries.ListOpenTasksDueIn(ctx, store.ListOpenTasksDueInParams{After: at(from.Add(lead)), Through: at(now.Add(lead))})
	if err != nil {
		return from, false, fmt.Errorf("list tasks due soon: %w", err)
	}
	due := append(timedBetween(d.tasks(rows), lead, from, now), dailyBetween(d.schedule, d.owner, from, now)...)
	if len(due) == 0 {
		return now, false, nil
	}
	sortDue(due)
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
		// A daily message with nothing to list is not sent, and not missed
		// either: the owner lost nothing.
		message, ok, err := d.message(ctx, item, now, late, result)
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
			d.logger.Warn("task notification refused by notifications", "key", item.Key, "error", err)
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
		d.logger.Info("task notification sent", "kind", item.Kind, "key", item.Key, "dueAt", item.At, "late", late)
	}
	return now, sent, errors.Join(errs...)
}

// message builds the notification for item, reading the Tasks (and for the
// summary, the events) as they are now; false when a daily message has
// nothing to list. late phrases a timed reminder from now.
func (d *Dispatcher) message(ctx context.Context, item Due, now time.Time, late bool, result *Result) (notify.Notification, bool, error) {
	if item.Kind == KindDue {
		return dueMessage(item, now, late, d.owner), true, nil
	}
	// Everything a daily message can name is due before the day after
	// tomorrow begins.
	rows, err := d.queries.ListOpenTasksDueBefore(ctx, at(startOfDay(item.Day.AddDate(0, 0, 2), d.owner)))
	if err != nil {
		return notify.Notification{}, false, fmt.Errorf("list open tasks: %w", err)
	}
	lists := classify(d.tasks(rows), item.Day, item.At)
	switch item.Kind {
	case KindMorning:
		message, ok := morningMessage(item, lists, now, d.owner)
		return message, ok, nil
	case KindEvening:
		message, ok := eveningMessage(item, lists, now, d.owner)
		return message, ok, nil
	}
	events, state := d.dayEvents(ctx, item.Day)
	if state == EventsUnavailable {
		result.CalendarUnavailable = true
	}
	message, ok := summaryMessage(item, lists, events, state, d.schedule.Merged(), now, d.owner)
	return message, ok, nil
}

// dayEvents reads the day's events from Calendar for the summary.
func (d *Dispatcher) dayEvents(ctx context.Context, day time.Time) ([]Event, EventsState) {
	if d.events == nil || !d.events.Enabled() {
		return nil, EventsOff
	}
	events, err := d.events.EventsOn(ctx, day)
	if err != nil {
		d.logger.Warn("Calendar is unavailable: the morning summary lists no events", "day", day.Format(time.DateOnly), "error", err)
		return nil, EventsUnavailable
	}
	return events, EventsListed
}

func (d *Dispatcher) tasks(rows []store.Task) []Task {
	out := make([]Task, 0, len(rows))
	for _, row := range rows {
		if task, ok := taskFrom(row, d.owner); ok {
			out = append(out, task)
		}
	}
	return out
}

// record stores a notification's outcome; false when it was already
// recorded (by an overlapping run).
func (d *Dispatcher) record(ctx context.Context, item Due, outcome string, now time.Time) (bool, error) {
	rows, err := d.queries.RecordNotification(ctx, store.RecordNotificationParams{DedupeKey: item.Key, DueAt: at(item.At), Outcome: outcome, RecordedAt: at(now)})
	if err != nil {
		return false, fmt.Errorf("record notification: %w", err)
	}
	return rows == 1, nil
}

// recordMissed records in Activity a notification Tasks could not send in
// time. Its id is derived from the dedupe key, so it is recorded once.
func (d *Dispatcher) recordMissed(ctx context.Context, item Due, now time.Time) {
	due := item.At.In(d.owner)
	when := fmt.Sprintf("%s, %s %s", due.Format("Mon 2 Jan 2006"), due.Format("15:04"), zoneName(due))
	data := map[string]any{"kind": string(item.Kind), "dueAt": item.At.UTC(), "dedupeKey": item.Key}
	subject := activity.Subject{Type: "task_notification", ID: item.Key}
	var summary string
	switch item.Kind {
	case KindDue:
		subject = activity.Subject{Type: "task", ID: item.Task.ID}
		data["title"], data["taskDueAt"] = item.Task.Title, item.Task.Due.UTC()
		summary = fmt.Sprintf("Reminder for task “%s” due %s was missed: Tasks was not running", activity.Preview(item.Task.Title, 120), when)
	case KindMorning:
		data["day"] = item.Day.Format(time.DateOnly)
		summary = fmt.Sprintf("Morning task list due %s was missed: Tasks was not running", when)
	case KindEvening:
		data["day"] = item.Day.Format(time.DateOnly)
		summary = fmt.Sprintf("Evening task list due %s was missed: Tasks was not running", when)
	default:
		data["day"] = item.Day.Format(time.DateOnly)
		summary = fmt.Sprintf("Morning summary due %s was missed: Tasks was not running", when)
	}
	d.activity.Emit(ctx, activity.Event{
		ID:         activity.EventID(missedType, item.Key),
		Type:       missedType,
		Subject:    subject,
		Outcome:    activity.Failed,
		Summary:    activity.Preview(summary, 500),
		Data:       data,
		OccurredAt: item.At.UTC(),
	})
	d.logger.Info("task notification missed", "kind", item.Kind, "key", item.Key, "dueAt", item.At, "detectedAt", now)
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
		// same notifications again without repeating any.
		d.logger.Warn("task notification run finished after its lease expired")
	}
	return nil
}

// prune forgets handled notifications long past any run's window, at most
// once an hour.
func (d *Dispatcher) prune(ctx context.Context, now time.Time) {
	if now.Sub(d.lastPrune) < pruneEvery {
		return
	}
	d.lastPrune = now
	if _, err := d.queries.PruneNotificationLog(ctx, at(now.Add(-logRetention))); err != nil && ctx.Err() == nil {
		d.logger.Warn("handled task notifications not pruned", "error", err)
	}
}

// Status is Task notifications' health for GET /v1/notifications/status.
func (d *Dispatcher) Status(ctx context.Context) (generated.TaskNotificationStatus, error) {
	status := generated.TaskNotificationStatus{
		Enabled: d.Enabled(), CalendarConfigured: d.events != nil && d.events.Enabled(),
		IntervalSeconds: int(d.interval / time.Second), GraceMinutes: int(d.grace / time.Minute),
		TimeZone: d.owner.String(), SummaryTime: d.schedule.Summary.String(), MorningTime: d.schedule.Morning.String(),
		EveningTime: d.schedule.Evening.String(), TimedLeadMinutes: int(d.schedule.TimedLead / time.Minute),
		MergedMorning: d.schedule.Merged(),
	}
	if d.queries == nil {
		return status, nil
	}
	row, err := d.queries.GetNotificationDispatch(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return generated.TaskNotificationStatus{}, err
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
		host = "tasks"
	}
	random := make([]byte, 4)
	_, _ = rand.Read(random)
	return host + "-" + hex.EncodeToString(random)
}
