package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/notify"
	"github.com/GAMch1k/life-os/services/tasks/internal/api"
	"github.com/GAMch1k/life-os/services/tasks/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/tasks/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/tasks/internal/testdb"
)

// These tests run the dispatcher against a fresh, fully migrated schema on
// TASKS_TEST_DATABASE_URL, with Tasks written through the real API, an
// in-process Notifications stand-in that deduplicates by key like the real
// service, a Calendar stand-in, and an Activity recorder. The owner lives in
// Berlin (CEST, UTC+2, in October 2026); the clock is moved by hand.

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

// calendarServer stands in for Calendar's GET /v1/events.
type calendarServer struct {
	mu      sync.Mutex
	down    bool
	items   []map[string]any
	queries []url.Values
}

func (c *calendarServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = append(c.queries, r.URL.Query())
	if c.down || r.URL.Path != "/v1/events" {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": c.items})
}

func (c *calendarServer) set(down bool, items ...map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.down, c.items = down, items
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

func (a *activityRecorder) all() []activity.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]activity.Event(nil), a.events...)
}

type harness struct {
	t           *testing.T
	pool        *pgxpool.Pool
	queries     *store.Queries
	app         *httpserver.App
	dispatcher  *Dispatcher
	schedule    Schedule
	notes       *notificationsServer
	notesURL    string
	calendar    *calendarServer
	calendarURL string
	activity    *activityRecorder
	activityURL string
	clock       time.Time
	seen        int
}

