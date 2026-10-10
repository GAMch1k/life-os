package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/calendar"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/feeds"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/ics"
)

var zawTypes = []string{"Bioabfall", "Gelber Sack", "Papier", "Restmüll Tonnen und Container 14-täglich"}

func (h *harness) ensure(preset feeds.Preset) feeds.EnsureResult {
	h.t.Helper()
	result, err := h.refresher.EnsurePreset(context.Background(), preset)
	if err != nil {
		h.t.Fatalf("EnsurePreset: %v", err)
	}
	return result
}

func (h *harness) list() []generated.Feed {
	h.t.Helper()
	var list generated.FeedList
	status, raw := h.do(http.MethodGet, "/v1/feeds", nil)
	if err := json.Unmarshal(raw, &list); status != http.StatusOK || err != nil {
		h.t.Fatalf("GET /v1/feeds: %d %s", status, raw)
	}
	return list.Items
}

const eveningBefore = `[{"kind":"localTime","daysBefore":1,"localTime":"19:00"}]`

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	l, _ := json.Marshal(left)
	r, _ := json.Marshal(right)
	return string(l) == string(r)
}

// With LIFE_OS_ZAW_ICS_URL set, every start ensures one ZAW feed; the first
// scheduler pass registers its source in Calendar ("ZAW", provider ZAW, the
// evening-before reminder) and imports the four bin types, titled by type.
func TestZAWPresetFeed(t *testing.T) {
	h := newHarness(t, nil)
	fx := loadFixture(t)
	server := &feedServer{body: fx.feed(nil)}
	feed := httptest.NewServer(server)
	defer feed.Close()
	feedURL := feed.URL + "/api.php?m=zaw&r=ical&area_id=1"

	created := h.ensure(feeds.ZAWPreset(feedURL))
	if created.Action != feeds.EnsureCreated || !created.Due || created.Feed.Preset == nil || *created.Feed.Preset != feeds.PresetZAW || !created.Feed.RegisterPending {
		t.Fatalf("first start = %+v", created)
	}
	id := types.UUID(created.Feed.ID.Bytes)
	// Restarts (new refresher and handler, same database): still one feed,
	// nothing changed.
	for range 2 {
		restarted := newHarness(t, h)
		if again := restarted.ensure(feeds.ZAWPreset(feedURL)); again.Action != feeds.EnsureUnchanged || again.Due || again.Feed.ID != created.Feed.ID {
			t.Fatalf("restart = %+v", again)
		}
	}
	listed := h.list()
	if len(listed) != 1 {
		t.Fatalf("%d feeds after restarts", len(listed))
	}
	if got := listed[0]; got.Id != id || got.Name != "ZAW" || got.Url != feedURL || !got.Enabled || got.Preset == nil || *got.Preset != "zaw" ||
		!slices.Equal(got.Filter.Include, zawTypes) || len(got.Filter.Exclude) != 0 || !got.Refresh.NextRefreshAt.Equal(fixedNow) || got.Refresh.LastOutcome != nil {
		t.Fatalf("feed = %+v", got)
	}
	if h.calendar.Count() != 0 {
		t.Fatal("registered before the scheduler ran")
	}

	// The scheduler's first pass at startup registers and imports it.
	h.runDue()
	refreshed := h.get(id)
	if got := counts(t, refreshed); outcome(refreshed) != "imported" || got.FeedEvents != 29 || got.Imported != 21 || got.Inserted != 21 || server.requests != 1 {
		t.Fatalf("initial refresh = %+v %+v", refreshed.Refresh, got)
	}
	source := h.source(id)
	if refreshed.SourceId == nil || source.ID != refreshed.SourceId.String() || source.Name != "ZAW" || source.Provider != feeds.ProviderZAW || !source.ReadOnly ||
		!sameJSON(t, source.DefaultReminders, []byte(eveningBefore)) || source.DefaultPriority != "medium" {
		t.Fatalf("registered = %+v", source.Source)
	}
	perType := map[string]int{}
	for _, event := range source.Events {
		perType[event.Item.Title]++
		if event.Item.Location == nil || *event.Item.Location != "Musterstadt, Beispielweg" {
			t.Errorf("event = %+v", event.Item)
		}
	}
	if len(source.Events) != 21 || len(perType) != 4 || perType["Bioabfall"] != 6 || perType["Gelber Sack"] != 6 ||
		perType["Papier Tonnen und Container"] != 3 || perType["Restmüll Tonnen und Container 14-täglich"] != 6 {
		t.Fatalf("imported %d events: %v", len(source.Events), perType)
	}

	// The same feed again: no duplicates, nothing rewritten.
	if got := counts(t, h.refresh(id)); got.Inserted != 0 || got.Updated != 0 || got.Unchanged != 21 {
		t.Fatalf("identical refresh = %+v", got)
	}

	// The owner's settings in Calendar outlast restarts while the
	// configuration stays.
	h.calendar.Update(id.String(), func(source *calendar.Source) {
		source.Name, source.DefaultReminders = "Müllabfuhr", json.RawMessage(`[{"kind":"relative","minutesBefore":90}]`)
	})
	restarted := newHarness(t, h)
	if again := restarted.ensure(feeds.ZAWPreset(feedURL)); again.Action != feeds.EnsureUnchanged || again.Feed.RegisterPending {
		t.Fatalf("restart after an edit = %+v", again)
	}
	restarted.runDue()
	if edited := h.source(id); edited.Name != "Müllabfuhr" || !strings.Contains(string(edited.DefaultReminders), "90") {
		t.Fatalf("edits lost on restart: %+v", edited.Source)
	}
	if mirrored := h.get(id); mirrored.Name != "Müllabfuhr" {
		t.Fatalf("mirrored name = %q", mirrored.Name)
	}

	// A new feed URL (the owner moved) is applied at the next start: due
	// right away with a full download; the owner's name and reminders stay.
	*h.clock = fixedNow.Add(time.Hour)
	movedURL := feed.URL + "/api.php?m=zaw&r=ical&area_id=2"
	moved := restarted.ensure(feeds.ZAWPreset(movedURL))
	if moved.Action != feeds.EnsureUpdated || !moved.Due || moved.Feed.Url != movedURL || !moved.Feed.NextRefreshAt.Time.Equal(*h.clock) ||
		moved.Feed.Etag != nil || moved.Feed.ID != created.Feed.ID {
		t.Fatalf("moved = %+v", moved)
	}
	restarted.runDue()
	if after := h.source(id); after.Name != "Müllabfuhr" || !strings.Contains(string(after.DefaultReminders), "90") || h.calendar.Count() != 1 {
		t.Fatalf("after the move = %+v", after.Source)
	}

	// A release that changes the preset's types or reminders replaces those
	// (the reminders in Calendar, which applies what the integration
	// changed).
	preset := feeds.ZAWPreset(movedURL)
	preset.Config.Include = append(preset.Config.Include, "Schadstoffmobil")
	preset.Config.DefaultReminders = json.RawMessage(`[{"kind":"localTime","daysBefore":1,"localTime":"18:00"}]`)
	retyped := restarted.ensure(preset)
	if retyped.Action != feeds.EnsureUpdated || !retyped.Due || len(retyped.Feed.FilterInclude) != 5 || !retyped.Feed.RegisterPending {
		t.Fatalf("preset change = %+v", retyped)
	}
	restarted.runDue()
	if after := h.source(id); !strings.Contains(string(after.DefaultReminders), "18:00") || after.Name != "Müllabfuhr" {
		t.Fatalf("preset change in Calendar = %+v", after.Source)
	}
	if len(h.list()) != 1 || h.calendar.Count() != 1 {
		t.Fatal("a configuration change added a feed")
	}

	// Unset, the feed stays as it is (the service only reports it).
	if found, ok, err := h.refresher.PresetFeed(context.Background(), feeds.PresetZAW); err != nil || !ok || found.ID != created.Feed.ID {
		t.Fatalf("PresetFeed = %v %v", ok, err)
	}
}

