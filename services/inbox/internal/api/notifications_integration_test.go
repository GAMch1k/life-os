package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/notify"
	store "github.com/GAMch1k/life-os/services/inbox/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/inbox/internal/testdb"
)

// inboxAPI serves the Inbox API on a fresh schema of the Postgres named by
// INBOX_TEST_DATABASE_URL, recording activity in events.
type inboxAPI struct {
	t      *testing.T
	app    *httpserver.App
	events *fakeService
}

func newInboxAPI(t *testing.T, notificationsURL string) (*inboxAPI, *Notifier) {
	t.Helper()
	pool := testdb.Open(t, "inbox_notify_test")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	events := newFakeService(t, nil)
	emitter := activity.NewEmitter(events.url, "inbox", logger)
	notifier := NewNotifier(notify.New(notificationsURL, "inbox"), emitter, logger)
	// Every send ends before the schema and the fake services go away.
	t.Cleanup(func() { waitForNotifications(t, notifier) })
	app := httpserver.New("inbox", httpserver.WithLogger(logger))
	Register(app, NewHandler(store.New(pool), emitter, notifier))
	return &inboxAPI{t: t, app: app, events: events}, notifier
}

// call makes one request as the processor and decodes the JSON answer. It
// fails the test unless the answer is a success within a second.
func (a *inboxAPI) call(method, path string, body any) map[string]any {
	a.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		a.t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	request.Header.Set("content-type", "application/json")
	request.Header.Set(activity.ActorHeader, "agent:processor")
	response, err := a.app.Test(request, fiber.TestConfig{Timeout: time.Second})
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(response.Body).Decode(&decoded)
	if response.StatusCode/100 != 2 {
		a.t.Fatalf("%s %s = %d %v", method, path, response.StatusCode, decoded)
	}
	return decoded
}

func (a *inboxAPI) capture(content string) string {
	a.t.Helper()
	return a.call(http.MethodPost, "/v1/inbox-items", map[string]any{"content": content, "source": "manual"})["id"].(string)
}

func (a *inboxAPI) startAttempt(itemID, key string) string {
	a.t.Helper()
	return a.call(http.MethodPost, "/v1/inbox-items/"+itemID+"/processing-attempts", map[string]any{"idempotencyKey": key, "agentName": "codex", "modelProfile": "test"})["id"].(string)
}

func (a *inboxAPI) requestReview(itemID, attemptID, reason string) map[string]any {
	a.t.Helper()
	return a.call(http.MethodPost, "/v1/inbox-items/"+itemID+"/human-review", map[string]any{
		"attemptId": attemptID, "proposedAction": map[string]any{"tool": "none", "arguments": map[string]any{}}, "candidates": []any{}, "errorSummary": reason,
	})
}

func (a *inboxAPI) finishAttempt(itemID, attemptID, status, reason string) {
	a.t.Helper()
	body := map[string]any{"status": status, "actions": []any{}}
	if reason != "" {
		body["errorSummary"] = reason
	}
	a.call(http.MethodPatch, "/v1/inbox-items/"+itemID+"/processing-attempts/"+attemptID, body)
}

func (a *inboxAPI) state(itemID string) string {
	a.t.Helper()
	return a.call(http.MethodGet, "/v1/inbox-items/"+itemID, nil)["state"].(string)
}

func TestHumanReviewNotifiesTheOwnerOncePerRound(t *testing.T) {
	notifications := newFakeService(t, nil)
	inbox, notifier := newInboxAPI(t, notifications.url)
	item := inbox.capture("Call the dentist\nabout the crown")
	first := inbox.startAttempt(item, "op-1")
	inbox.requestReview(item, first, "Two tasks match “dentist”.")
	// The processor repeats a request it is unsure went through.
	inbox.requestReview(item, first, "Two tasks match “dentist”.")
	waitForNotifications(t, notifier)

	sent := notifications.received()
	if len(sent) != 2 {
		t.Fatalf("notifications sent = %d, want 2 (one per request)", len(sent))
	}
	got := sent[0]
	want := map[string]any{
		"title":      "Needs your review",
		"body":       "Call the dentist about the crown\nTwo tasks match “dentist”.",
		"importance": "medium",
		"category":   "inbox.human_review",
		"link":       "/inbox#human-review",
		"dedupeKey":  "inbox.human_review:" + item + ":" + first,
	}
	for key, value := range want {
		if got.body[key] != value {
			t.Errorf("%s = %v, want %v", key, got.body[key], value)
		}
	}
	if got.header.Get(activity.ActorHeader) != "system:inbox" || got.header.Get(activity.CorrelationIDHeader) != item || got.header.Get(activity.CausationIDHeader) != activity.EventID("inbox.human_review.requested", first).String() {
		t.Fatalf("headers = %v", got.header)
	}
	// The repeat carries the same key, so Notifications stores it once.
	if sent[1].body["dedupeKey"] != want["dedupeKey"] {
		t.Fatalf("repeated request dedupe key = %v, want %v", sent[1].body["dedupeKey"], want["dedupeKey"])
	}

	// After the owner's decision a retry asks again: a new round, a new
	// notification.
	second := inbox.startAttempt(item, "op-1.retry.2")
	inbox.requestReview(item, second, "Still ambiguous.")
	waitForNotifications(t, notifier)
	sent = notifications.received()
	if len(sent) != 3 || sent[2].body["dedupeKey"] != "inbox.human_review:"+item+":"+second {
		t.Fatalf("notifications after a second round = %d, last key %v", len(sent), sent[len(sent)-1].body["dedupeKey"])
	}
}