func newHarness(t *testing.T, schedule Schedule) *harness {
	t.Helper()
	pool := testdb.Open(t, "")
	h := &harness{
		t: t, pool: pool, queries: store.New(pool), schedule: schedule,
		notes: &notificationsServer{stored: map[string]bool{}}, calendar: &calendarServer{}, activity: &activityRecorder{},
	}
	for target, handler := range map[*string]http.Handler{&h.notesURL: h.notes, &h.calendarURL: h.calendar, &h.activityURL: h.activity} {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		*target = server.URL
	}
	h.dispatcher = h.newDispatcher()
	h.app = httpserver.New("tasks", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	api.Register(h.app, api.NewHandler(h.queries, nil).WithNotifications(h.dispatcher))
	return h
}

// newDispatcher is another dispatcher (another Tasks process) on the
// harness's database, clock, Notifications, and Calendar.
func (h *harness) newDispatcher() *Dispatcher {
	return New(h.pool, notify.New(h.notesURL, "tasks"), NewCalendar(h.calendarURL, berlin), activity.NewEmitter(h.activityURL, "tasks", quiet),
		Config{Interval: 30 * time.Second, Grace: 2 * time.Hour, Owner: berlin, Schedule: h.schedule}, quiet).
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

func (h *harness) task(method, target string, body any, want int) generated.Task {
	h.t.Helper()
	status, raw := h.do(method, target, body)
	if status != want {
		h.t.Fatalf("%s %s: %d %s, want %d", method, target, status, raw, want)
	}
	var out generated.Task
	if err := json.Unmarshal(raw, &out); err != nil {
		h.t.Fatalf("%s %s: %v", method, target, err)
	}
	return out
}

// create adds a Task due at due (RFC 3339; empty for none) and returns its
// id.
func (h *harness) create(title, due string) string {
	h.t.Helper()
	body := map[string]any{"title": title}
	if due != "" {
		body["dueAt"] = due
	}
	return h.task(http.MethodPost, "/v1/tasks", body, http.StatusCreated).Id.String()
}

func (h *harness) complete(id string) {
	h.t.Helper()
	h.task(http.MethodPost, "/v1/tasks/"+id+"/complete", nil, http.StatusOK)
}

func (h *harness) reopen(id string) {
	h.t.Helper()
	h.task(http.MethodPost, "/v1/tasks/"+id+"/reopen", nil, http.StatusOK)
}

func (h *harness) update(id string, body map[string]any) {
	h.t.Helper()
	h.task(http.MethodPatch, "/v1/tasks/"+id, body, http.StatusOK)
}

func (h *harness) delete(id string) {
	h.t.Helper()
	if status, raw := h.do(http.MethodDelete, "/v1/tasks/"+id, nil); status != http.StatusNoContent {
		h.t.Fatalf("delete %s: %d %s", id, status, raw)
	}
}

// runAt runs the dispatcher at the given instant, which must succeed.
func (h *harness) runAt(at string) Result {
	h.t.Helper()
	h.clock = instant(at)
	result, err := h.dispatcher.RunOnce(context.Background())
	if err != nil {
		h.t.Fatalf("run at %s: %v", at, err)
	}
	if result.Busy {
		h.t.Fatalf("run at %s: busy", at)
	}
	return result
}

// sent returns the notifications created since the last call.
func (h *harness) sent() []map[string]any {
	h.t.Helper()
	h.notes.mu.Lock()
	defer h.notes.mu.Unlock()
	fresh := h.notes.created[h.seen:]
	h.seen = len(h.notes.created)
	return append([]map[string]any(nil), fresh...)
}

// expectSent checks the notifications created since the last check by
// "title: body".
func (h *harness) expectSent(want ...string) []map[string]any {
	h.t.Helper()
	fresh := h.sent()
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

// expectFields checks a sent notification's other fields.
func (h *harness) expectFields(sent map[string]any, category, importance, link, key, occurredAt string) {
	h.t.Helper()
	if sent["category"] != category || sent["importance"] != importance || sent["link"] != link || sent["dedupeKey"] != key || sent["occurredAt"] != occurredAt {
		h.t.Fatalf("notification = %v, want %s %s %s %s %s", sent, category, importance, link, key, occurredAt)
	}
}

func (h *harness) status() generated.TaskNotificationStatus {
	h.t.Helper()
	status, raw := h.do(http.MethodGet, "/v1/notifications/status", nil)
	if status != http.StatusOK {
		h.t.Fatalf("status: %d %s", status, raw)
	}
	var out generated.TaskNotificationStatus
	if err := json.Unmarshal(raw, &out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *harness) exec(sql string, args ...any) {
	h.t.Helper()
	if _, err := h.pool.Exec(context.Background(), sql, args...); err != nil {
		h.t.Fatal(err)
	}
}

func TestTimedTasksRemindOnceBeforeTheyAreDue(t *testing.T) {
	h := newHarness(t, DefaultSchedule())
	h.create("Pay rent", "2026-10-06T00:00:00+02:00")
	call := h.create("Call mom", "2026-10-10T15:00:00+02:00")
	// The first run only starts the cursor: nothing before it is sent.
	if result := h.runAt("2026-10-10T14:00:00+02:00"); result.Sent != 0 || !result.From.Equal(result.Through) {
		t.Fatalf("first run = %+v", result)
	}
	h.runAt("2026-10-10T14:29:40+02:00")
	h.expectSent()
	result := h.runAt("2026-10-10T14:30:10+02:00")
	fresh := h.expectSent("Call mom: Due in 30 minutes · 15:00")
	h.expectFields(fresh[0], "tasks.due", "medium", "/tasks?task="+call, "tasks:due:"+call+":2026-10-10T13:00:00Z", "2026-10-10T12:30:00Z")
	if result.Sent != 1 || result.Late != 0 || h.notes.actors[0] != "system:tasks" {
		t.Fatalf("result = %+v, actor %s", result, h.notes.actors[0])
	}
	h.runAt("2026-10-10T14:30:40+02:00")
	h.expectSent()
	// Another process, and a cursor set back, send nothing again.
	other := h.newDispatcher()
	h.clock = instant("2026-10-10T14:31:00+02:00")
	if result, err := other.RunOnce(context.Background()); err != nil || result.Sent != 0 {
		t.Fatalf("other process = %+v, %v", result, err)
	}
	h.exec(`UPDATE task_notification_dispatch SET last_run_at = $1`, instant("2026-10-10T14:00:00+02:00"))
	if result := h.runAt("2026-10-10T14:31:30+02:00"); result.Handled != 1 || result.Sent != 0 {
		t.Fatalf("after a lost cursor = %+v", result)
	}
	h.expectSent()
	if created, requests := h.notes.count(); created != 1 || requests != 1 {
		t.Fatalf("notifications: %d created, %d requests", created, requests)
	}
	status := h.status()
	if !status.Enabled || status.HandledThrough == nil || !status.HandledThrough.Equal(instant("2026-10-10T14:31:30+02:00")) || status.LastSentAt == nil || status.LastError != nil {
		t.Fatalf("status = %+v", status)
	}
}

func TestMorningAndEveningLists(t *testing.T) {
	h := newHarness(t, DefaultSchedule())
	h.create("Pay rent", "2026-10-06T00:00:00+02:00")       // date-only, Tue 6 Oct
	h.create("Call bank", "2026-10-09T16:00:00+02:00")      // timed, yesterday
	h.create("Renew passport", "2026-10-10T00:00:00+02:00") // date-only today: the owner's midnight
	h.create("Water plants", "2026-10-10T00:00:00Z")        // date-only today: a bare date as UTC midnight
	h.create("Standup", "2026-10-10T09:20:00+02:00")        // timed today (reminded at 08:50, before the cursor)
	h.create("Buy gift", "2026-10-11T00:00:00+02:00")       // date-only tomorrow
	h.create("Dentist", "2026-10-11T10:00:00+02:00")        // timed tomorrow: in neither list
	h.create("Someday", "")                                 // undated: in neither list
	h.complete(h.create("Done", "2026-10-08T00:00:00+02:00"))
	h.runAt("2026-10-10T08:55:00+02:00")
	h.runAt("2026-10-10T08:59:40+02:00")
	h.expectSent()
	h.runAt("2026-10-10T09:00:10+02:00")
	fresh := h.expectSent("Tasks for today: Today: Renew passport\nToday: Water plants\nOverdue since Tue 6 Oct: Pay rent\nOverdue since Fri 9 Oct: Call bank")
	h.expectFields(fresh[0], "tasks.morning", "medium", "/tasks", "tasks:morning:2026-10-10", "2026-10-10T07:00:00Z")

	h.runAt("2026-10-10T18:59:40+02:00")
	h.expectSent()
	h.runAt("2026-10-10T19:00:10+02:00")
	fresh = h.expectSent("Tasks for tomorrow: Buy gift")
	h.expectFields(fresh[0], "tasks.evening", "medium", "/tasks", "tasks:evening:2026-10-10", "2026-10-10T17:00:00Z")
}

func TestOverdueTasksRepeatEveryMorningUntilDone(t *testing.T) {
	h := newHarness(t, DefaultSchedule())
	rent := h.create("Pay rent", "2026-10-06T00:00:00+02:00")
	h.runAt("2026-10-10T08:59:00+02:00")
	h.runAt("2026-10-10T09:00:00+02:00")
	h.expectSent("Overdue tasks: Overdue since Tue 6 Oct: Pay rent")
	// Not twice the same day: another run, another process, a cursor set
	// back.
	h.runAt("2026-10-10T09:00:30+02:00")
	h.clock = instant("2026-10-10T09:01:00+02:00")
	if _, err := h.newDispatcher().RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.exec(`UPDATE task_notification_dispatch SET last_run_at = $1`, instant("2026-10-10T08:30:00+02:00"))
	if result := h.runAt("2026-10-10T09:01:30+02:00"); result.Handled != 1 {
		t.Fatalf("after a lost cursor = %+v", result)
	}
	h.expectSent()
	// Nothing is due tomorrow: the evening list is not sent.
	if result := h.runAt("2026-10-10T19:00:30+02:00"); result.Empty != 1 || result.Sent != 0 {
		t.Fatalf("empty evening = %+v", result)
	}
	h.expectSent()

	// The next morning: the summary at 08:00, the morning list at 09:00.
	h.runAt("2026-10-11T08:00:10+02:00")
	h.expectSent("Your day: Overdue: Pay rent (since Tue 6 Oct)")
	h.runAt("2026-10-11T09:00:10+02:00")
	h.expectSent("Overdue tasks: Overdue since Tue 6 Oct: Pay rent")

	// Done: nothing the day after, and empty messages are not missed.
	h.complete(rent)
	h.runAt("2026-10-12T07:59:50+02:00")
	if result := h.runAt("2026-10-12T09:00:10+02:00"); result.Empty != 2 || result.Sent != 0 {
		t.Fatalf("nothing open = %+v", result)
	}
	h.expectSent()
	// Reopened: back the next morning.
	h.reopen(rent)
	h.runAt("2026-10-13T07:59:50+02:00")
	h.runAt("2026-10-13T09:00:10+02:00")
	h.expectSent("Your day: Overdue: Pay rent (since Tue 6 Oct)", "Overdue tasks: Overdue since Tue 6 Oct: Pay rent")
	if missed := h.activity.all(); len(missed) != 0 {
		t.Fatalf("activity = %+v", missed)
	}
}

func TestCompletingOrChangingATaskTakesEffectOnTheNextRun(t *testing.T) {
	h := newHarness(t, DefaultSchedule())
	call := h.create("Call mom", "2026-10-10T15:00:00+02:00")
	bills := h.create("Pay bills", "2026-10-10T16:00:00+02:00")
	gift := h.create("Buy gift", "2026-10-12T00:00:00+02:00")
	h.runAt("2026-10-10T14:00:00+02:00")
	// Completed: no reminder.
	h.complete(call)
	h.runAt("2026-10-10T14:30:10+02:00")
	h.expectSent()
	// Reopened after its reminder time: not sent (it has passed).
	h.reopen(call)
	h.runAt("2026-10-10T14:40:00+02:00")
	h.expectSent()
	// A new due time reminds again, at the new time.
	h.update(call, map[string]any{"dueAt": "2026-10-10T17:00:00+02:00"})
	h.runAt("2026-10-10T15:30:10+02:00")
	h.expectSent("Pay bills: Due in 30 minutes · 16:00")
	h.runAt("2026-10-10T16:30:10+02:00")
	fresh := h.expectSent("Call mom: Due in 30 minutes · 17:00")
	if fresh[0]["dedupeKey"] != "tasks:due:"+call+":2026-10-10T15:00:00Z" {
		t.Fatalf("key = %v", fresh[0]["dedupeKey"])
	}
	// A title change after the reminder does not remind again.
	h.update(call, map[string]any{"title": "Call mum"})
	h.runAt("2026-10-10T16:30:40+02:00")
	h.expectSent()
	// Moved later: reminded again for the new time.
	h.update(bills, map[string]any{"dueAt": "2026-10-10T18:00:00+02:00"})
	h.runAt("2026-10-10T17:30:10+02:00")
	h.expectSent("Pay bills: Due in 30 minutes · 18:00")
	// A date-only Task moved to tomorrow is in the evening list.
	h.update(gift, map[string]any{"dueAt": "2026-10-11T00:00:00+02:00"})
	h.runAt("2026-10-10T19:00:10+02:00")
	h.expectSent("Tasks for tomorrow: Buy gift")
}

// A deleted Task is gone from every later message: its reminder is not sent,
// and the summary, morning, and evening lists leave it out, also a recurring
// one.
func TestDeletedTasksAreNeverNamedAgain(t *testing.T) {
	h := newHarness(t, DefaultSchedule())
	rent := h.create("Pay rent", "2026-10-06T00:00:00+02:00") // overdue
	call := h.create("Call mom", "2026-10-10T15:00:00+02:00") // timed today, reminded at 14:30
	h.create("Renew passport", "2026-10-10T00:00:00+02:00")   // date-only today, kept
	gift := h.create("Buy gift", "2026-10-11T00:00:00+02:00") // date-only tomorrow
	plants := h.task(http.MethodPost, "/v1/tasks", map[string]any{
		"title": "Water plants", "dueAt": "2026-10-11T00:00:00+02:00",
		"recurrence": map[string]any{"frequency": "weekly", "interval": 1},
	}, http.StatusCreated).Id.String()
	h.runAt("2026-10-10T07:00:00+02:00")
	h.runAt("2026-10-10T08:00:10+02:00")
	h.expectSent("Your day: Tasks: Renew passport · 15:00 Call mom\nOverdue: Pay rent (since Tue 6 Oct)")

	// Deleted after the summary named them: the morning list leaves them out,
	// and the timed one is not reminded.
	h.delete(rent)
	h.delete(call)
	h.runAt("2026-10-10T09:00:10+02:00")
	h.expectSent("Tasks for today: Today: Renew passport")
	h.runAt("2026-10-10T14:29:40+02:00")
	if result := h.runAt("2026-10-10T14:30:10+02:00"); result.Handled != 0 {
		t.Fatalf("reminder run = %+v", result)
	}
	h.expectSent()

	// Nothing left for tomorrow: the evening list is not sent.
	h.delete(gift)
	h.delete(plants)
	if result := h.runAt("2026-10-10T19:00:10+02:00"); result.Empty != 1 || result.Sent != 0 {
		t.Fatalf("evening = %+v", result)
	}
	h.expectSent()
}

func TestNotificationsDueWhileDownAreSentLateOrRecordedAsMissed(t *testing.T) {
	h := newHarness(t, DefaultSchedule())
	h.create("Pay rent", "2026-10-06T00:00:00+02:00")
	pills := h.create("Take pills", "2026-10-10T08:15:00+02:00") // reminded at 07:45: missed
	h.create("Call bank", "2026-10-10T10:45:00+02:00")           // reminded at 10:15: late
	h.runAt("2026-10-10T07:40:00+02:00")
	// Tasks is down until 10:30: the summary (08:00) is past the grace
	// window, the morning list (09:00) and the reminder (10:15) are not.
	result := h.runAt("2026-10-10T10:30:00+02:00")
	h.expectSent(
		"Overdue tasks: Overdue since Tue 6 Oct: Pay rent\nOverdue since 08:15: Take pills",
		"Call bank: Due in 15 minutes · 10:45 (late)",
	)
	if result.Sent != 2 || result.Late != 2 || result.Missed != 2 {
		t.Fatalf("result = %+v", result)
	}
	missed := h.activity.all()
	if len(missed) != 2 {
		t.Fatalf("activity = %+v", missed)
	}
	reminder, summary := missed[0], missed[1]
	if reminder.Type != "tasks.notification.missed" || reminder.Subject != (activity.Subject{Type: "task", ID: pills}) || reminder.Outcome != activity.Failed ||
		reminder.Summary != "Reminder for task “Take pills” due Sat 10 Oct 2026, 07:45 CEST was missed: Tasks was not running" ||
		reminder.ID != activity.EventID("tasks.notification.missed", "tasks:due:"+pills+":2026-10-10T06:15:00Z") || !reminder.OccurredAt.Equal(instant("2026-10-10T05:45:00Z")) ||
		reminder.Actor != activity.System("tasks") {
		t.Errorf("missed reminder = %+v", reminder)
	}
	if summary.Subject != (activity.Subject{Type: "task_notification", ID: "summary:morning:2026-10-10"}) ||
		summary.Summary != "Morning summary due Sat 10 Oct 2026, 08:00 CEST was missed: Tasks was not running" || summary.Data["day"] != "2026-10-10" {
		t.Errorf("missed summary = %+v", summary)
	}
	// Recorded once.
	h.runAt("2026-10-10T10:30:30+02:00")
	h.expectSent()
	if len(h.activity.all()) != 2 {
		t.Fatal("missed recorded twice")
	}
}

func TestTheSummaryReadsTheDaysEvents(t *testing.T) {
	h := newHarness(t, DefaultSchedule())
	h.calendar.set(false,
		map[string]any{"title": "Birthday", "allDay": true, "startDate": "2026-10-10", "endDate": "2026-10-10", "status": "active"},
		map[string]any{"title": "Dentist", "allDay": false, "startAt": "2026-10-10T07:00:00Z", "endAt": "2026-10-10T08:00:00Z", "status": "active"},
		map[string]any{"title": "Свидание с Ирой", "allDay": false, "startAt": "2026-10-10T18:00:00Z", "endAt": "2026-10-10T21:00:00Z", "status": "active"},
	)
	h.create("Renew passport", "2026-10-10T00:00:00+02:00")
	h.create("Call mom", "2026-10-10T15:00:00+02:00")
	h.create("Pay rent", "2026-10-06T00:00:00+02:00")
	h.runAt("2026-10-10T07:59:40+02:00")
	h.runAt("2026-10-10T08:00:10+02:00")
	fresh := h.expectSent("Your day: Events: Birthday (all day) · 09:00 Dentist · 20:00 Свидание с Ирой\nTasks: Renew passport · 15:00 Call mom\nOverdue: Pay rent (since Tue 6 Oct)")
	h.expectFields(fresh[0], "summary.morning", "low", "/", "summary:morning:2026-10-10", "2026-10-10T06:00:00Z")
	h.calendar.mu.Lock()
	query := h.calendar.queries[0]
	h.calendar.mu.Unlock()
	if query.Get("from") != "2026-10-09T22:00:00Z" || query.Get("to") != "2026-10-10T22:00:00Z" || query.Get("timeZone") != "Europe/Berlin" {
		t.Fatalf("calendar query = %v", query)
	}
	// The 09:00 morning list follows separately.
	h.runAt("2026-10-10T09:00:10+02:00")
	h.expectSent("Tasks for today: Today: Renew passport\nOverdue since Tue 6 Oct: Pay rent")
}

func TestEqualTimesSendOneMorningMessageEvenWithCalendarDown(t *testing.T) {
	schedule := DefaultSchedule()
	schedule.Summary = Clock{9, 0}
	h := newHarness(t, schedule)
	h.calendar.set(true)
	h.create("Renew passport", "2026-10-10T00:00:00+02:00")
	h.create("Pay rent", "2026-10-06T00:00:00+02:00")
	h.runAt("2026-10-10T07:59:40+02:00")
	h.runAt("2026-10-10T08:00:10+02:00")
	h.expectSent()
	result := h.runAt("2026-10-10T09:00:10+02:00")
	fresh := h.expectSent("Your day: Events: Calendar unavailable\nTasks: Renew passport\nOverdue: Pay rent (since Tue 6 Oct)")
	h.expectFields(fresh[0], "summary.morning", "medium", "/", "summary:morning:2026-10-10", "2026-10-10T07:00:00Z")
	if !result.CalendarUnavailable || result.Sent != 1 {
		t.Fatalf("result = %+v", result)
	}
	if status := h.status(); !status.MergedMorning || status.SummaryTime != "09:00" || status.MorningTime != "09:00" {
		t.Fatalf("status = %+v", status)
	}
}

func TestNotificationFailures(t *testing.T) {
	h := newHarness(t, DefaultSchedule())
	h.create("Call mom", "2026-10-10T15:00:00+02:00")
	h.create("Pay bills", "2026-10-10T15:10:00+02:00")
	h.runAt("2026-10-10T14:00:00+02:00")
	// Notifications is failing: the run stops and keeps the cursor before
	// the reminder.
	h.notes.failNext(http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusServiceUnavailable)
	h.clock = instant("2026-10-10T14:30:10+02:00")
	if _, err := h.dispatcher.RunOnce(context.Background()); err == nil {
		t.Fatal("a failing send was not reported")
	}
	h.expectSent()
	status := h.status()
	if status.LastError == nil || status.HandledThrough == nil || !status.HandledThrough.Equal(instant("2026-10-10T14:29:59.999999+02:00")) {
		t.Fatalf("status after a failure = %+v", status)
	}
	h.runAt("2026-10-10T14:30:40+02:00")
	h.expectSent("Call mom: Due in 30 minutes · 15:00")
	// Refused: recorded as failed and not retried.
	h.notes.failNext(http.StatusBadRequest)
	h.clock = instant("2026-10-10T14:40:10+02:00")
	if result, err := h.dispatcher.RunOnce(context.Background()); err == nil || result.Failed != 1 {
		t.Fatalf("refused = %+v, %v", result, err)
	}
	_, before := h.notes.count()
	h.runAt("2026-10-10T14:40:40+02:00")
	if _, after := h.notes.count(); after != before {
		t.Fatal("a refused notification was retried")
	}
	h.expectSent()
}

func TestOneProcessRunsAtATime(t *testing.T) {
	h := newHarness(t, DefaultSchedule())
	h.create("Call mom", "2026-10-10T15:00:00+02:00")
	h.runAt("2026-10-10T14:00:00+02:00")

	// Another process holds the lease.
	h.clock = instant("2026-10-10T14:30:10+02:00")
	if _, err := h.queries.ClaimNotificationRun(context.Background(), store.ClaimNotificationRunParams{Owner: "other", LeaseUntil: at(h.clock.Add(time.Minute)), Now: at(h.clock)}); err != nil {
		t.Fatal(err)
	}
	if result, err := h.dispatcher.RunOnce(context.Background()); err != nil || !result.Busy {
		t.Fatalf("run while another holds the lease = %+v, %v", result, err)
	}
	h.expectSent()
	// Its lease expires (it stopped): this process takes over.
	h.runAt("2026-10-10T14:31:20+02:00")
	h.expectSent("Call mom: Due in 29 minutes · 15:00 (late)")

	// Two processes running at once send each notification once.
	h.create("Pay bills", "2026-10-10T16:00:00+02:00")
	other := h.newDispatcher()
	h.clock = instant("2026-10-10T15:30:10+02:00")
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
	h.expectSent("Pay bills: Due in 30 minutes · 16:00")
	if results[0].Sent+results[1].Sent != 1 {
		t.Fatalf("results = %+v", results)
	}
}

func TestMigrationDownAndUp(t *testing.T) {
	h := newHarness(t, DefaultSchedule())
	h.create("Call mom", "2026-10-10T15:00:00+02:00")
	h.runAt("2026-10-10T12:00:00+02:00")
	var up, down string
	for _, path := range testdb.Migrations(t) {
		if strings.HasPrefix(filepath.Base(path), "000004_") {
			up = path
			down = strings.TrimSuffix(path, ".up.sql") + ".down.sql"
		}
	}
	if up == "" {
		t.Fatal("migration 000004 not found")
	}
	testdb.Apply(t, h.pool, down)
	var tables int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name LIKE 'task_notification_%'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("tables after down = %d, %v", tables, err)
	}
	var tasks int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM tasks`).Scan(&tasks); err != nil || tasks != 1 {
		t.Fatalf("tasks after down = %d, %v", tasks, err)
	}
	testdb.Apply(t, h.pool, up)
	// Delivery starts over: the first run starts the cursor again.
	if result := h.runAt("2026-10-10T14:30:10+02:00"); result.Sent != 0 {
		t.Fatalf("first run after up = %+v", result)
	}
	h.runAt("2026-10-10T14:30:40+02:00")
	h.expectSent()
}
