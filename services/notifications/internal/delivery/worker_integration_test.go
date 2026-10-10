package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/activity"
	store "github.com/GAMch1k/life-os/services/notifications/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/notifications/internal/testdb"
)

// fakeChannel answers each Send with the next scripted error (nil = sent)
// and records what it was given.
type fakeChannel struct {
	name string
	mu   sync.Mutex
	errs []error
	sent []Notification
}

func (f *fakeChannel) Name() string { return f.name }

func (f *fakeChannel) Send(_ context.Context, n Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, n)
	if len(f.errs) == 0 {
		return nil
	}
	err := f.errs[0]
	f.errs = f.errs[1:]
	return err
}

func (f *fakeChannel) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

type harness struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *store.Queries
	events *[]map[string]any
	mu     *sync.Mutex
}

func newHarness(t *testing.T) harness {
	t.Helper()
	pool := testdb.Open(t)
	events := &[]map[string]any{}
	mu := &sync.Mutex{}
	return harness{t: t, pool: pool, q: store.New(pool), events: events, mu: mu}
}

func (h harness) emitter() *activity.Emitter {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event map[string]any
		_ = json.NewDecoder(r.Body).Decode(&event)
		h.mu.Lock()
		*h.events = append(*h.events, event)
		h.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	h.t.Cleanup(server.Close)
	return activity.NewEmitter(server.URL, "notifications", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func (h harness) recorded() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]any(nil), *h.events...)
}

