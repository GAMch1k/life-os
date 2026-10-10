package api

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/notify"
	store "github.com/GAMch1k/life-os/services/inbox/internal/repository/generated"
)

const (
	humanReviewCategory = "inbox.human_review"
	failedCategory      = "inbox.failed"
	notificationFailed  = "inbox.notification.failed"
)

// Notifier tells the owner when a capture needs them: when it waits in Human
// Review, and when automatic processing gave up on it. Inbox sends these
// because it owns those state changes, so every path into them (the
// processor, Voice captures, retries, a new review round) is covered once.
//
// A notification goes out after the change is stored, on its own goroutine,
// and is best effort: Inbox answers its caller without waiting for it, so a
// slow or unavailable Notifications service never delays or fails an Inbox
// operation. (The processor gives each outcome write 10 s, and the
// synchronous Activity emission can already take most of that.) A
// notification that cannot be sent is logged and recorded as an
// inbox.notification.failed activity event in the item's workflow.
//
// A nil Notifier, or one whose client has no URL, sends nothing.
type Notifier struct {
	client   *notify.Client
	activity *activity.Emitter
	logger   *slog.Logger
	sending  sync.WaitGroup
}

// NewNotifier sends through client and records failures through emitter.
func NewNotifier(client *notify.Client, emitter *activity.Emitter, logger *slog.Logger) *Notifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &Notifier{client: client, activity: emitter, logger: logger}
}

// Wait returns once every notification being sent is done (each is bounded
// by the client's timeout), or when ctx ends. It runs at shutdown, after the
// server has drained its requests, so nothing is lost on a restart.
func (n *Notifier) Wait(ctx context.Context) error {
	if n == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		n.sending.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("inbox notifications still sending: %w", ctx.Err())
	}
}

// send notifies about item in the background, within the item's workflow:
// correlated with the item and caused by the Inbox event that recorded the
// change. It never reports an error; the Inbox change already happened.
func (n *Notifier) send(item store.InboxItem, cause googleuuid.UUID, notification notify.Notification) {
	if n == nil || !n.client.Enabled() {
		return
	}
	// The request's context ends with the request (and Fiber reuses it), so
	// the goroutine carries only the trace.
	trace := activity.Trace{CorrelationID: correlationID(item), CausationID: &cause}
	itemID := uuid(item.ID).String()
	n.sending.Add(1)
	go func() {
		defer n.sending.Done()
		ctx := activity.WithTrace(context.Background(), trace)
		err := n.client.Send(ctx, notification)
		if err == nil {
			return
		}
		n.logger.Warn("inbox notification not sent", "category", notification.Category, "dedupeKey", notification.DedupeKey, "inboxItemId", itemID, "error", err)
		n.activity.Emit(ctx, activity.Event{
			ID:            activity.EventID(notificationFailed, notification.DedupeKey),
			Type:          notificationFailed,
			Subject:       activity.Subject{Type: "inbox_item", ID: itemID},
			CorrelationID: trace.CorrelationID,
			CausationID:   &cause,
			Outcome:       activity.Failed,
			Summary:       "Notification not sent: " + notification.Title,
			Data:          map[string]any{"category": notification.Category, "dedupeKey": notification.DedupeKey, "error": activity.Preview(err.Error(), 500)},
		})
	}()
}

// humanReviewNotification asks the owner to decide about a capture. The
// dedupe key names the review round (the attempt that asked), so a repeated
// request notifies once while a later round of the same item notifies again.
func humanReviewNotification(item store.InboxItem, attemptID, reason string) notify.Notification {
	return notify.Notification{
		Title:      "Needs your review",
		Body:       notificationBody(item.Content, reason),
		Importance: notify.Medium,
		Category:   humanReviewCategory,
		Link:       "/inbox#human-review",
		DedupeKey:  humanReviewCategory + ":" + uuid(item.ID).String() + ":" + attemptID,
		OccurredAt: item.UpdatedAt.Time,
	}
}

// failedNotification says automatic processing gave up on a capture.
func failedNotification(item store.InboxItem, attemptID, reason string) notify.Notification {
	return notify.Notification{
		Title:      "A capture failed",
		Body:       notificationBody(item.Content, reason),
		Importance: notify.Medium,
		Category:   failedCategory,
		Link:       "/inbox#failed",
		DedupeKey:  failedCategory + ":" + uuid(item.ID).String() + ":" + attemptID,
		OccurredAt: item.UpdatedAt.Time,
	}
}

// notificationBody is the capture shortened to one line, with the reason on
// the next; well within the 2000 characters Notifications accepts.
func notificationBody(content, reason string) string {
	lines := make([]string, 0, 2)
	for _, line := range []string{activity.Preview(content, 200), activity.Preview(reason, 500)} {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
