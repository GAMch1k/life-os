package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/calendar"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/calendar/fakecalendar"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/feeds"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/ics"
	store "github.com/GAMch1k/life-os/services/calendar-feeds/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/testdb"
)

// fixedNow is Saturday 10 October 2026, 12:00 in Berlin (CEST).
var fixedNow = time.Date(2026, time.October, 10, 10, 0, 0, 0, time.UTC)

var berlin, _ = time.LoadLocation("Europe/Berlin")

// fiberTestConfig leaves a database round trip more than Fiber's 1s default.
var fiberTestConfig = fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true}

// feedServer serves an ICS feed the test rewrites between refreshes.
type feedServer struct {
	mu          sync.Mutex
	body        string
	status      int
	etag        string
	requests    int
	notModified int
}

func (f *feedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if f.status != 0 && f.status != http.StatusOK {
		w.WriteHeader(f.status)
		return
	}
	if f.etag != "" {
		if r.Header.Get("If-None-Match") == f.etag {
			f.notModified++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", f.etag)
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	_, _ = io.WriteString(w, f.body)
}

func (f *feedServer) set(change func(*feedServer)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

// fixture is the sanitized ZAW-style feed: 29 pickups between 14 October
// and 31 December 2026, 06:00-07:00 Berlin time.
type fixture struct {
	head, tail string
	events     []string
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "ics", "testdata", "zaw-sample.ics"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	first, last := strings.Index(text, "BEGIN:VEVENT"), strings.LastIndex(text, "END:VEVENT\r\n")+len("END:VEVENT\r\n")
	f := fixture{head: text[:first], tail: text[last:]}
	for _, block := range strings.SplitAfter(text[first:last], "END:VEVENT\r\n") {
		if block != "" {
			f.events = append(f.events, block)
		}
	}
	if len(f.events) != 29 {
		t.Fatalf("fixture has %d events", len(f.events))
	}
	return f
}

// feed renders the fixture, leaving out the pickups on the given dates
// (YYYYMMDD) and applying edits (old -> new text) to the rest.
func (f fixture) feed(without []string, edits ...string) string {
	var b strings.Builder
	b.WriteString(f.head)
	for _, event := range f.events {
		skip := false
		for _, date := range without {
			if strings.Contains(event, "DTSTART;TZID=Europe/Berlin:"+date+"T") {
				skip = true
			}
		}
		if skip {
			continue
		}
		for index := 0; index+1 < len(edits); index += 2 {
			event = strings.ReplaceAll(event, edits[index], edits[index+1])
		}
		b.WriteString(event)
	}
	b.WriteString(f.tail)
	return b.String()
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

// harness is the feeds API over a throwaway database, a fake Calendar, and
// an Activity recorder, on a clock the test moves.
type harness struct {
	t         *testing.T
	pool      *pgxpool.Pool
	clock     *time.Time
	calendar  *fakecalendar.Fake
	refresher *feeds.Refresher
	queries   *store.Queries
	app       *httpserver.App
	activity  *activityRecorder
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// newHarness starts the service; a restart passes the previous harness's
// pool, clock, and Calendar.
func newHarness(t *testing.T, previous *harness) *harness {
	t.Helper()
	h := &harness{t: t}
	if previous != nil {
		h.pool, h.clock, h.calendar = previous.pool, previous.clock, previous.calendar
	} else {
		h.pool = testdb.Open(t)
		start := fixedNow
		h.clock = &start
		h.calendar = fakecalendar.New(func() time.Time { return *h.clock }, berlin)
	}
	calendarServer := httptest.NewServer(h.calendar)
	t.Cleanup(calendarServer.Close)
	h.activity = &activityRecorder{}
	activityServer := httptest.NewServer(h.activity)
	t.Cleanup(activityServer.Close)
	schedule, err := feeds.ParseSchedule("04:30", berlin)
	if err != nil {
		t.Fatal(err)
	}
	// The feed server listens on loopback over http, which only the
	// development fetcher reaches.
	h.refresher = feeds.NewRefresher(h.pool, ics.NewFetcher(0, 5*time.Second, true), calendar.New(calendarServer.URL), schedule,
		activity.NewEmitter(activityServer.URL, "calendar-feeds", discard), discard)
	clock := h.clock
	h.refresher.SetClock(func() time.Time { return *clock })
	h.queries = store.New(h.pool)
	h.app = httpserver.New("calendar-feeds", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	Register(h.app, NewHandler(h.queries, h.refresher, nil))
	return h
}

// do sends a request (writes as dev:test) and returns status and body.
func (h *harness) do(method, target string, body any) (int, []byte) {
	h.t.Helper()
	var reader io.Reader
	switch value := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(value)
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			h.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("content-type", "application/json")
	req.Header.Set(activity.ActorHeader, "dev:test")
	resp, err := h.app.Test(req, fiberTestConfig)
	if err != nil {
		h.t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp.StatusCode, raw
}

func (h *harness) feed(method, target string, body any, wantStatus int) generated.Feed {
	h.t.Helper()
	status, raw := h.do(method, target, body)
	if status != wantStatus {
		h.t.Fatalf("%s %s: %d %s, want %d", method, target, status, raw, wantStatus)
	}
	var out generated.Feed
	if err := json.Unmarshal(raw, &out); err != nil {
		h.t.Fatalf("%s %s: %v in %s", method, target, err, raw)
	}
	return out
}

func (h *harness) get(id types.UUID) generated.Feed {
	h.t.Helper()
	return h.feed(http.MethodGet, "/v1/feeds/"+id.String(), nil, http.StatusOK)
}

func (h *harness) refresh(id types.UUID) generated.Feed {
	h.t.Helper()
	return h.feed(http.MethodPost, "/v1/feeds/"+id.String()+"/refresh", nil, http.StatusOK)
}

func (h *harness) runDue() {
	feeds.NewScheduler(h.refresher, time.Minute, discard).RunDue(context.Background())
}

// source is the feed's source in the fake Calendar.
func (h *harness) source(id types.UUID) fakecalendar.Source {
	h.t.Helper()
	source, ok := h.calendar.Source(id.String())
	if !ok {
		h.t.Fatalf("Calendar has no source for feed %s", id)
	}
	return source
}

func counts(t *testing.T, feed generated.Feed) generated.FeedRefreshCounts {
	t.Helper()
	if feed.Refresh.LastCounts == nil {
		t.Fatalf("no counts: %+v", feed.Refresh)
	}
	return *feed.Refresh.LastCounts
}

func outcome(feed generated.Feed) string {
	if feed.Refresh.LastOutcome == nil {
		return ""
	}
	return string(*feed.Refresh.LastOutcome)
}

// uidAt is the uid of the source's event starting at start.
func uidAt(t *testing.T, source fakecalendar.Source, start string) string {
	t.Helper()
	for uid, event := range source.Events {
		if event.Item.StartAt != nil && event.Item.StartAt.Format(time.RFC3339) == start {
			return uid
		}
	}
	t.Fatalf("no event starting %s", start)
	return ""
}

func eventAt(t *testing.T, source fakecalendar.Source, start string) fakecalendar.Event {
	t.Helper()
	return *source.Events[uidAt(t, source, start)]
}

var zawFilter = map[string]any{"include": []string{"Bioabfall", "Gelber Sack", "Papier", "Restmüll Tonnen und Container 14-täglich"}, "exclude": []string{}}
