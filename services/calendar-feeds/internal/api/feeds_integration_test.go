package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/calendar"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/feeds"
	store "github.com/GAMch1k/life-os/services/calendar-feeds/internal/repository/generated"
)

func TestFeedImportAndRefresh(t *testing.T) {
	h := newHarness(t, nil)
	fx := loadFixture(t)
	server := &feedServer{body: fx.feed(nil)}
	feed := httptest.NewServer(server)
	defer feed.Close()

	created := h.feed(http.MethodPost, "/v1/feeds", map[string]any{
		"name": "ZAW", "url": feed.URL + "/abholtermine.ics", "filter": zawFilter,
		"defaultReminders": []map[string]any{{"kind": "localTime", "daysBefore": 1, "localTime": "19:00"}},
	}, http.StatusCreated)
	if !created.Enabled || created.Name != "ZAW" || created.SourceId == nil || created.Refresh.NextRefreshAt == nil || !created.Refresh.NextRefreshAt.Equal(fixedNow) ||
		created.Refresh.LastOutcome != nil || created.Refresh.Refreshing || created.Preset != nil {
		t.Fatalf("created = %+v", created)
	}
	id := created.Id
	// Its source is registered in Calendar under the feed's id, read-only,
	// with the default reminders given on create.
	registered := h.source(id)
	if registered.ID != created.SourceId.String() || registered.Provider != feeds.ProviderICS || !registered.ReadOnly || registered.Name != "ZAW" ||
		!strings.Contains(string(registered.DefaultReminders), `"19:00"`) {
		t.Fatalf("registered = %+v", registered.Source)
	}

	// Initial import: the four kept waste types, 21 pickups.
	imported := h.refresh(id)
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
	source := h.source(id)
	if active := source.Active(); len(active) != 21 || !strings.HasSuffix(active[0], "@mymuell") {
		t.Fatalf("Calendar has %d events: %v", len(active), active)
	}
	// Events keep the ICS identity, times, and zone, and carry no reminders
	// or priority of their own (the owner's stay).
	paperUID := uidAt(t, source, "2026-10-15T04:00:00Z")
	paper := source.Events[paperUID]
	if paper.Item.Title != "Papier Tonnen und Container - Musterstadt, Beispielweg" || paper.Item.EndAt.Format(time.RFC3339) != "2026-10-15T05:00:00Z" ||
		*paper.Item.TimeZone != "Europe/Berlin" || *paper.Item.Location != "Musterstadt, Beispielweg" || paper.Item.AllDay || paper.Item.Status != "" {
		t.Fatalf("first event = %+v", paper.Item)
	}

	// The same feed again: nothing new, no duplicates.
	again := h.refresh(id)
	if got := counts(t, again); got.Inserted != 0 || got.Unchanged != 21 || got.Updated != 0 {
		t.Fatalf("identical refresh counts = %+v", got)
	}
	if events := h.source(id).Events; len(events) != 21 {
		t.Fatalf("after an identical refresh: %d events", len(events))
	}

	// The feed moves the paper pickup to the next day: same event, updated.
	server.set(func(f *feedServer) {
		f.body = fx.feed(nil, "DTSTART;TZID=Europe/Berlin:20261015T060000", "DTSTART;TZID=Europe/Berlin:20261016T060000",
			"DTEND;TZID=Europe/Berlin:20261015T070000", "DTEND;TZID=Europe/Berlin:20261016T070000")
	})
	if got := counts(t, h.refresh(id)); got.Updated != 1 || got.Inserted != 0 || got.Unchanged != 20 {
		t.Fatalf("changed counts = %+v", got)
	}
	if moved := h.source(id).Events[paperUID]; moved.Item.StartAt.Format(time.RFC3339) != "2026-10-16T04:00:00Z" {
		t.Fatalf("moved = %+v", moved.Item)
	}

	// A future pickup disappears from the feed: removed in Calendar.
	server.set(func(f *feedServer) { f.body = fx.feed([]string{"20261022"}) })
	if got := counts(t, h.refresh(id)); got.Removed != 1 || got.Updated != 1 {
		t.Fatalf("removal counts = %+v", got)
	}
	if removed := eventAt(t, h.source(id), "2026-10-22T04:00:00Z"); !removed.Removed {
		t.Fatalf("removed = %+v", removed)
	}

	// Time passes; the feed drops pickups that are over, as real feeds do.
	// Past events stay as they were (window upcomingOnly).
	*h.clock = time.Date(2026, time.October, 20, 10, 0, 0, 0, time.UTC)
	server.set(func(f *feedServer) {
		f.body = fx.feed([]string{"20261014", "20261015", "20261017", "20261019", "20261022"})
	})
	if got := counts(t, h.refresh(id)); got.Removed != 0 || got.Imported != 18 || got.Restored != 0 {
		t.Fatalf("after time passed = %+v", got)
	}
	if past := eventAt(t, h.source(id), "2026-10-19T04:00:00Z"); past.Removed {
		t.Fatalf("a past pickup was removed: %+v", past)
	}

	// The removed pickup returns (same UID): restored.
	server.set(func(f *feedServer) { f.body = fx.feed([]string{"20261014", "20261015", "20261017", "20261019"}) })
	if got := counts(t, h.refresh(id)); got.Restored != 1 {
		t.Fatalf("restore counts = %+v", got)
	}

	// Activity records each refresh as the feed, without the feed URL.
	recorded := h.activity.all()
	if len(recorded) != 6 {
		t.Fatalf("activity events = %d", len(recorded))
	}
	for _, event := range recorded {
		if event.Type != "calendar.source.refreshed" || event.Actor.Kind != "system" || event.Actor.Name != "ics-zaw" || event.Subject.Type != "calendar_source" ||
			event.Subject.ID != created.SourceId.String() || event.Source != "calendar-feeds" || event.Data["feedId"] != id.String() {
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
	// Every call to Calendar is the integration's own.
	for _, actor := range h.calendar.Actors {
		if actor != calendar.Actor {
			t.Fatalf("a call to Calendar as %q", actor)
		}
	}
}

func TestFeedFailuresKeepTheLastGoodData(t *testing.T) {
	h := newHarness(t, nil)
	fx := loadFixture(t)
	server := &feedServer{body: fx.feed(nil)}
	feed := httptest.NewServer(server)
	defer feed.Close()
	id := h.feed(http.MethodPost, "/v1/feeds", map[string]any{"name": "ZAW", "url": feed.URL}, http.StatusCreated).Id
	good := h.refresh(id)
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
		failed := h.refresh(id)
		if outcome(failed) != "failed" || failed.Refresh.LastError == nil || !strings.HasPrefix(*failed.Refresh.LastError, failure.message) ||
			failed.Refresh.ConsecutiveFailures != index+1 || !failed.Refresh.NextRefreshAt.Equal(fixedNow.Add(failure.nextRetry)) ||
			!failed.Refresh.LastSuccessAt.Equal(fixedNow) || counts(t, failed).Inserted != 29 {
			t.Fatalf("failure %d = %+v (error %v)", index, failed.Refresh, failed.Refresh.LastError)
		}
		if active := h.source(id).Active(); len(active) != 29 {
			t.Fatalf("failure %d dropped events: %d left", index, len(active))
		}
	}
	recorded := h.activity.all()
	if last := recorded[len(recorded)-1]; last.Type != "calendar.source.refresh_failed" || last.Outcome != activity.Failed || !strings.Contains(last.Summary, "Refreshing ZAW failed") {
		t.Fatalf("failure activity = %+v", last)
	}

	// Calendar unreachable: a failure of the refresh too, retried, events
	// kept, and the feed's validators not advanced.
	server.set(func(f *feedServer) { f.body, f.etag = fx.feed(nil), `"v9"` })
	h.calendar.Down = true
	down := h.refresh(id)
	if outcome(down) != "failed" || !strings.HasPrefix(*down.Refresh.LastError, "Calendar did not take the events") || down.Refresh.Etag != nil ||
		down.Refresh.ConsecutiveFailures != 5 {
		t.Fatalf("Calendar down = %+v (%v)", down.Refresh, *down.Refresh.LastError)
	}
	h.calendar.Down = false

	// The next good refresh clears the failure state.
	recovered := h.refresh(id)
	if outcome(recovered) != "imported" || recovered.Refresh.LastError != nil || recovered.Refresh.ConsecutiveFailures != 0 || counts(t, recovered).Unchanged != 29 ||
		recovered.Refresh.Etag == nil || *recovered.Refresh.Etag != `"v9"` {
		t.Fatalf("recovered = %+v", recovered.Refresh)
	}
}

// The feed's refresh state lives in its database: a restarted service (new
// handler and refresher) continues with the stored validators, schedule,
// and lease.
func TestFeedConditionalAndRestartSafeRefresh(t *testing.T) {
	h := newHarness(t, nil)
	fx := loadFixture(t)
	server := &feedServer{body: fx.feed(nil), etag: `"v1"`}
	feed := httptest.NewServer(server)
	defer feed.Close()
	id := h.feed(http.MethodPost, "/v1/feeds", map[string]any{"name": "ZAW", "url": feed.URL, "filter": zawFilter}, http.StatusCreated).Id
	first := h.refresh(id)
	if first.Refresh.Etag == nil || *first.Refresh.Etag != `"v1"` || counts(t, first).Inserted != 21 {
		t.Fatalf("first = %+v", first.Refresh)
	}

	// "Restart": a new refresher and handler over the same database.
	restarted := newHarness(t, h)
	state := restarted.get(id)
	if *state.Refresh.Etag != `"v1"` || state.Refresh.NextRefreshAt.Format(time.RFC3339) != "2026-10-11T02:30:00Z" || !state.Refresh.LastSuccessAt.Equal(fixedNow) {
		t.Fatalf("state after restart = %+v", state.Refresh)
	}
	syncs := h.source(id).Syncs
	unchanged := restarted.refresh(id)
	if outcome(unchanged) != "not_modified" || server.notModified != 1 || counts(t, unchanged).Inserted != 21 || *unchanged.Refresh.Etag != `"v1"` {
		t.Fatalf("conditional refresh = %+v (304s %d)", unchanged.Refresh, server.notModified)
	}
	if h.source(id).Syncs != syncs {
		t.Fatal("an unchanged feed was synced again")
	}

	// The scheduler refreshes what is due by the stored schedule.
	restarted.runDue()
	if server.requests != 2 {
		t.Fatalf("a feed not yet due was refreshed (%d requests)", server.requests)
	}
	*h.clock = time.Date(2026, time.October, 11, 2, 31, 0, 0, time.UTC)
	server.set(func(f *feedServer) { f.etag, f.body = `"v2"`, fx.feed([]string{"20261231"}) })
	restarted.runDue()
	scheduled := restarted.get(id)
	if outcome(scheduled) != "imported" || *scheduled.Refresh.Etag != `"v2"` || counts(t, scheduled).Removed != 1 || scheduled.Refresh.NextRefreshAt.Format(time.RFC3339) != "2026-10-12T02:30:00Z" {
		t.Fatalf("scheduled refresh = %+v", scheduled.Refresh)
	}
	if recorded := restarted.activity.all(); recorded[len(recorded)-1].Data["trigger"] != "scheduled" {
		t.Errorf("scheduled refresh activity = %+v", recorded[len(recorded)-1])
	}

	// A refresh that crashed holds the lease until it expires.
	now := *h.clock
	if _, err := h.queries.ClaimFeedRefresh(context.Background(), store.ClaimFeedRefreshParams{
		ID: pgtype.UUID{Bytes: id, Valid: true}, Now: pgtype.Timestamptz{Time: now, Valid: true}, LeaseUntil: pgtype.Timestamptz{Time: now.Add(3 * time.Minute), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if leased := restarted.get(id); !leased.Refresh.Refreshing {
		t.Fatalf("lease not shown: %+v", leased.Refresh)
	}
	if status, raw := restarted.do(http.MethodPost, "/v1/feeds/"+id.String()+"/refresh", nil); status != http.StatusConflict || !strings.Contains(string(raw), "refresh_in_progress") {
		t.Fatalf("refresh during a lease: %d %s", status, raw)
	}
	*h.clock = now.Add(4 * time.Minute)
	if taken := restarted.refresh(id); outcome(taken) != "not_modified" || taken.Refresh.Refreshing {
		t.Fatalf("refresh after the lease expired = %+v", taken.Refresh)
	}
}

func TestFeedConfiguration(t *testing.T) {
	h := newHarness(t, nil)
	fx := loadFixture(t)
	server := &feedServer{body: fx.feed(nil), etag: `"v1"`}
	feed := httptest.NewServer(server)
	defer feed.Close()
	created := h.feed(http.MethodPost, "/v1/feeds", map[string]any{"name": " Waste ", "url": feed.URL, "defaultPriority": "high"}, http.StatusCreated)
	if created.Name != "Waste" || len(created.Filter.Include) != 0 || h.source(created.Id).DefaultPriority != "high" || string(h.source(created.Id).DefaultReminders) != "null" {
		t.Fatalf("created = %+v / %+v", created, h.source(created.Id).Source)
	}
	id := created.Id
	path := "/v1/feeds/" + id.String()
	h.refresh(id)
	if len(h.source(id).Active()) != 29 {
		t.Fatal("import")
	}

	// A new filter drops the validators, is due right away, and syncs:
	// future events it now excludes are removed, as if they left the feed.
	*h.clock = fixedNow.Add(time.Hour)
	filtered := h.feed(http.MethodPatch, path, map[string]any{"filter": map[string]any{"include": []string{}, "exclude": []string{"Container wöchentlich", "Schadstoffmobil"}}}, http.StatusOK)
	if filtered.Refresh.Etag != nil || !filtered.Refresh.NextRefreshAt.Equal(*h.clock) || len(filtered.Filter.Exclude) != 2 {
		t.Fatalf("filtered = %+v", filtered)
	}
	if got := counts(t, h.refresh(id)); got.Removed != 8 || got.Imported != 21 {
		t.Fatalf("filter counts = %+v", got)
	}
	if source := h.source(id); len(source.Active()) != 21 || len(source.Events) != 29 {
		t.Fatal("filtered events")
	}

	// The owner renames the source in Calendar: refreshes carry the new name.
	h.calendar.Update(id.String(), func(source *calendar.Source) { source.Name = "Müll" })
	if renamed := h.refresh(id); renamed.Name != "Müll" {
		t.Fatalf("renamed = %+v", renamed)
	}
	if last := h.activity.all(); last[len(last)-1].Actor.Name != "ics-muell" {
		t.Fatalf("activity after the rename = %+v", last[len(last)-1])
	}

	// The owner disables the source in Calendar: not refreshed; enabling it
	// there makes it due within the scheduler's next pass.
	h.calendar.Update(id.String(), func(source *calendar.Source) { source.Enabled = false })
	h.runDue()
	if disabled := h.get(id); disabled.Enabled {
		t.Fatal("still enabled")
	}
	if status, raw := h.do(http.MethodPost, path+"/refresh", nil); status != http.StatusConflict || !strings.Contains(string(raw), "source_disabled") {
		t.Fatalf("refresh disabled: %d %s", status, raw)
	}
	requests := server.requests
	*h.clock = fixedNow.Add(2 * time.Hour)
	h.calendar.Update(id.String(), func(source *calendar.Source) { source.Enabled = true })
	h.runDue()
	if enabled := h.get(id); !enabled.Enabled || !enabled.Refresh.LastAttemptAt.Equal(*h.clock) || server.requests != requests+1 {
		t.Fatalf("enabled = %+v (%d requests)", enabled, server.requests-requests)
	}
	// Disabled between two passes: Calendar refuses the sync, which is no
	// failure of the feed; it waits until the owner enables it again.
	before := h.get(id)
	h.calendar.Update(id.String(), func(source *calendar.Source) { source.Enabled = false })
	server.set(func(f *feedServer) { f.etag = `"v3"` })
	if _, err := h.refresher.Refresh(context.Background(), id, feeds.Trigger{}); !errors.Is(err, feeds.ErrDisabled) {
		t.Fatalf("refresh disabled in Calendar: %v", err)
	}
	if after := h.get(id); after.Enabled || outcome(after) != outcome(before) || after.Refresh.ConsecutiveFailures != 0 || after.Refresh.Refreshing {
		t.Fatalf("after a refused sync = %+v", after.Refresh)
	}
	h.calendar.Update(id.String(), func(source *calendar.Source) { source.Enabled = true })

	for _, tc := range []struct{ body, want string }{
		{`{"url":null}`, "url cannot be null"},
		{`{"url":"https://10.0.0.1/feed.ics"}`, ""}, // the development fetcher allows it
		{`{"filter":{"include":[" "],"exclude":[]}}`, "terms must be"},
		{`{"name":"x"}`, "names no feed field"},
		{`{"url":"ftp://feeds.example.test/feed.ics"}`, "https"},
	} {
		status, raw := h.do(http.MethodPatch, path, tc.body)
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

	var list generated.FeedList
	status, raw := h.do(http.MethodGet, "/v1/feeds", nil)
	if err := json.Unmarshal(raw, &list); status != http.StatusOK || err != nil || len(list.Items) != 1 || list.Items[0].Id != id {
		t.Fatalf("list: %d %s", status, raw)
	}
	// Deleting the feed deletes its Calendar source (and so its events).
	if status, _ := h.do(http.MethodDelete, path, nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	if _, ok := h.calendar.Source(id.String()); ok {
		t.Fatal("the Calendar source outlived its feed")
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPost} {
		target := path
		if method == http.MethodPost {
			target += "/refresh"
		}
		if status, _ := h.do(method, target, nil); status != http.StatusNotFound {
			t.Errorf("%s %s after delete: %d", method, target, status)
		}
	}
}

// Nothing is added when Calendar cannot register the source, Calendar's
// refusal of the owner's settings is the caller's 400, and a source deleted
// in Calendar takes its feed with it.
func TestFeedsFollowCalendar(t *testing.T) {
	h := newHarness(t, nil)
	h.calendar.Down = true
	if status, raw := h.do(http.MethodPost, "/v1/feeds", map[string]any{"name": "Holidays", "url": "https://feeds.example.test/holidays.ics"}); status != http.StatusBadGateway ||
		!strings.Contains(string(raw), "calendar_unavailable") {
		t.Fatalf("create while Calendar is down: %d %s", status, raw)
	}
	h.calendar.Down = false
	if status, raw := h.do(http.MethodPost, "/v1/feeds", `{"name":"Holidays","url":"https://feeds.example.test/holidays.ics","defaultReminders":{"kind":"x"}}`); status != http.StatusBadRequest {
		t.Fatalf("create with unusable reminders: %d %s", status, raw)
	}
	for _, body := range []string{`{"name":"","url":"https://feeds.example.test/h.ics"}`, `{"name":"H","url":"not a url"}`, `{"name":"H","url":"https://feeds.example.test/h.ics","defaultPriority":"urgent"}`} {
		if status, raw := h.do(http.MethodPost, "/v1/feeds", body); status != http.StatusBadRequest {
			t.Errorf("create %s: %d %s", body, status, raw)
		}
	}
	if rows, _ := h.queries.ListFeeds(context.Background()); len(rows) != 0 || h.calendar.Count() != 0 {
		t.Fatalf("%d feeds and %d sources after refused creates", len(rows), h.calendar.Count())
	}

	created := h.feed(http.MethodPost, "/v1/feeds", map[string]any{"name": "Holidays", "url": "webcal://feeds.example.test/holidays.ics"}, http.StatusCreated)
	if created.Url != "https://feeds.example.test/holidays.ics" {
		t.Fatalf("created = %+v", created)
	}
	h.calendar.Remove(created.Id.String())
	if status, _ := h.do(http.MethodPost, "/v1/feeds/"+created.Id.String()+"/refresh", nil); status != http.StatusNotFound {
		t.Fatalf("refresh of a source deleted in Calendar: %d", status)
	}
	if status, _ := h.do(http.MethodGet, "/v1/feeds/"+created.Id.String(), nil); status != http.StatusNotFound {
		t.Fatalf("the feed outlived its Calendar source: %d", status)
	}
}

// A new preset feed is never registered while Calendar has sources of this
// integration that no feed here belongs to (such as after the feeds
// database was replaced), so the ZAW events are never imported twice; once
// they are gone from Calendar, it registers (its locations private) and
// syncs.
func TestNewFeedsWaitWhileCalendarHasUnknownSources(t *testing.T) {
	h := newHarness(t, nil)
	fx := loadFixture(t)
	server := &feedServer{body: fx.feed(nil)}
	feed := httptest.NewServer(server)
	defer feed.Close()
	feedURL := feed.URL + "/api.php?m=zaw&r=ical&area_id=1"
	unknown := "8f0b6a2e-3c4d-4e5f-8a9b-0c1d2e3f4a5b"
	h.calendar.Add(unknown, "ZAW", feeds.ProviderZAW)

	ensured, err := h.refresher.EnsurePreset(context.Background(), feeds.ZAWPreset(feedURL))
	if err != nil || ensured.Action != feeds.EnsureCreated || !ensured.Feed.RegisterPending {
		t.Fatalf("ensure = %+v, %v", ensured, err)
	}
	h.runDue()
	if h.calendar.Count() != 1 {
		t.Fatalf("a second ZAW source was registered: %d sources", h.calendar.Count())
	}
	id := types.UUID(ensured.Feed.ID.Bytes)
	waiting := h.get(id)
	if waiting.SourceId != nil || outcome(waiting) != "failed" || !strings.Contains(*waiting.Refresh.LastError, "this service does not know") || server.requests != 0 {
		t.Fatalf("waiting = %+v (%v)", waiting.Refresh, waiting.Refresh.LastError)
	}

	// The unknown source is deleted in Calendar: the feed registers and syncs.
	h.calendar.Remove(unknown)
	*h.clock = fixedNow.Add(20 * time.Minute)
	h.runDue()
	synced := h.get(id)
	source := h.source(id)
	if outcome(synced) != "imported" || counts(t, synced).Inserted != 21 || h.calendar.Count() != 1 || source.Provider != feeds.ProviderZAW ||
		!source.PrivateLocation || !strings.Contains(string(source.DefaultReminders), `"19:00"`) {
		t.Fatalf("after the unknown source went = %+v / %+v", synced.Refresh, source.Source)
	}
	if rows, _ := h.queries.ListRegisterPending(context.Background()); len(rows) != 0 {
		t.Fatalf("%d feeds still wait for registration", len(rows))
	}
}
