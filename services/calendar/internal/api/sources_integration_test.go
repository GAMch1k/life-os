package api

import (
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

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/ics"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/sources"
	"github.com/GAMch1k/life-os/services/calendar/internal/testdb"
)

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

// sourcesClient is an apiClient whose handler refreshes sources against
// the feed server, on a clock the test moves, recording Activity events.
type sourcesClient struct {
	apiClient
	pool      *pgxpool.Pool
	clock     *time.Time
	refresher *sources.Refresher
	activity  *activityRecorder
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

func newSourcesClient(t *testing.T, pool *pgxpool.Pool, clock *time.Time) sourcesClient {
	t.Helper()
	if pool == nil {
		pool = testdb.Open(t, "")
	}
	if clock == nil {
		start := fixedNow
		clock = &start
	}
	recorder := &activityRecorder{}
	activityServer := httptest.NewServer(recorder)
	t.Cleanup(activityServer.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	berlin, _ := time.LoadLocation("Europe/Berlin")
	schedule, err := sources.ParseSchedule("04:30", berlin)
	if err != nil {
		t.Fatal(err)
	}
	// The feed server listens on loopback over http, which only the
	// development fetcher reaches.
	refresher := sources.NewRefresher(pool, ics.NewFetcher(0, 5*time.Second, true), schedule, activity.NewEmitter(activityServer.URL, "calendar", logger), logger)
	refresher.SetClock(func() time.Time { return *clock })
	queries := store.New(pool)
	handler := NewHandler(pool, "Europe/Berlin").WithSources(refresher, nil)
	handler.now = func() time.Time { return *clock }
	return sourcesClient{
		apiClient: apiClient{t: t, app: newTestApp(handler), handler: handler, queries: queries},
		pool:      pool, clock: clock, refresher: refresher, activity: recorder,
	}
}

func (c sourcesClient) source(method, target string, body any, wantStatus int) generated.CalendarSource {
	c.t.Helper()
	status, raw := c.do(method, target, body)
	if status != wantStatus {
		c.t.Fatalf("%s %s: %d %s, want %d", method, target, status, raw, wantStatus)
	}
	var out generated.CalendarSource
	if err := json.Unmarshal(raw, &out); err != nil {
		c.t.Fatalf("%s %s: %v in %s", method, target, err, raw)
	}
	return out
}

func (c sourcesClient) refresh(id googleuuid.UUID) generated.CalendarSource {
	c.t.Helper()
	return c.source(http.MethodPost, "/v1/sources/"+id.String()+"/refresh", nil, http.StatusOK)
}

// events lists the source's events from October 2026 to January 2027.
func (c sourcesClient) events(id googleuuid.UUID, includeCancelled bool) []generated.CalendarEvent {
	c.t.Helper()
	target := "/v1/events?" + q("from", "2026-10-01T00:00:00Z", "to", "2027-01-01T00:00:00Z", "sourceId", id.String(), "limit", "100")
	if includeCancelled {
		target += "&includeCancelled=true"
	}
	return c.list(target).Items
}

func findStart(t *testing.T, items []generated.CalendarEvent, start string) generated.CalendarEvent {
	t.Helper()
	for _, item := range items {
		if item.StartAt != nil && item.StartAt.Format(time.RFC3339) == start {
			return item
		}
	}
	t.Fatalf("no event starting %s", start)
	return generated.CalendarEvent{}
}

func counts(t *testing.T, source generated.CalendarSource) generated.SourceRefreshCounts {
	t.Helper()
	if source.Refresh.LastCounts == nil {
		t.Fatalf("no counts: %+v", source.Refresh)
	}
	return *source.Refresh.LastCounts
}

func outcome(source generated.CalendarSource) string {
	if source.Refresh.LastOutcome == nil {
		return ""
	}
	return string(*source.Refresh.LastOutcome)
}

var zawFilter = map[string]any{"include": []string{"Bioabfall", "Gelber Sack", "Papier", "Restmüll Tonnen und Container 14-täglich"}, "exclude": []string{}}

func TestICSSourceImportAndRefresh(t *testing.T) {
	c := newSourcesClient(t, nil, nil)
	fx := loadFixture(t)
	server := &feedServer{body: fx.feed(nil)}
	feed := httptest.NewServer(server)
	defer feed.Close()

	created := c.source(http.MethodPost, "/v1/sources", map[string]any{
		"name": "ZAW", "url": feed.URL + "/abholtermine.ics", "filter": zawFilter,
		"defaultReminders": []map[string]any{{"kind": "localTime", "daysBefore": 1, "localTime": "19:00"}},
	}, http.StatusCreated)
	if created.Kind != generated.CalendarSourceKindIcs || !created.Enabled || created.Refresh.NextRefreshAt == nil || !created.Refresh.NextRefreshAt.Equal(fixedNow) ||
		created.Refresh.LastOutcome != nil || created.Refresh.Refreshing || created.DefaultReminders == nil || len(*created.DefaultReminders) != 1 {
		t.Fatalf("created = %+v", created)
	}
	id := created.Id

	// Initial import: the four kept waste types, 21 pickups.
	imported := c.refresh(id)
	if outcome(imported) != "imported" || imported.Refresh.LastError != nil || imported.Refresh.ConsecutiveFailures != 0 {
		t.Fatalf("first refresh = %+v", imported.Refresh)
	}
	if got := counts(t, imported); got.FeedEvents != 29 || got.Imported != 21 || got.Inserted != 21 || got.Updated != 0 || got.Removed != 0 {
		t.Fatalf("first counts = %+v", got)
	}
	// Next refresh: the daily quiet hour, 04:30 Berlin (CEST) tomorrow.
	if next := imported.Refresh.NextRefreshAt; next == nil || next.Format(time.RFC3339) != "2026-10-11T02:30:00Z" || !imported.Refresh.LastSuccessAt.Equal(fixedNow) {
		t.Fatalf("schedule = %+v", imported.Refresh)
	}
	events := c.events(id, false)
	if len(events) != 21 {
		t.Fatalf("listed %d events", len(events))
	}
	paper := events[0]
	if paper.Title != "Papier Tonnen und Container - Musterstadt, Beispielweg" || paper.StartAt.Format(time.RFC3339) != "2026-10-15T04:00:00Z" ||
		paper.Source.Kind != generated.EventSourceKindIcs || *paper.Source.SourceId != id || !strings.HasSuffix(*paper.Source.ExternalUid, "@mymuell") ||
		*paper.TimeZone != "Europe/Berlin" || len(paper.Reminders) != 0 || !paper.UseDefaultReminders || paper.Occurrence != nil {
		t.Fatalf("first event = %+v", paper)
	}
	// Reminders follow the source's default: the evening before at 19:00.
	if len(paper.EffectiveReminders) != 1 || *paper.EffectiveReminders[0].LocalTime != "19:00" || len(paper.DefaultReminders) != 1 {
		t.Fatalf("effective reminders = %+v", paper.EffectiveReminders)
	}

	// The same feed again: nothing new, no duplicates.
	again := c.refresh(id)
	if got := counts(t, again); got.Inserted != 0 || got.Unchanged != 21 || got.Updated != 0 {
		t.Fatalf("identical refresh counts = %+v", got)
	}
	if listed := c.events(id, true); len(listed) != 21 {
		t.Fatalf("after an identical refresh: %d events", len(listed))
	}

	// Imported events are read-only, except for their reminders.
	paperURL := "/v1/events/" + paper.Id.String()
	for _, body := range []string{`{"title":"Mine"}`, `{"status":"cancelled"}`, `{"reminders":[],"location":"x"}`} {
		if status, raw := c.do(http.MethodPatch, paperURL, body); status != http.StatusConflict || !strings.Contains(string(raw), "managed_by_source") {
			t.Errorf("PATCH %s: %d %s", body, status, raw)
		}
	}
	if status, raw := c.do(http.MethodDelete, paperURL, nil); status != http.StatusConflict || !strings.Contains(string(raw), "managed_by_source") {
		t.Errorf("DELETE: %d %s", status, raw)
	}
	override := c.event(http.MethodPatch, paperURL, `{"reminders":[{"kind":"localTime","daysBefore":1,"localTime":"20:30"}]}`, http.StatusOK)
	// An own reminder is added to the source's: the evening before at 19:00
	// and at 20:30.
	if len(override.EffectiveReminders) != 2 || *override.EffectiveReminders[0].LocalTime != "19:00" || *override.EffectiveReminders[1].LocalTime != "20:30" {
		t.Fatalf("own reminder = %+v", override.EffectiveReminders)
	}
	// The source's default can be turned off for one pickup, and back on.
	if status, raw := c.do(http.MethodPatch, paperURL, `{"useDefaultReminders":false,"title":"Mine"}`); status != http.StatusConflict || !strings.Contains(string(raw), "managed_by_source") {
		t.Errorf("defaults off with a title: %d %s", status, raw)
	}
	quiet := c.event(http.MethodPatch, paperURL, `{"useDefaultReminders":false}`, http.StatusOK)
	if len(quiet.EffectiveReminders) != 1 || *quiet.EffectiveReminders[0].LocalTime != "20:30" || len(quiet.DefaultReminders) != 1 {
		t.Fatalf("defaults off = %+v", quiet)
	}
	c.event(http.MethodPatch, paperURL, `{"useDefaultReminders":true}`, http.StatusOK)

	// The feed moves the paper pickup to the next day: same event, updated
	// in place, the owner's reminder kept.
	server.set(func(f *feedServer) {
		f.body = fx.feed(nil, "DTSTART;TZID=Europe/Berlin:20261015T060000", "DTSTART;TZID=Europe/Berlin:20261016T060000",
			"DTEND;TZID=Europe/Berlin:20261015T070000", "DTEND;TZID=Europe/Berlin:20261016T070000")
	})
	changed := c.refresh(id)
	if got := counts(t, changed); got.Updated != 1 || got.Inserted != 0 || got.Unchanged != 20 {
		t.Fatalf("changed counts = %+v", got)
	}
	moved := c.event(http.MethodGet, paperURL, nil, http.StatusOK)
	if moved.StartAt.Format(time.RFC3339) != "2026-10-16T04:00:00Z" || len(moved.Reminders) != 1 || *moved.Reminders[0].LocalTime != "20:30" || moved.Status != generated.Active {
		t.Fatalf("moved = %+v", moved)
	}

	// A future pickup disappears from the feed: cancelled and marked removed
	// by the source, kept as history, out of active listings.
	server.set(func(f *feedServer) { f.body = fx.feed([]string{"20261022"}) })
	gone := c.refresh(id)
	if got := counts(t, gone); got.Removed != 1 || got.Updated != 1 {
		t.Fatalf("removal counts = %+v", got)
	}
	active := c.events(id, false)
	if len(active) != 20 {
		t.Fatalf("active after removal: %d", len(active))
	}
	removed := findStart(t, c.events(id, true), "2026-10-22T04:00:00Z")
	if removed.Status != generated.Cancelled || removed.RemovedFromSourceAt == nil || !removed.RemovedFromSourceAt.Equal(fixedNow) || removed.CancelledAt == nil {
		t.Fatalf("removed = %+v", removed)
	}

	// Time passes; the feed drops pickups that are over, as real feeds do.
	// Past events stay as they were.
	*c.clock = time.Date(2026, time.October, 20, 10, 0, 0, 0, time.UTC)
	server.set(func(f *feedServer) {
		f.body = fx.feed([]string{"20261014", "20261015", "20261017", "20261019", "20261022"})
	})
	later := c.refresh(id)
	if got := counts(t, later); got.Removed != 0 || got.Imported != 18 || got.Restored != 0 {
		t.Fatalf("after time passed = %+v", got)
	}
	if past := findStart(t, c.events(id, false), "2026-10-19T04:00:00Z"); past.Status != generated.Active || past.RemovedFromSourceAt != nil {
		t.Fatalf("a past pickup was touched: %+v", past)
	}

	// The removed pickup returns (same UID): restored.
	server.set(func(f *feedServer) { f.body = fx.feed([]string{"20261014", "20261015", "20261017", "20261019"}) })
	back := c.refresh(id)
	if got := counts(t, back); got.Restored != 1 {
		t.Fatalf("restore counts = %+v", got)
	}
	restored := c.event(http.MethodGet, "/v1/events/"+removed.Id.String(), nil, http.StatusOK)
	if restored.Status != generated.Active || restored.RemovedFromSourceAt != nil || restored.CancelledAt != nil {
		t.Fatalf("restored = %+v", restored)
	}

	// Activity records each refresh as the source, without the feed URL.
	recorded := c.activity.all()
	if len(recorded) != 6 {
		t.Fatalf("activity events = %d", len(recorded))
	}
	for _, event := range recorded {
		if event.Type != "calendar.source.refreshed" || event.Actor.Kind != "system" || event.Actor.Name != "ics-zaw" || event.Subject.Type != "calendar_source" ||
			event.Subject.ID != id.String() || event.Source != "calendar" {
			t.Errorf("activity event = %+v", event)
		}
		raw, _ := json.Marshal(event)
		if strings.Contains(string(raw), feed.URL) || strings.Contains(string(raw), "abholtermine.ics") {
			t.Errorf("activity leaks the feed URL: %s", raw)
		}
	}
	if !strings.HasPrefix(recorded[0].Summary, "Refreshed ZAW: 21 new") || recorded[0].Data["trigger"] != "manual" {
		t.Errorf("first activity = %+v", recorded[0])
	}
}

func TestICSSourceFailuresKeepTheLastGoodData(t *testing.T) {
	c := newSourcesClient(t, nil, nil)
	fx := loadFixture(t)
	server := &feedServer{body: fx.feed(nil)}
	feed := httptest.NewServer(server)
	defer feed.Close()
	id := c.source(http.MethodPost, "/v1/sources", map[string]any{"name": "ZAW", "url": feed.URL}, http.StatusCreated).Id
	good := c.refresh(id)
	if counts(t, good).Inserted != 29 {
		t.Fatalf("import = %+v", good.Refresh)
	}
	failures := []struct {
		set       func(*feedServer)
		message   string
		nextRetry time.Duration
	}{
		{func(f *feedServer) { f.status = http.StatusInternalServerError }, "the feed answered HTTP 500", 15 * time.Minute},
		{func(f *feedServer) { f.status, f.body = 0, "<html><body>Wartungsarbeiten</body></html>" }, "the response is not an iCalendar feed", 30 * time.Minute},
		{func(f *feedServer) { f.body = fx.head + fx.tail }, "the feed contains no events", time.Hour},
		{func(f *feedServer) { f.body = fx.feed(nil)[:5000] }, "the feed is incomplete", 2 * time.Hour},
	}
	for index, failure := range failures {
		server.set(failure.set)
		failed := c.refresh(id)
		if outcome(failed) != "failed" || failed.Refresh.LastError == nil || !strings.HasPrefix(*failed.Refresh.LastError, failure.message) ||
			failed.Refresh.ConsecutiveFailures != index+1 || !failed.Refresh.NextRefreshAt.Equal(fixedNow.Add(failure.nextRetry)) ||
			!failed.Refresh.LastSuccessAt.Equal(fixedNow) || counts(t, failed).Inserted != 29 {
			t.Fatalf("failure %d = %+v (error %v)", index, failed.Refresh, failed.Refresh.LastError)
		}
		if events := c.events(id, false); len(events) != 29 {
			t.Fatalf("failure %d dropped events: %d left", index, len(events))
		}
	}
	recorded := c.activity.all()
	if last := recorded[len(recorded)-1]; last.Type != "calendar.source.refresh_failed" || last.Outcome != activity.Failed || !strings.Contains(last.Summary, "Refreshing ZAW failed") {
		t.Fatalf("failure activity = %+v", last)
	}

	// The next good refresh clears the failure state.
	server.set(func(f *feedServer) { f.body = fx.feed(nil) })
	recovered := c.refresh(id)
	if outcome(recovered) != "imported" || recovered.Refresh.LastError != nil || recovered.Refresh.ConsecutiveFailures != 0 || counts(t, recovered).Unchanged != 29 {
		t.Fatalf("recovered = %+v", recovered.Refresh)
	}
}

// The source's refresh state lives in the database: a restarted service
// (new handler and refresher) continues with the stored validators,
// schedule, and lease.
func TestICSSourceConditionalAndRestartSafeRefresh(t *testing.T) {
	c := newSourcesClient(t, nil, nil)
	fx := loadFixture(t)
	server := &feedServer{body: fx.feed(nil), etag: `"v1"`}
	feed := httptest.NewServer(server)
	defer feed.Close()
	id := c.source(http.MethodPost, "/v1/sources", map[string]any{"name": "ZAW", "url": feed.URL, "filter": zawFilter}, http.StatusCreated).Id
	first := c.refresh(id)
	if first.Refresh.Etag == nil || *first.Refresh.Etag != `"v1"` || counts(t, first).Inserted != 21 {
		t.Fatalf("first = %+v", first.Refresh)
	}

	// "Restart": a new refresher and handler over the same database.
	restarted := newSourcesClient(t, c.pool, c.clock)
	state := restarted.source(http.MethodGet, "/v1/sources/"+id.String(), nil, http.StatusOK)
	if *state.Refresh.Etag != `"v1"` || state.Refresh.NextRefreshAt.Format(time.RFC3339) != "2026-10-11T02:30:00Z" || !state.Refresh.LastSuccessAt.Equal(fixedNow) {
		t.Fatalf("state after restart = %+v", state.Refresh)
	}
	unchanged := restarted.refresh(id)
	if outcome(unchanged) != "not_modified" || server.notModified != 1 || counts(t, unchanged).Inserted != 21 || *unchanged.Refresh.Etag != `"v1"` {
		t.Fatalf("conditional refresh = %+v (304s %d)", unchanged.Refresh, server.notModified)
	}

	// The scheduler refreshes what is due by the stored schedule.
	scheduler := sources.NewScheduler(restarted.refresher, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	scheduler.RunDue(context.Background())
	if server.requests != 2 {
		t.Fatalf("a source not yet due was refreshed (%d requests)", server.requests)
	}
	*c.clock = time.Date(2026, time.October, 11, 2, 31, 0, 0, time.UTC)
	server.set(func(f *feedServer) { f.etag, f.body = `"v2"`, fx.feed([]string{"20261231"}) })
	scheduler.RunDue(context.Background())
	scheduled := restarted.source(http.MethodGet, "/v1/sources/"+id.String(), nil, http.StatusOK)
	if outcome(scheduled) != "imported" || *scheduled.Refresh.Etag != `"v2"` || counts(t, scheduled).Removed != 1 || scheduled.Refresh.NextRefreshAt.Format(time.RFC3339) != "2026-10-12T02:30:00Z" {
		t.Fatalf("scheduled refresh = %+v", scheduled.Refresh)
	}
	if recorded := restarted.activity.all(); recorded[len(recorded)-1].Data["trigger"] != "scheduled" {
		t.Errorf("scheduled refresh activity = %+v", recorded[len(recorded)-1])
	}

	// A refresh that crashed holds the lease until it expires.
	now := *c.clock
	if _, err := c.queries.ClaimSourceRefresh(context.Background(), store.ClaimSourceRefreshParams{ID: pgID(id), Now: timestamp(&now), LeaseUntil: pgtype.Timestamptz{Time: now.Add(3 * time.Minute), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if leased := restarted.source(http.MethodGet, "/v1/sources/"+id.String(), nil, http.StatusOK); !leased.Refresh.Refreshing {
		t.Fatalf("lease not shown: %+v", leased.Refresh)
	}
	if status, raw := restarted.do(http.MethodPost, "/v1/sources/"+id.String()+"/refresh", nil); status != http.StatusConflict || !strings.Contains(string(raw), "refresh_in_progress") {
		t.Fatalf("refresh during a lease: %d %s", status, raw)
	}
	*c.clock = now.Add(4 * time.Minute)
	if taken := restarted.refresh(id); outcome(taken) != "not_modified" || taken.Refresh.Refreshing {
		t.Fatalf("refresh after the lease expired = %+v", taken.Refresh)
	}
}

func TestICSSourceConfiguration(t *testing.T) {
	c := newSourcesClient(t, nil, nil)
	fx := loadFixture(t)
	server := &feedServer{body: fx.feed(nil), etag: `"v1"`}
	feed := httptest.NewServer(server)
	defer feed.Close()
	created := c.source(http.MethodPost, "/v1/sources", map[string]any{"name": " Waste ", "url": feed.URL}, http.StatusCreated)
	if created.Name != "Waste" || created.DefaultReminders != nil || len(created.Filter.Include) != 0 {
		t.Fatalf("created = %+v", created)
	}
	id := created.Id
	path := "/v1/sources/" + id.String()
	c.refresh(id)
	if len(c.events(id, false)) != 29 {
		t.Fatal("import")
	}

	// A new filter drops the validators, is due right away, and reconciles:
	// future events it now excludes are removed, as if they left the feed.
	*c.clock = fixedNow.Add(time.Hour)
	filtered := c.source(http.MethodPatch, path, map[string]any{"filter": map[string]any{"include": []string{}, "exclude": []string{"Container wöchentlich", "Schadstoffmobil"}}}, http.StatusOK)
	if filtered.Refresh.Etag != nil || !filtered.Refresh.NextRefreshAt.Equal(*c.clock) || len(filtered.Filter.Exclude) != 2 {
		t.Fatalf("filtered = %+v", filtered)
	}
	if got := counts(t, c.refresh(id)); got.Removed != 8 || got.Imported != 21 {
		t.Fatalf("filter counts = %+v", got)
	}
	if len(c.events(id, false)) != 21 || len(c.events(id, true)) != 29 {
		t.Fatal("filtered events")
	}
	// Default reminders apply to events without their own.
	c.source(http.MethodPatch, path, `{"defaultReminders":[{"kind":"relative","minutesBefore":60}],"name":"ZAW"}`, http.StatusOK)
	if item := c.events(id, false)[0]; len(item.EffectiveReminders) != 1 || *item.EffectiveReminders[0].MinutesBefore != 60 {
		t.Fatalf("effective reminders = %+v", item.EffectiveReminders)
	}
	cleared := c.source(http.MethodPatch, path, `{"defaultReminders":null}`, http.StatusOK)
	if cleared.DefaultReminders != nil || cleared.Name != "ZAW" {
		t.Fatalf("cleared = %+v", cleared)
	}

	// Disabled sources are not refreshed; enabling schedules a refresh now.
	disabled := c.source(http.MethodPatch, path, `{"enabled":false}`, http.StatusOK)
	if disabled.Enabled {
		t.Fatal("still enabled")
	}
	if status, raw := c.do(http.MethodPost, path+"/refresh", nil); status != http.StatusConflict || !strings.Contains(string(raw), "source_disabled") {
		t.Fatalf("refresh disabled: %d %s", status, raw)
	}
	*c.clock = fixedNow.Add(2 * time.Hour)
	if enabled := c.source(http.MethodPatch, path, `{"enabled":true}`, http.StatusOK); !enabled.Refresh.NextRefreshAt.Equal(*c.clock) {
		t.Fatalf("enabled = %+v", enabled.Refresh)
	}

	for _, tc := range []struct{ body, want string }{
		{`{"name":null}`, "name cannot be null"},
		{`{"url":"https://10.0.0.1/feed.ics","name":"x"}`, ""}, // the development fetcher allows it
		{`{"filter":{"include":[" "],"exclude":[]}}`, "terms must be"},
		{`{"defaultReminders":[{"kind":"localTime","daysBefore":1}]}`, "localTime reminder"},
		{`{"source":"x"}`, "names no source field"},
	} {
		status, raw := c.do(http.MethodPatch, path, tc.body)
		if tc.want == "" {
			if status != http.StatusOK {
				t.Errorf("PATCH %s: %d %s", tc.body, status, raw)
			}
			continue
		}
		if status != http.StatusBadRequest || !strings.Contains(string(raw), tc.want) {
			t.Errorf("PATCH %s: %d %s, want 400 %q", tc.body, status, raw, tc.want)
		}
	}

	var list generated.SourceList
	status, raw := c.do(http.MethodGet, "/v1/sources", nil)
	if err := json.Unmarshal(raw, &list); status != http.StatusOK || err != nil || len(list.Items) != 1 || list.Items[0].Id != id {
		t.Fatalf("list: %d %s", status, raw)
	}
	// Deleting the source deletes its events.
	someEvent := c.events(id, true)[0].Id
	if status, _ := c.do(http.MethodDelete, path, nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	if status, _ := c.do(http.MethodGet, "/v1/events/"+someEvent.String(), nil); status != http.StatusNotFound {
		t.Fatalf("event of a deleted source: %d", status)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPost} {
		target := path
		if method == http.MethodPost {
			target += "/refresh"
		}
		if status, _ := c.do(method, target, nil); status != http.StatusNotFound {
			t.Errorf("%s %s after delete: %d", method, target, status)
		}
	}
}
