package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/ics"
	"github.com/GAMch1k/life-os/services/calendar/internal/sources"
)

var zawTypes = []string{"Bioabfall", "Gelber Sack", "Papier", "Restmüll Tonnen und Container 14-täglich"}

func (c sourcesClient) ensure(preset sources.Preset) sources.EnsureResult {
	c.t.Helper()
	result, err := c.refresher.EnsurePreset(context.Background(), preset)
	if err != nil {
		c.t.Fatalf("EnsurePreset: %v", err)
	}
	return result
}

func (c sourcesClient) sources() []generated.CalendarSource {
	c.t.Helper()
	var list generated.SourceList
	status, raw := c.do(http.MethodGet, "/v1/sources", nil)
	if err := json.Unmarshal(raw, &list); status != http.StatusOK || err != nil {
		c.t.Fatalf("GET /v1/sources: %d %s", status, raw)
	}
	return list.Items
}

// isEveningBefore reports whether reminders are exactly "the day before at
// 19:00", the ZAW default.
func isEveningBefore(reminders *[]generated.Reminder) bool {
	if reminders == nil || len(*reminders) != 1 {
		return false
	}
	reminder := (*reminders)[0]
	return reminder.Kind == generated.LocalTime && reminder.DaysBefore != nil && *reminder.DaysBefore == 1 &&
		reminder.LocalTime != nil && *reminder.LocalTime == "19:00" && reminder.MinutesBefore == nil
}