// A feed the owner added by hand for the same URL is taken over rather than
// duplicated, its source becoming ZAW's in Calendar; one deleted comes back
// at the next start while the environment names it.
func TestZAWPresetAdoptsAHandAddedFeed(t *testing.T) {
	h := newHarness(t, nil)
	feedURL := "https://feeds.example.test/api.php?m=zaw&r=ical&area_id=1"
	manual := h.feed(http.MethodPost, "/v1/feeds", map[string]any{"name": "Abfall", "url": "webcal://feeds.example.test/api.php?m=zaw&r=ical&area_id=1"}, http.StatusCreated)
	other := h.feed(http.MethodPost, "/v1/feeds", map[string]any{"name": "Other", "url": "https://feeds.example.test/other.ics"}, http.StatusCreated)
	h.calendar.Update(manual.Id.String(), func(source *calendar.Source) { source.Enabled = false })
	if err := h.refresher.Mirror(context.Background()); err != nil {
		t.Fatal(err)
	}

	adopted := h.ensure(feeds.ZAWPreset(feedURL))
	if adopted.Action != feeds.EnsureAdopted || adopted.Due || types.UUID(adopted.Feed.ID.Bytes) != manual.Id || !adopted.Feed.RegisterPending {
		t.Fatalf("adopted = %+v", adopted)
	}
	if _, err := h.refresher.Register(context.Background(), adopted.Feed); err != nil {
		t.Fatal(err)
	}
	got := h.get(manual.Id)
	source := h.source(manual.Id)
	if got.Name != "ZAW" || !slices.Equal(got.Filter.Include, zawTypes) || got.Enabled || source.Provider != feeds.ProviderZAW ||
		source.Name != "ZAW" || !sameJSON(t, source.DefaultReminders, []byte(eveningBefore)) || source.Enabled {
		t.Fatalf("taken over = %+v / %+v", got, source.Source)
	}
	if listed := h.list(); len(listed) != 2 || listed[1].Id != other.Id || listed[1].Name != "Other" || listed[1].Preset != nil || listed[0].Preset == nil {
		t.Fatalf("feeds = %+v", listed)
	}
	if again := h.ensure(feeds.ZAWPreset(feedURL)); again.Action != feeds.EnsureUnchanged {
		t.Fatalf("after taking over = %+v", again)
	}

	if status, _ := h.do(http.MethodDelete, "/v1/feeds/"+manual.Id.String(), nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	recreated := h.ensure(feeds.ZAWPreset(feedURL))
	if recreated.Action != feeds.EnsureCreated || !recreated.Feed.Enabled || types.UUID(recreated.Feed.ID.Bytes) == manual.Id || len(h.list()) != 2 {
		t.Fatalf("recreated = %+v", recreated)
	}

	// An unusable URL is refused without being repeated in the error.
	strict := feeds.NewRefresher(h.pool, ics.NewFetcher(0, 0, false), h.refresher.Calendar(), h.refresher.Schedule(), nil, nil)
	for _, bad := range []string{"http://feeds.example.test/abholtermine.ics?street=secret", "https://127.0.0.1/abholtermine.ics?street=secret", "not a url"} {
		_, err := strict.EnsurePreset(context.Background(), feeds.ZAWPreset(bad))
		if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "feeds.example.test") {
			t.Errorf("EnsurePreset(%q) error = %v", bad, err)
		}
	}
	if len(h.list()) != 2 {
		t.Fatal("an unusable URL changed the feeds")
	}
}
