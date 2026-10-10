package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/notify"
)

// dashboard serves GET /api/status like the Dashboard, with the checks the
// test sets; failing answers 502 as a gateway in front of a stopped
// Dashboard would.
type dashboard struct {
	mu      sync.Mutex
	checks  map[string]string // id → state
	failing bool
}

func (d *dashboard) set(id, state string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.checks[id] = state
}

func (d *dashboard) fail(failing bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failing = failing
}

func (d *dashboard) serve(t *testing.T) *httptest.Server {
	titles := map[string]string{"processing": "Automatic processing", "inbox": "Inbox backlog", "services": "Services"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			http.NotFound(w, r)
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.failing {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
			return
		}
		checks := []map[string]any{}
		for _, id := range []string{"processing", "inbox", "services"} {
			state := d.checks[id]
			check := map[string]any{"id": id, "title": titles[id], "state": state, "summary": titles[id] + " is " + state + ".", "facts": []any{}}
			if state != "ok" {
				check["action"] = "Do something about " + id + "."
			}
			checks = append(checks, check)
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"checkedAt": "2026-10-10T12:00:00.000Z", "timeZone": "Europe/Berlin", "overall": "ok", "checks": checks})
	}))
	t.Cleanup(server.Close)
	return server
}

func newDashboard() *dashboard {
	return &dashboard{checks: map[string]string{"processing": "ok", "inbox": "ok", "services": "ok"}}
}

type stored struct {
	notify.Sent
	Body  string `json:"body"`
	Link  string `json:"link"`
	Actor string `json:"-"`
}

// notifications imitates the Notifications service's send (with dedupe) and
// list (newest first, category prefix and source filters). down makes every
// request fail with 503.
type notifications struct {
	mu    sync.Mutex
	items []stored
	posts int
	down  bool
	sent  chan stored
}

func (n *notifications) setDown(down bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.down = down
}

func (n *notifications) all() []stored {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]stored(nil), n.items...)
}

