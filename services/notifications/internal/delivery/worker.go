package delivery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/diagnostics"
	store "github.com/GAMch1k/life-os/services/notifications/internal/repository/generated"
)

// Store is the persistence the worker needs; *store.Queries satisfies it.
type Store interface {
	ClaimDelivery(context.Context, store.ClaimDeliveryParams) (store.Delivery, error)
	GetNotification(context.Context, pgtype.UUID) (store.Notification, error)
	MarkDeliverySent(context.Context, store.MarkDeliverySentParams) (store.Delivery, error)
	RetryDelivery(context.Context, store.RetryDeliveryParams) (store.Delivery, error)
	FinishDelivery(context.Context, store.FinishDeliveryParams) (store.Delivery, error)
}

// Options tune a Worker.
type Options struct {
	Policy      Policy
	Poll        time.Duration // how often to look for due deliveries
	Lease       time.Duration // how long a claim is held
	SendTimeout time.Duration // bound on one Send; shorter than Lease
	// Owner identifies this worker in leases; empty picks host-pid-random.
	Owner string
	// Now is the clock for age decisions; nil uses time.Now.
	Now func() time.Time
}

// Worker delivers due deliveries one at a time. Several workers (or service
// replicas) may run against one database: a lease keeps each delivery with
// one worker, and an expired lease makes it claimable again.
type Worker struct {
	store    Store
	channels map[string]Channel
	options  Options
	activity *activity.Emitter
	logger   *slog.Logger
	wake     chan struct{}
}

// maxBatch bounds the deliveries handled per wake-up before the worker
// checks for shutdown and waits for the next tick.
const maxBatch = 50

// maxErrorLength bounds a stored error message.
const maxErrorLength = 500