// With CALENDAR_ZAW_ICS_URL set, every start ensures one ZAW source; the
// first start's scheduler pass imports the four bin types, titled by type,
// reminding the evening before.
func TestZAWPresetSource(t *testing.T) {
	c := newSourcesClient(t, nil, nil)
	fx := loadFixture(t)
	server := &feedServer{body: fx.feed(nil)}
	feed := httptest.NewServer(server)
	defer feed.Close()
	feedURL := feed.URL + "/api.php?m=zaw&r=ical&area_id=1"

	created := c.ensure(sources.ZAWPreset(feedURL))
	if created.Action != sources.EnsureCreated || !created.Due || created.Source.Preset == nil || *created.Source.Preset != sources.PresetZAW {
		t.Fatalf("first start = %+v", created)
	}
	id := googleuuid.UUID(created.Source.ID.Bytes)
	// Restarts (new refresher and handler, same database): still one source,
	// nothing changed.
	for range 2 {
		restarted := newSourcesClient(t, c.pool, c.clock)
		if again := restarted.ensure(sources.ZAWPreset(feedURL)); again.Action != sources.EnsureUnchanged || again.Due || again.Source.ID != created.Source.ID {
			t.Fatalf("restart = %+v", again)
		}
	}
	listed := c.sources()
	if len(listed) != 1 {
		t.Fatalf("%d sources after restarts", len(listed))
	}
	source := listed[0]
	if source.Id != id || source.Name != "ZAW" || source.Url != feedURL || !source.Enabled || !slices.Equal(source.Filter.Include, zawTypes) ||
		len(source.Filter.Exclude) != 0 || !isEveningBefore(source.DefaultReminders) || !source.Refresh.NextRefreshAt.Equal(fixedNow) || source.Refresh.LastOutcome != nil {
		t.Fatalf("source = %+v", source)
	}

	// The scheduler's first pass at startup imports it right away.
	sources.NewScheduler(c.refresher, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil))).RunDue(context.Background())
	refreshed := c.source(http.MethodGet, "/v1/sources/"+id.String(), nil, http.StatusOK)
	if got := counts(t, refreshed); outcome(refreshed) != "imported" || got.FeedEvents != 29 || got.Imported != 21 || got.Inserted != 21 || server.requests != 1 {
		t.Fatalf("initial refresh = %+v %+v", refreshed.Refresh, got)
	}
	events := c.events(id, true)
	perType := map[string]int{}
	for _, event := range events {
		perType[event.Title]++
		if event.Location == nil || *event.Location != "Musterstadt, Beispielweg" || event.Status != generated.Active ||
			event.Source.Kind != generated.EventSourceKindIcs || event.Source.Name == nil || *event.Source.Name != "ZAW" ||
			event.Reminders != nil || !isEveningBefore(event.EffectiveReminders) {
			t.Errorf("event = %+v (source %+v)", event, event.Source)
		}
	}
	if len(events) != 21 || len(perType) != 4 || perType["Bioabfall"] != 6 || perType["Gelber Sack"] != 6 ||
		perType["Papier Tonnen und Container"] != 3 || perType["Restmüll Tonnen und Container 14-täglich"] != 6 {
		t.Fatalf("imported %d events: %v", len(events), perType)
	}
	if first := events[0]; first.Title != "Papier Tonnen und Container" || first.StartAt.Format(time.RFC3339) != "2026-10-15T04:00:00Z" {
		t.Fatalf("first pickup = %q at %v", first.Title, first.StartAt)
	}
	// Search finds a pickup by type; the location stays searchable.
	if found := c.titles("/v1/events/search?" + q("q", "bioabfall beispielweg", "sourceId", id.String())); len(found) != 6 || slices.ContainsFunc(found, func(title string) bool { return title != "Bioabfall" }) {
		t.Fatalf("search = %v", found)
	}

	// The same feed again: no duplicates, nothing rewritten.
	if got := counts(t, c.refresh(id)); got.Inserted != 0 || got.Updated != 0 || got.Unchanged != 21 {
		t.Fatalf("identical refresh = %+v", got)
	}
	// A pickup removed upstream is cancelled by the source, kept as history.
	server.set(func(f *feedServer) { f.body = fx.feed([]string{"20261019"}) })
	if got := counts(t, c.refresh(id)); got.Removed != 1 || got.Imported != 20 {
		t.Fatalf("removal = %+v", got)
	}
	removed := findStart(t, c.events(id, true), "2026-10-19T04:00:00Z")
	if removed.Title != "Bioabfall" || removed.Status != generated.Cancelled || removed.RemovedFromSourceAt == nil || len(c.events(id, false)) != 20 {
		t.Fatalf("removed pickup = %+v", removed)
	}

	// Edits through the API outlast restarts while the configuration stays.
	path := "/v1/sources/" + id.String()
	c.source(http.MethodPatch, path, `{"name":"Müllabfuhr","defaultReminders":[{"kind":"relative","minutesBefore":90}]}`, http.StatusOK)
	restarted := newSourcesClient(t, c.pool, c.clock)
	if again := restarted.ensure(sources.ZAWPreset(feedURL)); again.Action != sources.EnsureUnchanged {
		t.Fatalf("restart after an edit = %+v", again)
	}
	edited := c.source(http.MethodGet, path, nil, http.StatusOK)
	if edited.Name != "Müllabfuhr" || edited.DefaultReminders == nil || *(*edited.DefaultReminders)[0].MinutesBefore != 90 {
		t.Fatalf("edits lost on restart: %+v", edited)
	}
	if event := c.events(id, false)[0]; *event.Source.Name != "Müllabfuhr" || *(*event.EffectiveReminders)[0].MinutesBefore != 90 {
		t.Fatalf("event after the edit: %+v", event.Source)
	}

	// A new feed URL (the owner moved) is applied at the next start: due
	// right away with a full download; the edited name and reminders stay.
	*c.clock = fixedNow.Add(time.Hour)
	movedURL := feed.URL + "/api.php?m=zaw&r=ical&area_id=2"
	moved := restarted.ensure(sources.ZAWPreset(movedURL))
	if moved.Action != sources.EnsureUpdated || !moved.Due || moved.Source.Url != movedURL || !moved.Source.NextRefreshAt.Time.Equal(*c.clock) ||
		moved.Source.Etag != nil || moved.Source.Name != "Müllabfuhr" || moved.Source.ID != created.Source.ID {
		t.Fatalf("moved = %+v", moved)
	}
	if after := c.source(http.MethodGet, path, nil, http.StatusOK); *(*after.DefaultReminders)[0].MinutesBefore != 90 || !slices.Equal(after.Filter.Include, zawTypes) {
		t.Fatalf("after the move = %+v", after)
	}

	// A release that changes the preset's types or reminders replaces those.
	preset := sources.ZAWPreset(movedURL)
	preset.Config.Include = append(preset.Config.Include, "Schadstoffmobil")
	preset.Config.DefaultReminders = json.RawMessage(`[{"kind":"localTime","daysBefore":1,"localTime":"18:00"}]`)
	retyped := restarted.ensure(preset)
	after := c.source(http.MethodGet, path, nil, http.StatusOK)
	if retyped.Action != sources.EnsureUpdated || !retyped.Due || len(after.Filter.Include) != 5 || *(*after.DefaultReminders)[0].LocalTime != "18:00" || after.Name != "Müllabfuhr" {
		t.Fatalf("preset change = %+v / %+v", retyped, after)
	}
	if len(c.sources()) != 1 {
		t.Fatal("a configuration change added a source")
	}

	// Unset, the source stays as it is (Calendar only reports it).
	if found, ok, err := c.refresher.PresetSource(context.Background(), sources.PresetZAW); err != nil || !ok || found.ID != created.Source.ID {
		t.Fatalf("PresetSource = %v %v", ok, err)
	}
}

