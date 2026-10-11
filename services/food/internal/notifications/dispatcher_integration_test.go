package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/notify"
	"github.com/GAMch1k/life-os/services/food/internal/api"
	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/food/internal/testdb"
)

// These tests run the dispatcher against a fresh, fully migrated schema on
// FOOD_TEST_DATABASE_URL, with pantry items written through the real API, an
// in-process Notifications stand-in that deduplicates by key like the real
// service, and an Activity recorder. The owner lives in Berlin (CEST,
// UTC+2, in October 2026; the reminder is due at 08:00, 06:00 UTC); the
// clock is moved by hand.

// notificationsServer stands in for the Notifications service.
type notificationsServer struct {
	mu       sync.Mutex
	stored   map[string]bool
	created  []map[string]any
	requests int
	actors   []string
	// fail answers the next requests with these statuses.
	fail []int
}

func (s *notificationsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	s.actors = append(s.actors, r.Header.Get(activity.ActorHeader))
	w.Header().Set("content-type", "application/json")
	if len(s.fail) > 0 {
		status := s.fail[0]
		s.fail = s.fail[1:]
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"code":"bad_request","message":"refused"},"request_id":"r1"}`))
		return
	}
	key, _ := body["dedupeKey"].(string)
	if s.stored[key] {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
		return
	}
	s.stored[key] = true
	s.created = append(s.created, body)
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{}`))
}

func (s *notificationsServer) failNext(statuses ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = append(s.fail, statuses...)
}

func (s *notificationsServer) count() (created, requests int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.created), s.requests
}

type activityRecorder struct {
	mu     sync.Mutex
	events []activity.Event
}

func (a *activityRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var event activity.Event
	_ = json.NewDecoder(r.Body).Decode(&event)
	a.mu.Lock()
	a.events = append(a.events, event)
	a.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

func (a *activityRecorder) missed() []activity.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []activity.Event
	for _, event := range a.events {
		if event.Type == missedType {
			out = append(out, event)
		}
	}
	return out
}

type harness struct {
	t           *testing.T
	pool        *pgxpool.Pool
	queries     *store.Queries
	app         *httpserver.App
	dispatcher  *Dispatcher
	notes       *notificationsServer
	notesURL    string
	activity    *activityRecorder
	activityURL string
	clock       time.Time
	seen        int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	pool := testdb.Open(t, "")
	h := &harness{t: t, pool: pool, queries: store.New(pool), notes: &notificationsServer{stored: map[string]bool{}}, activity: &activityRecorder{}}
	for target, handler := range map[*string]http.Handler{&h.notesURL: h.notes, &h.activityURL: h.activity} {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		*target = server.URL
	}
	h.dispatcher = h.newDispatcher()
	h.app = httpserver.New("food", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	api.Register(h.app, api.NewHandler(pool, api.Options{Location: berlin}).WithNotifications(h.dispatcher))
	return h
}

// newDispatcher is another dispatcher (another Food process, or the same
// one after a restart) on the harness's database, clock, and Notifications.
func (h *harness) newDispatcher() *Dispatcher {
	return New(h.pool, notify.New(h.notesURL, "food"), activity.NewEmitter(h.activityURL, "food", quiet),
		Config{Interval: 30 * time.Second, Grace: 2 * time.Hour, Owner: berlin, ExpiryTime: Clock{8, 0}}, quiet).
		WithClock(func() time.Time { return h.clock })
}

func (h *harness) do(method, target string, body any) (int, []byte) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("content-type", "application/json")
	req.Header.Set(activity.ActorHeader, "dev:test")
	resp, err := h.app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		h.t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp.StatusCode, raw
}

// stock adds a pantry item expiring on expires (YYYY-MM-DD; empty for none)
// and returns its id.
func (h *harness) stock(name, expires string) string {
	h.t.Helper()
	item := map[string]any{"name": name, "location": "fridge"}
	if expires != "" {
		item["expiresOn"] = expires
	}
	status, raw := h.do(http.MethodPost, "/v1/pantry-items", map[string]any{"items": []any{item}})
	if status != http.StatusCreated {
		h.t.Fatalf("add %s: %d %s", name, status, raw)
	}
	var out generated.AddPantryItemsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		h.t.Fatal(err)
	}
	return out.Items[0].Id.String()
}

func (h *harness) usedUp(id string) {
	h.t.Helper()
	if status, raw := h.do(http.MethodPost, "/v1/pantry-items/"+id+"/used-up", nil); status != http.StatusOK {
		h.t.Fatalf("used up: %d %s", status, raw)
	}
}