func NewWorker(s Store, channels []Channel, options Options, emitter *activity.Emitter, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	if options.Owner == "" {
		options.Owner = defaultOwner()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	byName := make(map[string]Channel, len(channels))
	for _, channel := range channels {
		byName[channel.Name()] = channel
	}
	return &Worker{store: s, channels: byName, options: options, activity: emitter, logger: logger, wake: make(chan struct{}, 1)}
}

// Wake makes the worker look for due deliveries now instead of at its next
// tick. It never blocks.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run delivers until ctx is cancelled. A delivery under way finishes first.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.options.Poll)
	defer ticker.Stop()
	for {
		for range maxBatch {
			if ctx.Err() != nil {
				return
			}
			worked, err := w.ProcessOne(context.WithoutCancel(ctx))
			if err != nil {
				w.logger.Warn("notification delivery round failed", "error", err)
				break
			}
			if !worked {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.wake:
		}
	}
}

// ProcessOne claims one due delivery and attempts it. worked is false when
// nothing was due.
func (w *Worker) ProcessOne(ctx context.Context) (worked bool, err error) {
	claimed, err := w.store.ClaimDelivery(ctx, store.ClaimDeliveryParams{
		LeaseOwner:   w.options.Owner,
		LeaseSeconds: int32(max(1, w.options.Lease/time.Second)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim delivery: %w", err)
	}
	row, err := w.store.GetNotification(ctx, claimed.NotificationID)
	if err != nil {
		return true, fmt.Errorf("load notification of delivery %s: %w", uuid.UUID(claimed.ID.Bytes), err)
	}
	notification := channelView(row)

	channel, ok := w.channels[claimed.Channel]
	if !ok {
		return true, w.finish(ctx, claimed, row, StatusSkipped, fmt.Sprintf("channel %q is not configured", claimed.Channel), "channel_not_configured")
	}
	if w.options.Policy.Expired(notification.CreatedAt, w.options.Now()) {
		// This claim started no attempt.
		return true, w.finish(ctx, claimed, row, StatusFailed, expiredMessage(claimed.Attempts-1, w.options.Policy.MaxAge), "expired")
	}

	sendCtx, cancel := context.WithTimeout(ctx, w.options.SendTimeout)
	sendErr := channel.Send(sendCtx, notification)
	cancel()
	if sendErr == nil {
		sent, err := w.store.MarkDeliverySent(ctx, store.MarkDeliverySentParams{ID: claimed.ID, LeaseOwner: w.options.Owner})
		if errors.Is(err, pgx.ErrNoRows) {
			// Another worker took over after our lease expired; it may send
			// again (delivery is at least once).
			w.logger.Warn("notification sent after its lease was lost", "delivery", uuid.UUID(claimed.ID.Bytes), "channel", claimed.Channel)
			return true, nil
		}
		if err != nil {
			return true, fmt.Errorf("record sent delivery: %w", err)
		}
		w.emitDelivered(ctx, row, sent)
		return true, nil
	}

	message := boundError(sendErr)
	decision := w.options.Policy.AfterFailure(claimed.Attempts, notification.CreatedAt, w.options.Now(), sendErr)
	if decision.Status != StatusRetrying {
		if decision.Status == StatusFailed && !IsPermanent(sendErr) {
			message = expiredMessage(claimed.Attempts, w.options.Policy.MaxAge) + "; last error: " + message
		}
		return true, w.finish(ctx, claimed, row, decision.Status, message, diagnostics.ErrorCode(sendErr))
	}
	_, err = w.store.RetryDelivery(ctx, store.RetryDeliveryParams{
		ID: claimed.ID, LeaseOwner: w.options.Owner, LastError: &message,
		NextAttemptAt: pgtype.Timestamptz{Time: decision.NextAttemptAt.UTC(), Valid: true},
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return true, fmt.Errorf("schedule delivery retry: %w", err)
	}
	// The log has a classified code; the channel's own words stay in the
	// delivery's last error, for Status (docs/architecture/multi-user.md#logs).
	w.logger.Info("notification delivery will be retried", "delivery", uuid.UUID(claimed.ID.Bytes),
		"channel", claimed.Channel, "attempt", claimed.Attempts, "nextAttemptAt", decision.NextAttemptAt.UTC(),
		"error", diagnostics.ErrorCode(sendErr))
	return true, nil
}

func (w *Worker) finish(ctx context.Context, claimed store.Delivery, row store.Notification, status, message, code string) error {
	finished, err := w.store.FinishDelivery(ctx, store.FinishDeliveryParams{
		ID: claimed.ID, LeaseOwner: w.options.Owner, Status: status, LastError: &message,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("end delivery as %s: %w", status, err)
	}
	if status == StatusFailed {
		w.logger.Warn("notification delivery failed", "delivery", uuid.UUID(claimed.ID.Bytes), "channel", claimed.Channel,
			"attempts", claimed.Attempts, "error", code)
		w.emitFailed(ctx, row, finished)
	}
	return nil
}

func expiredMessage(attempts int32, maxAge time.Duration) string {
	noun := "attempts"
	if attempts == 1 {
		noun = "attempt"
	}
	return fmt.Sprintf("not delivered within the maximum age of %s (%d %s)", formatAge(maxAge), attempts, noun)
}

// formatAge writes d in its largest whole unit, such as 24h, 90m, or 45s.
func formatAge(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.Round(time.Second).String()
}

func (w *Worker) emitDelivered(ctx context.Context, row store.Notification, d store.Delivery) {
	id := uuid.UUID(row.ID.Bytes).String()
	created := activity.EventID("notification.created", id)
	w.activity.Emit(ctx, activity.Event{
		ID:            activity.EventID("notification.delivered", id, d.Channel),
		Type:          "notification.delivered",
		Source:        "notifications",
		Subject:       activity.Subject{Type: "notification", ID: id},
		CorrelationID: correlation(row),
		CausationID:   &created,
		Actor:         activity.System("notifications"),
		Outcome:       activity.Succeeded,
		Summary:       fmt.Sprintf("Notification “%s” delivered via %s", activity.Preview(row.Title, 120), d.Channel),
		Data: map[string]any{
			"channel": d.Channel, "attempts": d.Attempts,
			"importance": row.Importance, "category": row.Category, "source": row.Source, "title": row.Title,
		},
	})
}

func (w *Worker) emitFailed(ctx context.Context, row store.Notification, d store.Delivery) {
	id := uuid.UUID(row.ID.Bytes).String()
	created := activity.EventID("notification.created", id)
	lastError := ""
	if d.LastError != nil {
		lastError = *d.LastError
	}
	w.activity.Emit(ctx, activity.Event{
		ID:            activity.EventID("notification.delivery_failed", id, d.Channel),
		Type:          "notification.delivery_failed",
		Source:        "notifications",
		Subject:       activity.Subject{Type: "notification", ID: id},
		CorrelationID: correlation(row),
		CausationID:   &created,
		Actor:         activity.System("notifications"),
		Outcome:       activity.Failed,
		Summary:       fmt.Sprintf("Notification “%s” not delivered via %s", activity.Preview(row.Title, 120), d.Channel),
		Data: map[string]any{
			"channel": d.Channel, "attempts": d.Attempts, "error": lastError,
			"importance": row.Importance, "category": row.Category, "source": row.Source, "title": row.Title,
		},
	})
}

// correlation is the producer's workflow, or the notification's own id.
func correlation(row store.Notification) string {
	if row.CorrelationID != nil && *row.CorrelationID != "" {
		return *row.CorrelationID
	}
	return uuid.UUID(row.ID.Bytes).String()
}

func channelView(row store.Notification) Notification {
	n := Notification{
		ID:         uuid.UUID(row.ID.Bytes),
		Title:      row.Title,
		Body:       row.Body,
		Importance: row.Importance,
		Category:   row.Category,
		Source:     row.Source,
		OccurredAt: row.OccurredAt.Time,
		CreatedAt:  row.CreatedAt.Time,
	}
	if row.Link != nil {
		n.Link = *row.Link
	}
	return n
}

// boundError is err's message on one line, at most maxErrorLength
// characters.
func boundError(err error) string {
	message := strings.Join(strings.FieldsFunc(err.Error(), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
	if message == "" {
		message = "delivery failed"
	}
	if runes := []rune(message); len(runes) > maxErrorLength {
		message = string(runes[:maxErrorLength-1]) + "…"
	}
	return message
}

func defaultOwner() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "notifications"
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(suffix))
}
