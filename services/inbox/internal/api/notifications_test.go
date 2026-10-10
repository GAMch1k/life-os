package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/notify"
	store "github.com/GAMch1k/life-os/services/inbox/internal/repository/generated"
)

// sent is one request a fake Notifications or Activity service received.
type sent struct {
	header http.Header
	body   map[string]any
}

// fakeService records the JSON requests it receives. Until release is
// closed (when set) it holds every request, like a slow service; status
// answers them (201 when zero).
type fakeService struct {
	mu       sync.Mutex
	requests []sent
	status   int
	release  chan struct{}
	url      string
}

func newFakeService(t *testing.T, configure func(*fakeService)) *fakeService {
	t.Helper()
	fake := &fakeService{}
	if configure != nil {
		configure(fake)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fake.release != nil {
			select {
			case <-fake.release:
			case <-r.Context().Done():
				return
			}
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		fake.mu.Lock()
		fake.requests = append(fake.requests, sent{header: r.Header.Clone(), body: body})
		fake.mu.Unlock()
		status := fake.status
		if status == 0 {
			status = http.StatusCreated
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"code":"bad_request","message":"rejected"}}`))
	}))
	t.Cleanup(server.Close)
	fake.url = server.URL
	return fake
}

func (f *fakeService) received() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sent(nil), f.requests...)
}

// ofType returns the activity events of one type.
func (f *fakeService) ofType(eventType string) []sent {
	var events []sent
	for _, request := range f.received() {
		if request.body["type"] == eventType {
			events = append(events, request)
		}
	}
	return events
}

func waitForNotifications(t *testing.T, notifier *Notifier) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := notifier.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func testItem(content string) store.InboxItem {
	return store.InboxItem{
		ID:        pgtype.UUID{Bytes: googleuuid.MustParse("0f3c2a10-7d4e-4b8a-9c61-2e5f8a9b1c0d"), Valid: true},
		Content:   content,
		State:     "human_review",
		UpdatedAt: pgtype.Timestamptz{Time: time.Date(2026, 10, 10, 8, 15, 0, 0, time.UTC), Valid: true},
	}
}

const testAttempt = "5b8e1f2a-3c4d-4e5f-8a9b-0c1d2e3f4a5b"

func TestHumanReviewNotificationNamesTheItemAndTheRound(t *testing.T) {
	item := testItem("Call   the\ndentist about " + strings.Repeat("the crown ", 40))
	got := humanReviewNotification(item, testAttempt, "Two tasks match\n“dentist”.")
	if got.Title != "Needs your review" || got.Importance != notify.Medium || got.Category != "inbox.human_review" || got.Link != "/inbox#human-review" {
		t.Fatalf("notification = %+v", got)
	}
	if want := "inbox.human_review:0f3c2a10-7d4e-4b8a-9c61-2e5f8a9b1c0d:" + testAttempt; got.DedupeKey != want {
		t.Fatalf("dedupe key = %q, want %q", got.DedupeKey, want)
	}
	if !got.OccurredAt.Equal(item.UpdatedAt.Time) {
		t.Fatalf("occurredAt = %v, want the state change %v", got.OccurredAt, item.UpdatedAt.Time)
	}
	// The capture on one shortened line, the reason on the next.
	lines := strings.Split(got.Body, "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "Call the dentist about the crown") || !strings.HasSuffix(lines[0], "…") || len([]rune(lines[0])) != 200 {
		t.Fatalf("body = %q", got.Body)
	}
	if lines[1] != "Two tasks match “dentist”." {
		t.Fatalf("reason line = %q", lines[1])
	}
	// Another round of the same item is another notification.
	if again := humanReviewNotification(item, "6c9f2a3b-4d5e-4f60-9b0c-1d2e3f4a5b6c", "x"); again.DedupeKey == got.DedupeKey {
		t.Fatal("a new review round must not reuse the dedupe key")
	}
}

func TestFailedNotificationSaysWhy(t *testing.T) {
	got := failedNotification(testItem("Pay the rent"), testAttempt, "gave up after 3 attempts: unusable model output")
	want := notify.Notification{
		Title:      "A capture failed",
		Body:       "Pay the rent\ngave up after 3 attempts: unusable model output",
		Importance: notify.Medium,
		Category:   "inbox.failed",
		Link:       "/inbox#failed",
		DedupeKey:  "inbox.failed:0f3c2a10-7d4e-4b8a-9c61-2e5f8a9b1c0d:" + testAttempt,
		OccurredAt: time.Date(2026, 10, 10, 8, 15, 0, 0, time.UTC),
	}
	if got != want {
		t.Fatalf("notification = %+v\nwant %+v", got, want)
	}
	if body := notificationBody("Pay the rent", ""); body != "Pay the rent" {
		t.Fatalf("body without a reason = %q", body)
	}
}

func TestNotifierSendsAsInboxInTheItemsWorkflow(t *testing.T) {
	notifications := newFakeService(t, nil)
	notifier := NewNotifier(notify.New(notifications.url, "inbox"), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	correlation := "voice-upload-42"
	item := testItem("Buy milk")
	item.CorrelationID = &correlation
	cause := activity.EventID("inbox.human_review.requested", testAttempt)

	notifier.send(item, cause, humanReviewNotification(item, testAttempt, "No task matches."))
	waitForNotifications(t, notifier)

	requests := notifications.received()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	got := requests[0]
	if got.header.Get(activity.ActorHeader) != "system:inbox" || got.header.Get(activity.CorrelationIDHeader) != correlation || got.header.Get(activity.CausationIDHeader) != cause.String() {
		t.Fatalf("headers = %v", got.header)
	}
	want := map[string]any{
		"title":      "Needs your review",
		"body":       "Buy milk\nNo task matches.",
		"importance": "medium",
		"category":   "inbox.human_review",
		"link":       "/inbox#human-review",
		"dedupeKey":  "inbox.human_review:0f3c2a10-7d4e-4b8a-9c61-2e5f8a9b1c0d:" + testAttempt,
		"occurredAt": "2026-10-10T08:15:00Z",
	}
	for key, value := range want {
		if got.body[key] != value {
			t.Errorf("%s = %v, want %v", key, got.body[key], value)
		}
	}
}

func TestNotifierLogsAndRecordsAFailedSend(t *testing.T) {
	notifications := newFakeService(t, func(f *fakeService) { f.status = http.StatusBadRequest })
	events := newFakeService(t, nil)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	notifier := NewNotifier(notify.New(notifications.url, "inbox"), activity.NewEmitter(events.url, "inbox", logger), logger)
	item := testItem("Pay the rent")
	cause := activity.EventID("inbox.processing.failed", testAttempt)
	notification := failedNotification(item, testAttempt, "gave up")

	notifier.send(item, cause, notification)
	waitForNotifications(t, notifier)

	if !strings.Contains(logs.String(), "inbox notification not sent") || !strings.Contains(logs.String(), notification.DedupeKey) {
		t.Fatalf("log = %q", logs.String())
	}
	recorded := events.ofType("inbox.notification.failed")
	if len(recorded) != 1 {
		t.Fatalf("inbox.notification.failed events = %d, want 1", len(recorded))
	}
	event := recorded[0].body
	subject, _ := event["subject"].(map[string]any)
	data, _ := event["data"].(map[string]any)
	actor, _ := event["actor"].(map[string]any)
	if event["outcome"] != "failed" || subject["id"] != "0f3c2a10-7d4e-4b8a-9c61-2e5f8a9b1c0d" || event["correlationId"] != "0f3c2a10-7d4e-4b8a-9c61-2e5f8a9b1c0d" || event["causationId"] != cause.String() {
		t.Fatalf("event = %v", event)
	}
	if actor["kind"] != "system" || actor["name"] != "inbox" || data["category"] != "inbox.failed" || data["dedupeKey"] != notification.DedupeKey || !strings.Contains(data["error"].(string), "400") {
		t.Fatalf("event actor = %v, data = %v", actor, data)
	}
}

func TestNotifierIsBoundedWhenNotificationsIsSlow(t *testing.T) {
	notifications := newFakeService(t, func(f *fakeService) { f.release = make(chan struct{}) })
	events := newFakeService(t, nil)
	client := notify.New(notifications.url, "inbox")
	client.Timeout = 300 * time.Millisecond
	notifier := NewNotifier(client, activity.NewEmitter(events.url, "inbox", nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	item := testItem("Buy milk")

	started := time.Now()
	notifier.send(item, activity.EventID("inbox.human_review.requested", testAttempt), humanReviewNotification(item, testAttempt, "why"))
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("send took %v; it must not wait for Notifications", elapsed)
	}
	// Still sending: Wait gives up with its context.
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := notifier.Wait(short); err == nil {
		t.Fatal("Wait returned before the send finished")
	}
	// The client's timeout ends the send, which is then recorded as failed.
	waitForNotifications(t, notifier)
	close(notifications.release)
	if len(events.ofType("inbox.notification.failed")) != 1 {
		t.Fatal("a send that timed out must be recorded as failed")
	}
}

func TestNotifierWithoutURLSendsNothing(t *testing.T) {
	events := newFakeService(t, nil)
	notifier := NewNotifier(notify.New("", "inbox"), activity.NewEmitter(events.url, "inbox", nil), nil)
	item := testItem("Buy milk")
	notifier.send(item, activity.EventID("inbox.human_review.requested", testAttempt), humanReviewNotification(item, testAttempt, "why"))
	waitForNotifications(t, notifier)
	if got := events.received(); len(got) != 0 {
		t.Fatalf("activity events = %d, want none", len(got))
	}

	var none *Notifier
	none.send(item, googleuuid.New(), humanReviewNotification(item, testAttempt, "why"))
	if err := none.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