func (n *notifications) serve(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.mu.Lock()
		defer n.mu.Unlock()
		w.Header().Set("content-type", "application/json")
		if n.down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodPost:
			n.posts++
			var item stored
			if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
				t.Errorf("decode notification: %v", err)
			}
			item.Actor = r.Header.Get(activity.ActorHeader)
			item.Source = strings.TrimPrefix(item.Actor, "system:")
			item.CreatedAt = time.Now()
			for _, existing := range n.items {
				if existing.DedupeKey == item.DedupeKey {
					_ = json.NewEncoder(w).Encode(existing)
					return
				}
			}
			n.items = append(n.items, item)
			if n.sent != nil {
				n.sent <- item
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(item)
		case http.MethodGet:
			query := r.URL.Query()
			result := []stored{}
			for i := len(n.items) - 1; i >= 0; i-- {
				item := n.items[i]
				category := query.Get("category")
				if (item.Category == category || strings.HasPrefix(item.Category, category+".")) && item.Source == query.Get("source") {
					result = append(result, item)
				}
			}
			if len(result) > 1 {
				result = result[:1]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": result})
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// clock is a manual clock: each evaluation in these tests is one minute.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fixture struct {
	dashboard     *dashboard
	notifications *notifications
	statusURL     string
	notifyURL     string
	clock         *clock
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{dashboard: newDashboard(), notifications: &notifications{}, clock: &clock{now: t0}}
	f.statusURL = f.dashboard.serve(t).URL + "/api/status"
	f.notifyURL = f.notifications.serve(t).URL
	return f
}

// monitor starts a monitor process against the fixture; a second call is a
// restart (a new process with empty memory).
func (f *fixture) monitor() *Monitor {
	return New(Options{
		Status:        &StatusClient{URL: f.statusURL, Timeout: 2 * time.Second},
		Notifier:      notify.New(f.notifyURL, "monitor"),
		Interval:      time.Minute,
		Confirmations: 2,
		Now:           f.clock.Now,
	})
}

// evaluate runs one evaluation, then lets a minute pass.
func (f *fixture) evaluate(m *Monitor, times int) {
	for range times {
		m.Evaluate(context.Background())
		f.clock.advance(time.Minute)
	}
}

func (f *fixture) expectNotifications(t *testing.T, want ...string) []stored {
	t.Helper()
	items := f.notifications.all()
	got := make([]string, len(items))
	for i, item := range items {
		got[i] = fmt.Sprintf("%s %s", item.Importance, item.Title)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("notifications:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	return items
}

func TestTheMonitorNotifiesAConfirmedProblemAndItsRecovery(t *testing.T) {
	f := newFixture(t)
	m := f.monitor()
	f.evaluate(m, 3)
	f.expectNotifications(t)

	f.dashboard.set("processing", "problem") // at 12:03
	f.evaluate(m, 1)
	f.expectNotifications(t)
	f.evaluate(m, 5)
	items := f.expectNotifications(t, "high Automatic processing has a problem")
	problem := items[0]
	if problem.Category != "status.processing" || problem.Link != "/status" || problem.Actor != "system:monitor" ||
		problem.Body != "Automatic processing is problem.\n\nDo something about processing." ||
		problem.DedupeKey != "status:processing:problem:2026-10-10T12:03:00Z" || !problem.OccurredAt.Equal(minute(3)) {
		t.Fatalf("problem notification = %+v", problem)
	}

	f.dashboard.set("processing", "ok") // at 12:09
	f.evaluate(m, 3)
	items = f.expectNotifications(t, "high Automatic processing has a problem", "low Automatic processing recovered")
	if recovered := items[1]; recovered.DedupeKey != "status:processing:ok:2026-10-10T12:09:00Z" ||
		recovered.Body != "Automatic processing is ok.\n\nBack to normal after about 6 min." {
		t.Fatalf("recovery = %+v", recovered)
	}
}

func TestTheMonitorNotifiesWarningsAsMedium(t *testing.T) {
	f := newFixture(t)
	m := f.monitor()
	f.dashboard.set("inbox", "warning")
	f.evaluate(m, 2)
	f.dashboard.set("inbox", "ok")
	f.evaluate(m, 2)
	f.expectNotifications(t, "medium Inbox backlog needs attention", "low Inbox backlog recovered")
}

func TestARestartNeitherRepeatsNorLosesAnEpisode(t *testing.T) {
	f := newFixture(t)
	f.dashboard.set("services", "problem")
	f.evaluate(f.monitor(), 2)
	f.expectNotifications(t, "high Services has a problem")

	// Restarted during the episode: nothing new.
	restarted := f.monitor()
	f.evaluate(restarted, 5)
	f.expectNotifications(t, "high Services has a problem")

	// Recovered while the monitor was restarting: the recovery still comes.
	f.dashboard.set("services", "ok")
	again := f.monitor()
	f.evaluate(again, 2)
	f.expectNotifications(t, "high Services has a problem", "low Services recovered")

	// Broke while the monitor was down: notified after the restart.
	f.dashboard.set("processing", "problem")
	f.evaluate(f.monitor(), 2)
	f.expectNotifications(t, "high Services has a problem", "low Services recovered", "high Automatic processing has a problem")
}

func TestAnUnreachableStatusIsAProblemOfItsOwn(t *testing.T) {
	f := newFixture(t)
	m := f.monitor()
	f.evaluate(m, 2)
	f.dashboard.fail(true)
	f.evaluate(m, 1)
	f.expectNotifications(t)
	f.evaluate(m, 1)
	items := f.expectNotifications(t, "high Life OS status is unreachable")
	if items[0].Category != "status.dashboard" || !strings.Contains(items[0].Body, "status endpoint returned 502") {
		t.Fatalf("notification = %+v", items[0])
	}
	f.dashboard.fail(false)
	f.evaluate(m, 2)
	f.expectNotifications(t, "high Life OS status is unreachable", "low Life OS status is reachable again")
}

func TestNotificationsBeingDownDelaysButNeverLosesOrRepeats(t *testing.T) {
	f := newFixture(t)
	f.notifications.setDown(true)
	m := f.monitor()
	f.dashboard.set("processing", "problem") // at 12:00
	f.evaluate(m, 4)
	f.expectNotifications(t)

	f.notifications.setDown(false)
	f.evaluate(m, 3)
	items := f.expectNotifications(t, "high Automatic processing has a problem")
	if items[0].DedupeKey != "status:processing:problem:2026-10-10T12:00:00Z" {
		t.Fatalf("the notification dates from the first observation: %s", items[0].DedupeKey)
	}
}

func TestASendThatSucceededButLookedFailedIsNotRepeated(t *testing.T) {
	f := newFixture(t)
	m := f.monitor()
	f.evaluate(m, 1) // read the history
	f.dashboard.set("processing", "problem")
	f.evaluate(m, 2)
	f.expectNotifications(t, "high Automatic processing has a problem")
	// The same transition sent again (as after a lost response) is
	// deduplicated by its key.
	transition := Transition{Check: Observation{Key: "processing", Title: "Automatic processing", State: Problem}, From: OK, Since: minute(1)}
	if err := notify.New(f.notifyURL, "monitor").Send(context.Background(), notification(transition)); err != nil {
		t.Fatal(err)
	}
	f.expectNotifications(t, "high Automatic processing has a problem")
}

func TestWithoutNotificationsTheMonitorOnlyLogs(t *testing.T) {
	f := newFixture(t)
	m := New(Options{Status: &StatusClient{URL: f.statusURL}, Notifier: notify.New("", "monitor"), Interval: time.Minute, Confirmations: 2, Now: f.clock.Now})
	if m.sending {
		t.Fatal("a client without a URL does not send")
	}
	f.dashboard.set("processing", "problem")
	f.evaluate(m, 3)
	if len(m.tracker.Due()) != 0 {
		t.Fatal("the change counts as handled")
	}
	f.expectNotifications(t)
}

func TestRunPollsOnTheInterval(t *testing.T) {
	f := newFixture(t)
	f.notifications.sent = make(chan stored, 4)
	f.dashboard.set("inbox", "problem")
	m := New(Options{
		Status:        &StatusClient{URL: f.statusURL, Timeout: time.Second},
		Notifier:      notify.New(f.notifyURL, "monitor"),
		Interval:      10 * time.Millisecond,
		Confirmations: 2,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Run(ctx)
	}()
	select {
	case item := <-f.notifications.sent:
		if item.Title != "Inbox backlog has a problem" {
			t.Errorf("notification = %+v", item)
		}
	case <-time.After(5 * time.Second):
		t.Error("no notification within 5 s of polling every 10 ms")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	if err := m.Ready(context.Background()); err != nil {
		t.Fatalf("Ready = %v", err)
	}
}

func TestReadyReportsStalledEvaluations(t *testing.T) {
	f := newFixture(t)
	m := f.monitor()
	if err := m.Ready(context.Background()); err != nil {
		t.Fatalf("a new monitor is ready: %v", err)
	}
	f.clock.advance(5 * time.Minute)
	if err := m.Ready(context.Background()); err == nil {
		t.Fatal("ready without an evaluation for five intervals")
	}
	m.Evaluate(context.Background())
	if err := m.Ready(context.Background()); err != nil {
		t.Fatalf("ready after an evaluation: %v", err)
	}
}

func TestFetchRefusesWhatIsNotARuntimeStatus(t *testing.T) {
	for name, answer := range map[string]struct {
		status int
		body   string
	}{
		"server error": {http.StatusInternalServerError, `{"checks":[]}`},
		"html":         {http.StatusOK, `<html>Life OS</html>`},
		"no checks":    {http.StatusOK, `{"checkedAt":"2026-10-10T12:00:00Z","checks":[]}`},
		"no id":        {http.StatusOK, `{"checks":[{"id":"","title":"Inbox backlog","state":"ok"}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(answer.status)
				_, _ = w.Write([]byte(answer.body))
			}))
			defer server.Close()
			if _, err := (&StatusClient{URL: server.URL}).Fetch(context.Background()); err == nil {
				t.Fatal("Fetch accepted it")
			}
		})
	}
}

func TestFetchHonoursItsTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	started := time.Now()
	if _, err := (&StatusClient{URL: server.URL, Timeout: 50 * time.Millisecond}).Fetch(context.Background()); err == nil {
		t.Fatal("Fetch did not time out")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Fetch took %s with a 50 ms timeout", elapsed)
	}
}