func TestOnlyACaptureThatFailedForGoodNotifies(t *testing.T) {
	notifications := newFakeService(t, nil)
	inbox, notifier := newInboxAPI(t, notifications.url)

	done := inbox.capture("Buy milk")
	inbox.finishAttempt(done, inbox.startAttempt(done, "op-done"), "processed", "")

	failing := inbox.capture("Pay the rent")
	inbox.finishAttempt(failing, inbox.startAttempt(failing, "op-rent"), "retryable", "model unavailable")
	waitForNotifications(t, notifier)
	if sent := notifications.received(); len(sent) != 0 {
		t.Fatalf("notifications for a success and a retry = %d, want none", len(sent))
	}

	last := inbox.startAttempt(failing, "op-rent.retry.2")
	inbox.finishAttempt(failing, last, "failed", "gave up after 3 attempts: unusable model output")
	waitForNotifications(t, notifier)
	sent := notifications.received()
	if len(sent) != 1 {
		t.Fatalf("notifications = %d, want 1", len(sent))
	}
	want := map[string]any{
		"title":      "A capture failed",
		"body":       "Pay the rent\ngave up after 3 attempts: unusable model output",
		"importance": "medium",
		"category":   "inbox.failed",
		"link":       "/inbox#failed",
		"dedupeKey":  "inbox.failed:" + failing + ":" + last,
	}
	for key, value := range want {
		if sent[0].body[key] != value {
			t.Errorf("%s = %v, want %v", key, sent[0].body[key], value)
		}
	}
	if got := sent[0].header.Get(activity.CausationIDHeader); got != activity.EventID("inbox.processing.failed", last).String() {
		t.Fatalf("causation = %q", got)
	}
}

func TestInboxSucceedsWhenNotificationsIsDown(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	inbox, notifier := newInboxAPI(t, down.URL)

	review := inbox.capture("Call the dentist")
	inbox.requestReview(review, inbox.startAttempt(review, "op-review"), "Two tasks match.")
	failing := inbox.capture("Pay the rent")
	inbox.finishAttempt(failing, inbox.startAttempt(failing, "op-rent"), "failed", "gave up")
	if inbox.state(review) != "human_review" || inbox.state(failing) != "failed" {
		t.Fatal("the Inbox changes must stand without Notifications")
	}

	waitForNotifications(t, notifier)
	recorded := inbox.events.ofType("inbox.notification.failed")
	if len(recorded) != 2 {
		t.Fatalf("inbox.notification.failed events = %d, want 2", len(recorded))
	}
}

func TestInboxDoesNotWaitForSlowNotifications(t *testing.T) {
	notifications := newFakeService(t, func(f *fakeService) { f.release = make(chan struct{}) })
	inbox, notifier := newInboxAPI(t, notifications.url)
	item := inbox.capture("Call the dentist")
	attempt := inbox.startAttempt(item, "op-1")

	started := time.Now()
	inbox.requestReview(item, attempt, "Two tasks match.")
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("the review request took %v while Notifications was slow", elapsed)
	}
	if inbox.state(item) != "human_review" {
		t.Fatal("the review must be stored before Notifications answers")
	}

	close(notifications.release)
	waitForNotifications(t, notifier)
	if sent := notifications.received(); len(sent) != 1 || sent[0].body["category"] != "inbox.human_review" {
		t.Fatalf("notifications once Notifications answered = %v", sent)
	}
}

func TestInboxWithoutNotificationsURLSendsNothing(t *testing.T) {
	inbox, notifier := newInboxAPI(t, "")
	item := inbox.capture("Call the dentist")
	inbox.requestReview(item, inbox.startAttempt(item, "op-1"), "Two tasks match.")
	waitForNotifications(t, notifier)
	if inbox.state(item) != "human_review" || len(inbox.events.ofType("inbox.notification.failed")) != 0 {
		t.Fatal("without a Notifications URL the review is stored and nothing is sent or recorded")
	}
}