// runAt runs the dispatcher at the given instant, which must succeed.
func (h *harness) runAt(at string) Result {
	h.t.Helper()
	return h.runWith(h.dispatcher, at)
}

func (h *harness) runWith(d *Dispatcher, at string) Result {
	h.t.Helper()
	h.clock = instant(at)
	result, err := d.RunOnce(context.Background())
	if err != nil {
		h.t.Fatalf("run at %s: %v", at, err)
	}
	if result.Busy {
		h.t.Fatalf("run at %s: busy", at)
	}
	return result
}

// expectSent checks the notifications created since the last check by
// "title: body".
func (h *harness) expectSent(want ...string) []map[string]any {
	h.t.Helper()
	h.notes.mu.Lock()
	fresh := append([]map[string]any(nil), h.notes.created[h.seen:]...)
	h.seen = len(h.notes.created)
	h.notes.mu.Unlock()
	got := make([]string, 0, len(fresh))
	for _, body := range fresh {
		text, _ := body["body"].(string)
		got = append(got, body["title"].(string)+": "+text)
	}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		h.t.Fatalf("sent %q, want %q", got, want)
	}
	return fresh
}

func (h *harness) status() generated.FoodNotificationStatus {
	h.t.Helper()
	status, raw := h.do(http.MethodGet, "/v1/notifications/status", nil)
	if status != http.StatusOK {
		h.t.Fatalf("status: %d %s", status, raw)
	}
	var out generated.FoodNotificationStatus
	if err := json.Unmarshal(raw, &out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *harness) outcome(key string) string {
	h.t.Helper()
	var outcome string
	if err := h.pool.QueryRow(context.Background(), "SELECT outcome FROM food_notification_log WHERE dedupe_key = $1", key).Scan(&outcome); err != nil {
		h.t.Fatalf("outcome of %s: %v", key, err)
	}
	return outcome
}

func TestOneReminderADayNamesTodayAndTomorrow(t *testing.T) {
	h := newHarness(t)
	// The first run only starts the cursor.
	if result := h.runAt("2026-10-10T07:59:40+02:00"); result.Sent != 0 || !result.From.Equal(result.Through) {
		t.Fatalf("first run = %+v", result)
	}
	h.stock("Milch", "2026-10-10")
	h.stock("Joghurt", "2026-10-11")
	h.stock("joghurt", "2026-10-11")
	h.stock("Old cheese", "2026-10-09")
	h.stock("Later", "2026-10-12")
	h.stock("No date", "")
	h.usedUp(h.stock("Eaten", "2026-10-10"))

	result := h.runAt("2026-10-10T08:00:10+02:00")
	fresh := h.expectSent("Food expiring today: Today: Milch\nTomorrow: Joghurt (2)")
	sent := fresh[0]
	if sent["category"] != "food.expiry" || sent["importance"] != "medium" || sent["link"] != "/food?tab=pantry" ||
		sent["dedupeKey"] != "food:expiry:2026-10-10" || sent["occurredAt"] != "2026-10-10T06:00:00Z" {
		t.Fatalf("notification = %v", sent)
	}
	if result.Sent != 1 || result.Late != 0 {
		t.Fatalf("result = %+v", result)
	}
	if h.notes.actors[0] != "system:food" {
		t.Fatalf("actor = %q", h.notes.actors[0])
	}
	// Once a day.
	h.runAt("2026-10-10T08:00:40+02:00")
	h.runAt("2026-10-10T20:00:00+02:00")
	h.expectSent()
	status := h.status()
	if !status.Enabled || status.ExpiryTime != "08:00" || status.TimeZone != "Europe/Berlin" || status.LastSentAt == nil ||
		!status.LastSentAt.Equal(instant("2026-10-10T06:00:10Z")) || status.HandledThrough == nil || !status.HandledThrough.Equal(instant("2026-10-10T18:00:00Z")) ||
		status.LastError != nil || status.LastSuccessAt == nil {
		t.Fatalf("status = %+v", status)
	}
	// The next morning: Milch expired yesterday and is not named again;
	// yesterday's tomorrow is today now, and the day after is tomorrow.
	h.runAt("2026-10-11T08:00:10+02:00")
	h.expectSent("Food expiring today: Today: Joghurt (2)\nTomorrow: Later")
}

func TestDaysWithNothingToSaySendNothing(t *testing.T) {
	h := newHarness(t)
	h.runAt("2026-10-10T07:00:00+02:00")
	// Expired before today and expiring later are nothing to say.
	h.stock("Old cheese", "2026-10-09")
	h.stock("Later", "2026-10-13")
	result := h.runAt("2026-10-10T08:00:10+02:00")
	h.expectSent()
	if result.Empty != 1 || result.Sent != 0 || h.outcome("food:expiry:2026-10-10") != "empty" {
		t.Fatalf("result = %+v", result)
	}
	// Recorded, so food added later that day does not trigger it again.
	h.stock("Milch", "2026-10-10")
	h.runAt("2026-10-10T09:00:00+02:00")
	h.expectSent()
	if status := h.status(); status.LastSentAt != nil {
		t.Fatalf("status = %+v", status)
	}
	// Two days on, the later food is tomorrow's: low importance.
	h.runAt("2026-10-11T07:00:00+02:00")
	fresh := h.runAt("2026-10-12T08:00:10+02:00")
	sent := h.expectSent("Food expiring tomorrow: Tomorrow: Later")
	if sent[0]["importance"] != "low" || fresh.Sent != 1 {
		t.Fatalf("sent = %v, result %+v", sent, fresh)
	}
}

func TestRemindersDueWhileDownAreSentLateOrRecordedAsMissed(t *testing.T) {
	h := newHarness(t)
	h.stock("Milch", "2026-10-11")
	h.stock("Brot", "2026-10-12")
	h.runAt("2026-10-10T07:00:00+02:00")
	// Food is down until 09:30: within the grace window, sent late.
	result := h.runAt("2026-10-10T09:30:00+02:00")
	h.expectSent("Food expiring tomorrow: Tomorrow: Milch")
	if result.Sent != 1 || result.Late != 1 {
		t.Fatalf("result = %+v", result)
	}
	// Down again from 10:00 until 10:30 the next day: past the grace window.
	h.runAt("2026-10-10T10:00:00+02:00")
	result = h.runAt("2026-10-11T10:30:00+02:00")
	h.expectSent()
	if result.Missed != 1 || h.outcome("food:expiry:2026-10-11") != "missed" {
		t.Fatalf("result = %+v", result)
	}
	missed := h.activity.missed()
	if len(missed) != 1 {
		t.Fatalf("missed = %+v", missed)
	}
	event := missed[0]
	if event.Subject != (activity.Subject{Type: "food_notification", ID: "food:expiry:2026-10-11"}) || event.Outcome != activity.Failed ||
		event.Summary != "Expiry reminder due Sun 11 Oct 2026, 08:00 CEST was missed: Food was not running" ||
		event.ID != activity.EventID(missedType, "food:expiry:2026-10-11") || !event.OccurredAt.Equal(instant("2026-10-11T06:00:00Z")) ||
		event.Actor != activity.System("food") || event.Data["day"] != "2026-10-11" {
		t.Fatalf("missed event = %+v", event)
	}
	// Recorded once.
	h.runAt("2026-10-11T10:30:30+02:00")
	if len(h.activity.missed()) != 1 {
		t.Fatal("missed recorded twice")
	}
	// A day off the catch-up window entirely: older reminders are not
	// examined after a week down.
	result = h.runAt("2026-10-20T07:00:00+02:00")
	if result.Missed != 0 || !result.From.Equal(instant("2026-10-13T05:00:00Z")) {
		t.Fatalf("after a week down = %+v", result)
	}
}

func TestNotificationFailures(t *testing.T) {
	h := newHarness(t)
	h.stock("Milch", "2026-10-10")
	h.runAt("2026-10-10T07:00:00+02:00")
	// Notifications is failing: the run stops and keeps the cursor before
	// the reminder.
	h.notes.failNext(http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusServiceUnavailable)
	h.clock = instant("2026-10-10T08:00:10+02:00")
	if _, err := h.dispatcher.RunOnce(context.Background()); err == nil {
		t.Fatal("a failing send was not reported")
	}
	h.expectSent()
	status := h.status()
	if status.LastError == nil || status.LastErrorAt == nil || status.HandledThrough == nil || !status.HandledThrough.Equal(instant("2026-10-10T07:59:59.999999+02:00")) {
		t.Fatalf("status after a failure = %+v", status)
	}
	h.runAt("2026-10-10T08:00:40+02:00")
	h.expectSent("Food expiring today: Today: Milch")
	// Refused: recorded as failed and not retried.
	h.stock("Brot", "2026-10-12")
	h.notes.failNext(http.StatusBadRequest)
	h.clock = instant("2026-10-11T08:00:10+02:00")
	if result, err := h.dispatcher.RunOnce(context.Background()); err == nil || result.Failed != 1 {
		t.Fatalf("refused = %+v, %v", result, err)
	}
	_, before := h.notes.count()
	h.runAt("2026-10-11T08:00:40+02:00")
	if _, after := h.notes.count(); after != before {
		t.Fatal("a refused reminder was retried")
	}
	h.expectSent()
	if h.outcome("food:expiry:2026-10-11") != "failed" {
		t.Fatal("refusal not recorded")
	}
}

func TestARestartOrAnOlderCursorNeverSendsTwice(t *testing.T) {
	h := newHarness(t)
	h.stock("Milch", "2026-10-10")
	h.runAt("2026-10-10T07:00:00+02:00")
	h.runAt("2026-10-10T08:00:10+02:00")
	h.expectSent("Food expiring today: Today: Milch")
	// Another process (a restart) runs from the stored cursor.
	restarted := h.newDispatcher()
	if result := h.runWith(restarted, "2026-10-10T08:01:00+02:00"); result.Sent != 0 {
		t.Fatalf("after restart = %+v", result)
	}
	// The cursor set back before the reminder: the log knows it was sent.
	if _, err := h.pool.Exec(context.Background(), "UPDATE food_notification_dispatch SET last_run_at = $1", instant("2026-10-10T07:00:00+02:00")); err != nil {
		t.Fatal(err)
	}
	if result := h.runWith(restarted, "2026-10-10T08:02:00+02:00"); result.Sent != 0 || result.Handled != 1 {
		t.Fatalf("with an older cursor = %+v", result)
	}
	h.expectSent()
}

func TestOneProcessRunsAtATime(t *testing.T) {
	h := newHarness(t)
	h.stock("Milch", "2026-10-10")
	h.stock("Brot", "2026-10-11")
	h.runAt("2026-10-10T07:00:00+02:00")

	// Another process holds the lease.
	h.clock = instant("2026-10-10T08:00:10+02:00")
	if _, err := h.queries.ClaimNotificationRun(context.Background(), store.ClaimNotificationRunParams{Owner: "other", LeaseUntil: at(h.clock.Add(time.Minute)), Now: at(h.clock)}); err != nil {
		t.Fatal(err)
	}
	if result, err := h.dispatcher.RunOnce(context.Background()); err != nil || !result.Busy {
		t.Fatalf("run while another holds the lease = %+v, %v", result, err)
	}
	h.expectSent()
	// Its lease expires (it stopped): this process takes over.
	h.runAt("2026-10-10T08:01:20+02:00")
	h.expectSent("Food expiring today: Today: Milch\nTomorrow: Brot")

	// Two processes running at once send the next day's reminder once.
	other := h.newDispatcher()
	h.clock = instant("2026-10-11T08:00:10+02:00")
	var wg sync.WaitGroup
	results := make([]Result, 2)
	for index, d := range []*Dispatcher{h.dispatcher, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := d.RunOnce(context.Background())
			if err != nil {
				t.Error(err)
			}
			results[index] = result
		}()
	}
	wg.Wait()
	h.expectSent("Food expiring today: Today: Brot")
	if results[0].Sent+results[1].Sent != 1 {
		t.Fatalf("results = %+v", results)
	}
}

func TestMigrationDownAndUp(t *testing.T) {
	h := newHarness(t)
	h.stock("Milch", "2026-10-11")
	h.runAt("2026-10-10T07:00:00+02:00")
	testdb.Apply(t, h.pool, testdb.Migration(t, "000003", "down"))
	var tables int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name LIKE 'food_notification_%'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("tables after down = %d, %v", tables, err)
	}
	var pantry int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM pantry_items`).Scan(&pantry); err != nil || pantry != 1 {
		t.Fatalf("pantry after down = %d, %v", pantry, err)
	}
	testdb.Apply(t, h.pool, testdb.Migration(t, "000003", "up"))
	// Delivery starts over: the first run starts the cursor again.
	if result := h.runAt("2026-10-10T08:00:10+02:00"); result.Sent != 0 {
		t.Fatalf("first run after up = %+v", result)
	}
	h.runAt("2026-10-10T08:00:40+02:00")
	h.expectSent()
}
