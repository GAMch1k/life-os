package reminders

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/notify"
	"github.com/GAMch1k/life-os/services/calendar/internal/api"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/testdb"
)

// These tests run the dispatcher against a fresh, fully migrated schema on
// CALENDAR_TEST_DATABASE_URL, with events written through the real API,
// an in-process Notifications stand-in that deduplicates by key like the
// real service, and an Activity recorder. The owner lives in Berlin; the
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

func (a *activityRecorder) all() []activity.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]activity.Event(nil), a.events...)
}

type harness struct {
	t          *testing.T
	pool       *pgxpool.Pool
	queries    *store.Queries
	handler    *api.Handler
	app        *httpserver.App
	dispatcher *Dispatcher
	notes      *notificationsServer
	notesURL   string
	activity   *activityRecorder
	clock      time.Time
	seen       int
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newHarness(t *testing.T) *harness {
	t.Helper()
	pool := testdb.Open(t, "")
	h := &harness{t: t, pool: pool, queries: store.New(pool), notes: &notificationsServer{stored: map[string]bool{}}, activity: &activityRecorder{}}
	notes := httptest.NewServer(h.notes)
	t.Cleanup(notes.Close)
	activityServer := httptest.NewServer(h.activity)
	t.Cleanup(activityServer.Close)
	h.notesURL = notes.URL
	h.handler = api.NewHandler(pool, "Europe/Berlin")
	h.dispatcher = h.newDispatcher(activity.NewEmitter(activityServer.URL, "calendar", quiet))
	h.app = httpserver.New("calendar", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	api.Register(h.app, h.handler.WithReminders(h.dispatcher))
	return h
}

// newDispatcher is another dispatcher (another Calendar process) on the
// harness's database, clock, and Notifications.
func (h *harness) newDispatcher(emitter *activity.Emitter) *Dispatcher {
	return New(h.pool, h.handler, notify.New(h.notesURL, "calendar"), emitter, Config{Interval: 30 * time.Second, Grace: 2 * time.Hour, Owner: berlin}, quiet).
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

func (h *harness) event(method, target string, body any, want int) generated.CalendarEvent {
	h.t.Helper()
	status, raw := h.do(method, target, body)
	if status != want {
		h.t.Fatalf("%s %s: %d %s, want %d", method, target, status, raw, want)
	}
	var out generated.CalendarEvent
	if want != http.StatusNoContent {
		if err := json.Unmarshal(raw, &out); err != nil {
			h.t.Fatalf("%s %s: %v", method, target, err)
		}
	}
	return out
}

// create adds a timed Life OS event with its own reminders only.
func (h *harness) create(title, start string, minutes int, reminders ...map[string]any) generated.CalendarEvent {
	h.t.Helper()
	begin := instant(start)
	return h.event(http.MethodPost, "/v1/events", map[string]any{
		"title": title, "allDay": false, "startAt": begin, "endAt": begin.Add(time.Duration(minutes) * time.Minute),
		"reminders": reminders, "useDefaultReminders": false,
	}, http.StatusCreated)
}

func rel(minutes int) map[string]any {
	return map[string]any{"kind": "relative", "minutesBefore": minutes}
}
func local(days int, clock string) map[string]any {
	return map[string]any{"kind": "localTime", "daysBefore": days, "localTime": clock}
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
		got = append(got, body["title"].(string)+": "+body["body"].(string))
	}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		h.t.Fatalf("sent %q, want %q", got, want)
	}
	return fresh
}

// settings replaces the owner's default reminder lists given.
func (h *harness) settings(defaults map[string]any) {
	h.t.Helper()
	if status, raw := h.do(http.MethodPatch, "/v1/settings", map[string]any{"defaultReminders": defaults}); status != http.StatusOK {
		h.t.Fatalf("settings: %d %s", status, raw)
	}
}

func (h *harness) status() generated.ReminderDeliveryStatus {
	h.t.Helper()
	status, raw := h.do(http.MethodGet, "/v1/reminders/status", nil)
	if status != http.StatusOK {
		h.t.Fatalf("status: %d %s", status, raw)
	}
	var out generated.ReminderDeliveryStatus
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

func TestEveryEffectiveReminderFiresOnce(t *testing.T) {
	h := newHarness(t)
	if status := h.status(); !status.Enabled || status.IntervalSeconds != 30 || status.GraceMinutes != 120 || status.HandledThrough != nil {
		t.Fatalf("status before the first run = %+v", status)
	}
	// The owner's default 30 minutes plus the event's own day and 3 hours.
	h.settings(map[string]any{"timed": []any{rel(30)}})
	begin := instant("2026-10-11T13:00:00Z")
	dentist := h.event(http.MethodPost, "/v1/events", map[string]any{
		"title": "Dentist", "allDay": false, "startAt": begin, "endAt": begin.Add(time.Hour), "location": "Dr. Weber",
		"reminders": []any{rel(1440), rel(180)},
	}, http.StatusCreated)

	// The first run only starts the cursor.
	if result := h.runAt("2026-10-10T10:00:00Z"); result.Sent != 0 {
		t.Fatalf("first run = %+v", result)
	}
	h.runAt("2026-10-10T12:59:50Z")
	h.expectSent()
	h.runAt("2026-10-10T13:00:10Z")
	fresh := h.expectSent("Dentist: Tomorrow at 15:00 · Dr. Weber")
	want := map[string]any{
		"importance": "medium", "category": "calendar.reminder", "link": "/calendar?date=2026-10-11", "occurredAt": "2026-10-10T13:00:00Z",
		"dedupeKey": "calendar:reminder:" + dentist.Id.String() + ":single:2026-10-11T13:00:00Z:rel:1440",
	}
	for key, value := range want {
		if fresh[0][key] != value {
			t.Errorf("%s = %v, want %v", key, fresh[0][key], value)
		}
	}
	h.runAt("2026-10-10T13:00:40Z")
	h.expectSent()
	h.runAt("2026-10-11T10:00:20Z")
	h.expectSent("Dentist: In 3 hours · 15:00–16:00 · Dr. Weber")
	h.runAt("2026-10-11T12:30:05Z")
	h.expectSent("Dentist: In 30 minutes · 15:00–16:00 · Dr. Weber")
	h.runAt("2026-10-11T13:00:05Z")
	h.expectSent()
	if created, requests := h.notes.count(); created != 3 || requests != 3 {
		t.Fatalf("created %d in %d requests, want 3 in 3", created, requests)
	}
	for _, actor := range h.notes.actors {
		if actor != "system:calendar" {
			t.Fatalf("actor = %q", actor)
		}
	}
	status := h.status()
	if !status.Enabled || status.HandledThrough == nil || !status.HandledThrough.Equal(instant("2026-10-11T13:00:05Z")) ||
		status.LastSentAt == nil || !status.LastSentAt.Equal(instant("2026-10-11T12:30:05Z")) || status.LastError != nil || status.LastSuccessAt == nil {
		t.Fatalf("status = %+v", status)
	}
	if events := h.activity.all(); len(events) != 0 {
		t.Fatalf("sent reminders record nothing in Activity (Notifications does), got %+v", events)
	}
}

func TestRecurringOccurrencesFollowTheirExceptions(t *testing.T) {
	h := newHarness(t)
	begin := instant("2026-10-12T05:00:00Z") // 07:00 in Berlin
	gym := h.event(http.MethodPost, "/v1/events", map[string]any{
		"title": "Gym", "allDay": false, "startAt": begin, "endAt": begin.Add(time.Hour),
		"recurrence": map[string]any{"frequency": "daily", "interval": 1, "count": 4},
		"reminders":  []any{rel(60)}, "useDefaultReminders": false,
	}, http.StatusCreated)
	occurrence := func(rid string) string { return "/v1/events/" + gym.Id.String() + "/occurrences/" + rid }
	// Skip the second, move the third to 09:00 with its own reminder.
	h.event(http.MethodPatch, occurrence("2026-10-13T05:00:00Z"), map[string]any{"status": "cancelled"}, http.StatusOK)
	h.event(http.MethodPatch, occurrence("2026-10-14T05:00:00Z"), map[string]any{
		"startAt": "2026-10-14T07:00:00Z", "endAt": "2026-10-14T08:00:00Z", "reminders": []any{rel(15)},
	}, http.StatusOK)

	h.runAt("2026-10-10T10:00:00Z")
	h.runAt("2026-10-12T04:00:10Z")
	first := h.expectSent("Gym: In 1 hour · 07:00–08:00")
	if key := first[0]["dedupeKey"]; key != "calendar:reminder:"+gym.Id.String()+":2026-10-12T05:00:00Z:2026-10-12T05:00:00Z:rel:60" {
		t.Fatalf("key = %v", key)
	}
	h.runAt("2026-10-13T04:00:10Z")
	h.expectSent() // skipped
	h.runAt("2026-10-14T04:00:10Z")
	h.expectSent() // moved: its own reminder is 15 minutes before 09:00
	h.runAt("2026-10-14T06:45:10Z")
	moved := h.expectSent("Gym: In 15 minutes · 09:00–10:00")
	if key := moved[0]["dedupeKey"]; key != "calendar:reminder:"+gym.Id.String()+":2026-10-14T05:00:00Z:2026-10-14T07:00:00Z:rel:15" {
		t.Fatalf("moved key = %v", key)
	}
	if link := moved[0]["link"]; link != "/calendar?date=2026-10-14" {
		t.Fatalf("link = %v", link)
	}
	h.runAt("2026-10-15T04:00:10Z")
	h.expectSent("Gym: In 1 hour · 07:00–08:00")
	h.runAt("2026-10-16T04:00:10Z")
	h.expectSent() // the count of 4 is over
}

func TestAllDayAndImportedEventsUseTheirDefaults(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var zaw, club googleuuid.UUID
	if err := h.pool.QueryRow(ctx, `INSERT INTO calendar_sources (kind, name, url, default_reminders, preset)
		VALUES ('ics', 'ZAW', 'https://feeds.example.test/zaw.ics', '[{"kind":"localTime","daysBefore":1,"localTime":"19:00"}]', 'zaw') RETURNING id`).Scan(&zaw); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(ctx, `INSERT INTO calendar_sources (kind, name, url, default_reminders)
		VALUES ('ics', 'Club', 'https://feeds.example.test/club.ics', '[{"kind":"relative","minutesBefore":60}]') RETURNING id`).Scan(&club); err != nil {
		t.Fatal(err)
	}
	imported := `INSERT INTO events (title, location, all_day, start_at, end_at, source_kind, source_id, external_uid, status)
		VALUES ($1, $2, false, $3, $4, 'ics', $5, $6, $7)`
	h.exec(imported, "Papier", "Roßdorf, Darmstädter Str.", instant("2026-10-14T04:00:00Z"), instant("2026-10-14T04:15:00Z"), zaw, "zaw-1", "active")
	h.exec(imported, "Bioabfall", "Roßdorf, Darmstädter Str.", instant("2026-10-15T04:00:00Z"), instant("2026-10-15T04:15:00Z"), zaw, "zaw-2", "active")
	h.exec(`UPDATE events SET status = 'cancelled', cancelled_at = now(), removed_from_source_at = now() WHERE external_uid = 'zaw-2'`)
	h.exec(imported, "Match", "Stadium", instant("2026-10-13T16:00:00Z"), instant("2026-10-13T18:00:00Z"), club, "club-1", "active")
	// The owner's all-day default plus the event's own reminder on the day.
	h.settings(map[string]any{"allDay": []any{local(1, "19:00")}})
	h.event(http.MethodPost, "/v1/events", map[string]any{
		"title": "Birthday", "allDay": true, "startDate": "2026-10-15", "endDate": "2026-10-15", "reminders": []any{local(0, "08:00")},
	}, http.StatusCreated)
	h.event(http.MethodPost, "/v1/events", map[string]any{
		"title": "Holiday", "allDay": true, "startDate": "2026-10-16", "endDate": "2026-10-18", "reminders": []any{rel(0)}, "useDefaultReminders": false,
	}, http.StatusCreated)

	h.runAt("2026-10-10T10:00:00Z")
	h.runAt("2026-10-13T15:00:10Z")
	h.expectSent("Match: In 1 hour · 18:00–20:00 · Stadium")
	h.runAt("2026-10-13T17:00:10Z")
	h.expectSent("Papier: Tomorrow at 06:00") // ZAW: no street address
	h.runAt("2026-10-14T17:00:10Z")
	h.expectSent("Birthday: Tomorrow, all day") // the cancelled pickup sends nothing
	h.runAt("2026-10-15T06:00:10Z")
	h.expectSent("Birthday: Today, all day")
	h.runAt("2026-10-15T22:00:10Z") // midnight in Berlin
	h.expectSent("Holiday: Today until Sun 18 Oct")
}

func TestGoogleSyncedEventsRemindToo(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// A synced Google calendar is a source of kind google; its default
	// reminders are the calendar's (10 minutes here).
	var connection, calendar, source googleuuid.UUID
	if err := h.pool.QueryRow(ctx, `INSERT INTO google_connections (account_email, refresh_token_ciphertext, refresh_token_nonce)
		VALUES ('owner@example.com', '\x00', '\x00') RETURNING id`).Scan(&connection); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(ctx, `INSERT INTO google_calendars (connection_id, google_calendar_id, summary, access_role, is_primary, sync_enabled)
		VALUES ($1, 'owner@example.com', 'Personal', 'owner', true, true) RETURNING id`, connection).Scan(&calendar); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(ctx, `INSERT INTO calendar_sources (kind, name, url, default_reminders, google_calendar_id)
		VALUES ('google', 'Personal', 'https://www.googleapis.com/calendar/v3/calendars/owner@example.com', '[{"kind":"relative","minutesBefore":10}]', $1) RETURNING id`, calendar).Scan(&source); err != nil {
		t.Fatal(err)
	}
	// Following the defaults plus its own Google override of an hour.
	h.exec(`INSERT INTO events (title, location, all_day, start_at, end_at, source_kind, source_id, external_uid, reminders, use_default_reminders)
		VALUES ('Dentist', 'Dr. Weber', false, $1, $2, 'google', $3, 'g-1', '[{"kind":"relative","minutesBefore":60}]', true)`,
		instant("2026-10-12T08:00:00Z"), instant("2026-10-12T09:00:00Z"), source)
	// A weekly Google series whose second occurrence was cancelled in Google.
	var series googleuuid.UUID
	if err := h.pool.QueryRow(ctx, `INSERT INTO events (title, all_day, start_at, end_at, source_kind, source_id, external_uid, recurrence_frequency, recurrence_interval, recurrence_count)
		VALUES ('Team call', false, $1, $2, 'google', $3, 'g-2', 'weekly', 1, 3) RETURNING id`,
		instant("2026-10-13T07:00:00Z"), instant("2026-10-13T07:30:00Z"), source).Scan(&series); err != nil {
		t.Fatal(err)
	}
	h.exec(`INSERT INTO event_exceptions (event_id, recurrence_id, cancelled_at) VALUES ($1, '2026-10-20T07:00:00Z', now())`, series)

	h.runAt("2026-10-10T10:00:00Z")
	h.runAt("2026-10-12T07:00:10Z")
	h.expectSent("Dentist: In 1 hour · 10:00–11:00 · Dr. Weber")
	h.runAt("2026-10-12T07:50:10Z")
	h.expectSent("Dentist: In 10 minutes · 10:00–11:00 · Dr. Weber")
	h.runAt("2026-10-13T06:50:10Z")
	call := h.expectSent("Team call: In 10 minutes · 09:00–09:30")
	if key := call[0]["dedupeKey"]; key != "calendar:reminder:"+series.String()+":2026-10-13T07:00:00Z:2026-10-13T07:00:00Z:rel:10" {
		t.Fatalf("key = %v", key)
	}
	h.runAt("2026-10-20T06:50:10Z")
	h.expectSent() // cancelled in Google
	// The series keeps its wall clock after the change to winter time.
	h.runAt("2026-10-27T07:50:10Z")
	h.expectSent("Team call: In 10 minutes · 09:00–09:30")
}

func TestRemindersDueWhileDownAreSentLateOrRecordedAsMissed(t *testing.T) {
	h := newHarness(t)
	a := h.create("Standup", "2026-10-10T11:00:00Z", 15, rel(50)) // due 10:10
	h.create("Dentist", "2026-10-10T13:00:00Z", 60, rel(60))      // due 12:00
	h.create("Review", "2026-10-10T14:00:00Z", 30, rel(30))       // due 13:30
	h.runAt("2026-10-10T10:00:00Z")

	// Calendar was down from 10:00 to 12:30: 10:10 is past the 2-hour
	// grace window, 12:00 is not.
	result := h.runAt("2026-10-10T12:30:00Z")
	if result.Missed != 1 || result.Sent != 1 || result.Late != 1 {
		t.Fatalf("result = %+v", result)
	}
	h.expectSent("Dentist: In 30 minutes · 15:00–16:00 (late)")
	events := h.activity.all()
	if len(events) != 1 {
		t.Fatalf("activity = %+v", events)
	}
	missed := events[0]
	key := "calendar:reminder:" + a.Id.String() + ":single:2026-10-10T11:00:00Z:rel:50"
	if missed.Type != "calendar.reminder.missed" || missed.ID != activity.EventID("calendar.reminder.missed", key) ||
		missed.Subject != (activity.Subject{Type: "calendar_event", ID: a.Id.String()}) || missed.Outcome != activity.Failed ||
		missed.Actor != activity.System("calendar") || !missed.OccurredAt.Equal(instant("2026-10-10T10:10:00Z")) ||
		missed.Data["dedupeKey"] != key || missed.Data["title"] != "Standup" ||
		missed.Summary != "Reminder for “Standup” due Sat 10 Oct 2026, 12:10 CEST was missed: Calendar was not running" {
		t.Fatalf("missed = %+v", missed)
	}

	h.runAt("2026-10-10T12:30:30Z")
	h.expectSent()
	// A run whose cursor was lost (as if its finish never happened)
	// neither repeats nor reclassifies anything.
	h.exec(`UPDATE calendar_reminder_dispatch SET last_run_at = $1`, instant("2026-10-10T10:00:00Z"))
	if result := h.runAt("2026-10-10T12:31:00Z"); result.Handled != 2 || result.Sent+result.Missed != 0 {
		t.Fatalf("rerun = %+v", result)
	}
	h.expectSent()
	if len(h.activity.all()) != 1 {
		t.Fatal("a missed reminder is recorded once")
	}
	h.runAt("2026-10-10T13:30:10Z")
	h.expectSent("Review: In 30 minutes · 16:00–16:30")
}

func TestNotificationFailures(t *testing.T) {
	h := newHarness(t)
	h.create("Dentist", "2026-10-10T13:00:00Z", 60, rel(30)) // due 12:30
	h.create("Call", "2026-10-10T13:00:00Z", 30, rel(20))    // due 12:40
	h.runAt("2026-10-10T10:00:00Z")

	// Notifications is failing: the reminder is retried, not lost.
	h.notes.failNext(http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusServiceUnavailable)
	h.clock = instant("2026-10-10T12:30:10Z")
	if result, err := h.dispatcher.RunOnce(context.Background()); err == nil || result.Sent != 0 {
		t.Fatalf("run against a failing Notifications = %+v, %v", result, err)
	}
	status := h.status()
	if status.LastError == nil || !strings.Contains(*status.LastError, "503") || status.LastErrorAt == nil ||
		status.HandledThrough == nil || !status.HandledThrough.Equal(instant("2026-10-10T12:29:59.999999Z")) {
		t.Fatalf("status after the failure = %+v", status)
	}
	// A minute after it was due is still on time.
	h.runAt("2026-10-10T12:31:00Z")
	h.expectSent("Dentist: In 30 minutes · 15:00–16:00")
	if created, requests := h.notes.count(); created != 1 || requests != 4 {
		t.Fatalf("created %d in %d requests", created, requests)
	}

	// A notification Notifications refuses is not retried.
	h.notes.failNext(http.StatusBadRequest)
	h.clock = instant("2026-10-10T12:40:10Z")
	if result, err := h.dispatcher.RunOnce(context.Background()); err == nil || result.Failed != 1 {
		t.Fatalf("refused run = %+v, %v", result, err)
	}
	h.runAt("2026-10-10T12:41:00Z")
	h.expectSent()
	if _, requests := h.notes.count(); requests != 5 {
		t.Fatalf("requests = %d, want no retry of the refused reminder", requests)
	}
	status = h.status()
	if status.LastError == nil || !strings.Contains(*status.LastError, "400") || !status.LastSuccessAt.After(*status.LastErrorAt) {
		t.Fatalf("status = %+v", status)
	}
}

func TestChangesTakeEffectOnTheNextRun(t *testing.T) {
	h := newHarness(t)
	dentist := h.create("Dentist", "2026-10-10T13:00:00Z", 60, rel(30))
	h.runAt("2026-10-10T10:00:00Z")
	h.runAt("2026-10-10T12:30:10Z")
	h.expectSent("Dentist: In 30 minutes · 15:00–16:00")

	// Moved: it reminds again at its new time.
	h.event(http.MethodPatch, "/v1/events/"+dentist.Id.String(), map[string]any{"startAt": "2026-10-10T15:00:00Z", "endAt": "2026-10-10T16:00:00Z"}, http.StatusOK)
	h.runAt("2026-10-10T14:30:10Z")
	h.expectSent("Dentist: In 30 minutes · 17:00–18:00")

	call := h.create("Call", "2026-10-10T16:00:00Z", 30, rel(30))
	h.event(http.MethodPatch, "/v1/events/"+call.Id.String(), map[string]any{"status": "cancelled"}, http.StatusOK)
	lunch := h.create("Lunch", "2026-10-10T17:00:00Z", 60, rel(30))
	if status, raw := h.do(http.MethodDelete, "/v1/events/"+lunch.Id.String(), nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", status, raw)
	}
	review := h.create("Review", "2026-10-10T18:00:00Z", 30)
	h.runAt("2026-10-10T15:30:10Z")
	h.runAt("2026-10-10T16:30:10Z")
	h.expectSent() // cancelled and deleted events send nothing

	// A reminder added before it is due fires.
	h.event(http.MethodPatch, "/v1/events/"+review.Id.String(), map[string]any{"reminders": []any{rel(30)}}, http.StatusOK)
	h.runAt("2026-10-10T17:30:10Z")
	h.expectSent("Review: In 30 minutes · 20:00–20:30")
}

func TestOneProcessRunsAtATime(t *testing.T) {
	h := newHarness(t)
	h.create("Dentist", "2026-10-10T13:00:00Z", 60, rel(30))
	h.runAt("2026-10-10T10:00:00Z")

	// Another process holds the lease.
	h.clock = instant("2026-10-10T12:30:10Z")
	if _, err := h.queries.ClaimReminderRun(context.Background(), store.ClaimReminderRunParams{Owner: "other", LeaseUntil: at(h.clock.Add(time.Minute)), Now: at(h.clock)}); err != nil {
		t.Fatal(err)
	}
	if result, err := h.dispatcher.RunOnce(context.Background()); err != nil || !result.Busy {
		t.Fatalf("run while another holds the lease = %+v, %v", result, err)
	}
	h.expectSent()
	// Its lease expires (it stopped): this process takes over.
	h.runAt("2026-10-10T12:31:20Z")
	h.expectSent("Dentist: In 29 minutes · 15:00–16:00 (late)")

	// Two processes running at once send each reminder once.
	h.create("Call", "2026-10-10T14:00:00Z", 30, rel(30))
	other := h.newDispatcher(nil)
	h.clock = instant("2026-10-10T13:30:10Z")
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
	h.expectSent("Call: In 30 minutes · 16:00–16:30")
	if results[0].Sent+results[1].Sent != 1 {
		t.Fatalf("results = %+v", results)
	}
}

func TestMigrationDownAndUp(t *testing.T) {
	h := newHarness(t)
	h.create("Dentist", "2026-10-10T13:00:00Z", 60, rel(30))
	h.runAt("2026-10-10T10:00:00Z")
	var up, down string
	for _, path := range testdb.Migrations(t) {
		if strings.HasPrefix(filepath.Base(path), "000012_") {
			up = path
			down = strings.TrimSuffix(path, ".up.sql") + ".down.sql"
		}
	}
	if up == "" {
		t.Fatal("migration 000012 not found")
	}
	testdb.Apply(t, h.pool, down)
	var tables int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name LIKE 'calendar_reminder_%'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("tables after down = %d, %v", tables, err)
	}
	var events int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM events`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("events after down = %d, %v", events, err)
	}
	testdb.Apply(t, h.pool, up)
	// Delivery starts over: the first run starts the cursor again.
	if result := h.runAt("2026-10-10T12:30:10Z"); result.Sent != 0 {
		t.Fatalf("first run after up = %+v", result)
	}
	h.runAt("2026-10-10T12:30:40Z")
	h.expectSent()
}