// A source the owner added by hand for the same feed is taken over rather
// than duplicated, and stays disabled if it was; one deleted through the API
// comes back at the next start while the environment names it.
func TestZAWPresetAdoptsAHandAddedSource(t *testing.T) {
	c := newSourcesClient(t, nil, nil)
	feedURL := "https://feeds.example.test/api.php?m=zaw&r=ical&area_id=1"
	manual := c.source(http.MethodPost, "/v1/sources", map[string]any{"name": "Abfall", "url": "webcal://feeds.example.test/api.php?m=zaw&r=ical&area_id=1", "enabled": false}, http.StatusCreated)
	other := c.source(http.MethodPost, "/v1/sources", map[string]any{"name": "Other", "url": "https://feeds.example.test/other.ics"}, http.StatusCreated)

	adopted := c.ensure(sources.ZAWPreset(feedURL))
	if adopted.Action != sources.EnsureAdopted || adopted.Due || googleuuid.UUID(adopted.Source.ID.Bytes) != manual.Id || adopted.Source.Enabled {
		t.Fatalf("adopted = %+v", adopted)
	}
	got := c.source(http.MethodGet, "/v1/sources/"+manual.Id.String(), nil, http.StatusOK)
	if got.Name != "ZAW" || !slices.Equal(got.Filter.Include, zawTypes) || !isEveningBefore(got.DefaultReminders) || got.Enabled {
		t.Fatalf("taken over = %+v", got)
	}
	if listed := c.sources(); len(listed) != 2 || listed[1].Id != other.Id || listed[1].Name != "Other" {
		t.Fatalf("sources = %+v", listed)
	}
	if again := c.ensure(sources.ZAWPreset(feedURL)); again.Action != sources.EnsureUnchanged {
		t.Fatalf("after taking over = %+v", again)
	}

	if status, _ := c.do(http.MethodDelete, "/v1/sources/"+manual.Id.String(), nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	recreated := c.ensure(sources.ZAWPreset(feedURL))
	if recreated.Action != sources.EnsureCreated || !recreated.Source.Enabled || googleuuid.UUID(recreated.Source.ID.Bytes) == manual.Id || len(c.sources()) != 2 {
		t.Fatalf("recreated = %+v", recreated)
	}

	// An unusable URL is refused without being repeated in the error.
	strict := sources.NewRefresher(c.pool, ics.NewFetcher(0, 0, false), c.refresher.Schedule(), nil, nil)
	for _, bad := range []string{"http://feeds.example.test/abholtermine.ics?street=secret", "https://127.0.0.1/abholtermine.ics?street=secret", "not a url"} {
		_, err := strict.EnsurePreset(context.Background(), sources.ZAWPreset(bad))
		if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "feeds.example.test") {
			t.Errorf("EnsurePreset(%q) error = %v", bad, err)
		}
	}
	if len(c.sources()) != 2 {
		t.Fatal("an unusable URL changed the sources")
	}
}