func (h harness) worker(owner string, now func() time.Time, channels ...Channel) *Worker {
	return NewWorker(h.q, channels, Options{
		Policy:      Policy{BaseDelay: 10 * time.Second, MaxDelay: 15 * time.Minute, MaxAge: 24 * time.Hour},
		Poll:        20 * time.Millisecond,
		Lease:       30 * time.Second,
		SendTimeout: 5 * time.Second,
		Owner:       owner,
		Now:         now,
	}, h.emitter(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// seed stores a notification with one pending delivery per channel.
func (h harness) seed(key string, channels ...string) store.Notification {
	h.t.Helper()
	ctx := context.Background()
	link := "/inbox"
	correlation := "workflow-" + key
	row, err := h.q.InsertNotification(ctx, store.InsertNotificationParams{
		Title: "Inbox needs you", Body: "private detail", Importance: "medium", Category: "inbox.human_review",
		Source: "inbox", Link: &link, DedupeKey: key, CorrelationID: &correlation,
		OccurredAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	for _, channel := range channels {
		if _, err := h.q.InsertDelivery(ctx, store.InsertDeliveryParams{NotificationID: row.ID, Channel: channel}); err != nil {
			h.t.Fatal(err)
		}
	}
	return row
}

func (h harness) delivery(row store.Notification, channel string) store.Delivery {
	h.t.Helper()
	deliveries, err := h.q.ListDeliveries(context.Background(), []pgtype.UUID{row.ID})
	if err != nil {
		h.t.Fatal(err)
	}
	for _, d := range deliveries {
		if d.Channel == channel {
			return d
		}
	}
	h.t.Fatalf("no %s delivery", channel)
	return store.Delivery{}
}

func (h harness) process(w *Worker) bool {
	h.t.Helper()
	worked, err := w.ProcessOne(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return worked
}

// makeDue moves a delivery's next attempt into the past.
func (h harness) makeDue(row store.Notification) {
	h.t.Helper()
	if _, err := h.pool.Exec(context.Background(), `UPDATE deliveries SET next_attempt_at = now() - interval '1 second' WHERE notification_id = $1`, row.ID); err != nil {
		h.t.Fatal(err)
	}
}

func TestWorkerSendsAndRecordsDelivery(t *testing.T) {
	h := newHarness(t)
	channel := &fakeChannel{name: "test"}
	w := h.worker("w1", nil, channel)
	row := h.seed("k1", "test")

	if !h.process(w) {
		t.Fatal("nothing was claimed")
	}
	if h.process(w) {
		t.Fatal("a sent delivery was claimed again")
	}
	d := h.delivery(row, "test")
	if d.Status != StatusSent || d.Attempts != 1 || !d.SentAt.Valid || d.NextAttemptAt.Valid || d.LeaseOwner != nil || d.LastError != nil {
		t.Fatalf("delivery = %+v", d)
	}
	if channel.calls() != 1 || channel.sent[0].Title != "Inbox needs you" || channel.sent[0].Body != "private detail" ||
		channel.sent[0].Link != "/inbox" || channel.sent[0].Importance != "medium" {
		t.Fatalf("channel got %+v", channel.sent)
	}
	events := h.recorded()
	if len(events) != 1 || events[0]["type"] != "notification.delivered" || events[0]["correlationId"] != "workflow-k1" ||
		events[0]["causationId"] != activity.EventID("notification.created", channel.sent[0].ID.String()).String() {
		t.Fatalf("events = %v", events)
	}
	data, _ := events[0]["data"].(map[string]any)
	if data["channel"] != "test" || data["body"] != nil {
		t.Fatalf("delivered data = %v", data)
	}
}

func TestWorkerRetriesWithBackoffThenSends(t *testing.T) {
	h := newHarness(t)
	channel := &fakeChannel{name: "test", errs: []error{errors.New("connection refused"), errors.New("503 unavailable")}}
	w := h.worker("w1", nil, channel)
	row := h.seed("k1", "test")

	before := time.Now()
	h.process(w)
	d := h.delivery(row, "test")
	if d.Status != StatusRetrying || d.Attempts != 1 || d.LastError == nil || *d.LastError != "connection refused" || d.LeaseOwner != nil {
		t.Fatalf("after one failure = %+v", d)
	}
	if wait := d.NextAttemptAt.Time.Sub(before); wait < 9*time.Second || wait > 12*time.Second {
		t.Fatalf("first retry in %v, want about 10s", wait)
	}
	if h.process(w) {
		t.Fatal("a retry was claimed before it was due")
	}

	h.makeDue(row)
	before = time.Now()
	h.process(w)
	d = h.delivery(row, "test")
	if wait := d.NextAttemptAt.Time.Sub(before); d.Attempts != 2 || wait < 19*time.Second || wait > 22*time.Second {
		t.Fatalf("after two failures = %+v (retry in %v, want about 20s)", d, wait)
	}

	h.makeDue(row)
	h.process(w)
	if d = h.delivery(row, "test"); d.Status != StatusSent || d.Attempts != 3 {
		t.Fatalf("after the third attempt = %+v", d)
	}
	if events := h.recorded(); len(events) != 1 || events[0]["type"] != "notification.delivered" {
		t.Fatalf("events = %v (retries record nothing)", events)
	}
}

func TestWorkerFailsPermanentErrorsAtOnce(t *testing.T) {
	h := newHarness(t)
	channel := &fakeChannel{name: "test", errs: []error{Permanent(errors.New("403 topic forbidden"))}}
	w := h.worker("w1", nil, channel)
	row := h.seed("k1", "test")

	h.process(w)
	d := h.delivery(row, "test")
	if d.Status != StatusFailed || d.Attempts != 1 || d.NextAttemptAt.Valid || d.LastError == nil || *d.LastError != "403 topic forbidden" {
		t.Fatalf("delivery = %+v", d)
	}
	events := h.recorded()
	if len(events) != 1 || events[0]["type"] != "notification.delivery_failed" || events[0]["outcome"] != "failed" {
		t.Fatalf("events = %v", events)
	}
	if data, _ := events[0]["data"].(map[string]any); data["error"] != "403 topic forbidden" {
		t.Fatalf("failed data = %v", data)
	}
}

func TestWorkerFailsDeliveriesPastTheMaximumAge(t *testing.T) {
	h := newHarness(t)
	channel := &fakeChannel{name: "test", errs: []error{errors.New("timeout")}}
	tomorrow := func() time.Time { return time.Now().Add(25 * time.Hour) }

	// Too old when claimed: failed without an attempt.
	stale := h.seed("stale", "test")
	h.process(h.worker("w1", tomorrow, channel))
	if d := h.delivery(stale, "test"); d.Status != StatusFailed || channel.calls() != 0 || d.LastError == nil ||
		*d.LastError != "not delivered within the maximum age of 24h (0 attempts)" {
		t.Fatalf("stale delivery = %+v (calls %d)", d, channel.calls())
	}

	// A retry that would fall beyond the maximum age fails instead.
	late := h.seed("late", "test")
	nearlyTooOld := func() time.Time { return time.Now().Add(24*time.Hour - 5*time.Second) }
	h.process(h.worker("w1", nearlyTooOld, channel))
	d := h.delivery(late, "test")
	if d.Status != StatusFailed || channel.calls() != 1 || d.LastError == nil ||
		*d.LastError != "not delivered within the maximum age of 24h (1 attempt); last error: timeout" {
		t.Fatalf("late delivery = %+v", d)
	}
	if events := h.recorded(); len(events) != 2 || events[0]["type"] != "notification.delivery_failed" || events[1]["type"] != "notification.delivery_failed" {
		t.Fatalf("events = %v", events)
	}
}

func TestWorkerSkipsChannelsNoLongerConfigured(t *testing.T) {
	h := newHarness(t)
	row := h.seed("k1", "removed")
	h.process(h.worker("w1", nil))
	d := h.delivery(row, "removed")
	if d.Status != StatusSkipped || d.LastError == nil || *d.LastError != `channel "removed" is not configured` {
		t.Fatalf("delivery = %+v", d)
	}
	if events := h.recorded(); len(events) != 0 {
		t.Fatalf("events = %v (a skip records nothing)", events)
	}

	skipping := &fakeChannel{name: "test", errs: []error{Skip("muted by the channel")}}
	other := h.seed("k2", "test")
	h.process(h.worker("w1", nil, skipping))
	if d := h.delivery(other, "test"); d.Status != StatusSkipped || *d.LastError != "muted by the channel" {
		t.Fatalf("delivery = %+v", d)
	}
}

func TestExpiredLeaseIsClaimedAgain(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	row := h.seed("k1", "test")
	// A worker claims the delivery and then stops mid-send.
	claimed, err := h.q.ClaimDelivery(ctx, store.ClaimDeliveryParams{LeaseOwner: "crashed", LeaseSeconds: 60})
	if err != nil || claimed.Attempts != 1 || claimed.LeaseOwner == nil {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	channel := &fakeChannel{name: "test"}
	w := h.worker("w2", nil, channel)
	if h.process(w) {
		t.Fatal("a live lease was claimed")
	}
	if _, err := h.pool.Exec(ctx, `UPDATE deliveries SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, claimed.ID); err != nil {
		t.Fatal(err)
	}
	if !h.process(w) {
		t.Fatal("an expired lease was not claimed")
	}
	if d := h.delivery(row, "test"); d.Status != StatusSent || d.Attempts != 2 {
		t.Fatalf("delivery = %+v", d)
	}
	// The crashed worker cannot record anything any more.
	if _, err := h.q.MarkDeliverySent(ctx, store.MarkDeliverySentParams{ID: claimed.ID, LeaseOwner: "crashed"}); err == nil {
		t.Fatal("a lost lease recorded a result")
	}
}

func TestConcurrentWorkersSendEachDeliveryOnce(t *testing.T) {
	h := newHarness(t)
	const notifications = 12
	for index := range notifications {
		h.seed(string(rune('a'+index)), "test")
	}
	channel := &fakeChannel{name: "test"}
	var wg sync.WaitGroup
	for index := range 3 {
		w := h.worker(string(rune('x'+index)), nil, channel)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				worked, err := w.ProcessOne(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				if !worked {
					return
				}
			}
		}()
	}
	wg.Wait()
	if channel.calls() != notifications {
		t.Fatalf("sends = %d, want %d", channel.calls(), notifications)
	}
	var unsent int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM deliveries WHERE status <> 'sent'`).Scan(&unsent); err != nil || unsent != 0 {
		t.Fatalf("unsent = %d, %v", unsent, err)
	}
}

func TestRunDeliversWhenWoken(t *testing.T) {
	h := newHarness(t)
	channel := &fakeChannel{name: "test"}
	w := NewWorker(h.q, []Channel{channel}, Options{
		Policy: Policy{BaseDelay: time.Second, MaxDelay: time.Minute, MaxAge: time.Hour},
		// A long poll: only Wake can make this delivery prompt.
		Poll: time.Hour, Lease: 30 * time.Second, SendTimeout: 5 * time.Second,
	}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	defer func() { cancel(); <-done }()

	time.Sleep(50 * time.Millisecond) // the first round found nothing
	h.seed("k1", "test")
	w.Wake()
	deadline := time.Now().Add(5 * time.Second)
	for channel.calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if channel.calls() != 1 {
		t.Fatalf("sends = %d after Wake, want 1", channel.calls())
	}
}
