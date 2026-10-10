package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/testdb"
)

// These tests drive the integration API (P11-06) over HTTP: integrations
// register their sources, sync their events, and follow the change feed.

const (
	googleActor = "system:google-calendar"
	feedsActor  = "system:calendar-feeds"
)

// integrationClient is an apiClient on a pool the test can restart on.
type integrationClient struct {
	apiClient
	pool *pgxpool.Pool
}

func newIntegrationClient(t *testing.T) *integrationClient {
	t.Helper()
	c := &integrationClient{pool: testdb.Open(t, "")}
	c.t = t
	c.restart()
	return c
}

// restart is a Calendar restart on the same database.
func (c *integrationClient) restart() {
	handler := NewHandler(c.pool, "Europe/Berlin")
	handler.now = func() time.Time { return fixedNow }
	c.apiClient = apiClient{t: c.t, app: newTestApp(handler), handler: handler, queries: store.New(c.pool)}
}

// as sends a request as actor ("" sends none).
func (c *integrationClient) as(actor, method, target string, body any) (int, []byte) {
	c.t.Helper()
	return c.do(method, target, body, activity.ActorHeader, actor)
}

// expect sends a request that must answer status, decoding into out.
func (c *integrationClient) expect(actor, method, target string, body any, status int, out any) {
	c.t.Helper()
	got, raw := c.as(actor, method, target, body)
	if got != status {
		c.t.Fatalf("%s %s as %q: %d %s, want %d", method, target, actor, got, raw, status)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: %v in %s", method, target, err, raw)
		}
	}
}

// refused sends a request that must fail with status and code.
func (c *integrationClient) refused(actor, method, target string, body any, status int, code string) string {
	c.t.Helper()
	var envelope struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	c.expect(actor, method, target, body, status, &envelope)
	if envelope.Error.Code != code {
		c.t.Fatalf("%s %s as %q: code %q (%s), want %q", method, target, actor, envelope.Error.Code, envelope.Error.Message, code)
	}
	return envelope.Error.Message
}

func sourcePath(integration, externalID string) string {
	return "/v1/integrations/" + integration + "/sources/" + url.PathEscape(externalID)
}

func (c *integrationClient) register(actor, integration, externalID string, body map[string]any, status int) generated.IntegrationSource {
	c.t.Helper()
	var out generated.IntegrationSource
	c.expect(actor, http.MethodPut, sourcePath(integration, externalID), body, status, &out)
	return out
}

func (c *integrationClient) sync(actor, integration, externalID string, body map[string]any) generated.SyncIntegrationSourceResult {
	c.t.Helper()
	var out generated.SyncIntegrationSourceResult
	c.expect(actor, http.MethodPost, sourcePath(integration, externalID)+"/sync", body, http.StatusOK, &out)
	return out
}

// events lists the events of October and November 2026 (cancelled ones
// too), every page, with extra query parameters.
func (c *integrationClient) events(extra ...string) []generated.CalendarEvent {
	c.t.Helper()
	query := append([]string{"from", "2026-09-01T00:00:00Z", "to", "2026-12-01T00:00:00Z", "includeCancelled", "true", "limit", "100"}, extra...)
	var out generated.EventList
	c.expect("user", http.MethodGet, "/v1/events?"+q(query...), nil, http.StatusOK, &out)
	return out.Items
}

func byTitle(t *testing.T, items []generated.CalendarEvent, title string) generated.CalendarEvent {
	t.Helper()
	for _, item := range items {
		if item.Title == title {
			return item
		}
	}
	t.Fatalf("no event %q in %v", title, titles(items))
	return generated.CalendarEvent{}
}

func syncCountsOf(result generated.SyncIntegrationSourceResult) string {
	return strings.Join([]string{
		"received=" + itoa(result.Received), "inserted=" + itoa(result.Inserted), "updated=" + itoa(result.Updated),
		"removed=" + itoa(result.Removed), "restored=" + itoa(result.Restored), "unchanged=" + itoa(result.Unchanged),
	}, " ")
}

func itoa(value int) string { return strconv.Itoa(value) }

func zawRegistration(name string) map[string]any {
	return map[string]any{
		"name": name, "provider": "ZAW", "readOnly": true,
		"defaultReminders": []map[string]any{{"kind": "localTime", "daysBefore": 1, "localTime": "19:00"}},
	}
}

func TestIntegrationSourcesRegisterIdempotentlyAndKeepTheOwnersSettings(t *testing.T) {
	c := newIntegrationClient(t)
	created := c.register(feedsActor, "calendar-feeds", "zaw", zawRegistration("ZAW"), http.StatusCreated)
	if created.Integration != "calendar-feeds" || created.ExternalId != "zaw" || created.Provider != "ZAW" || created.Name != "ZAW" ||
		!created.Enabled || !created.ReadOnly || created.InProcess || created.DefaultPriority != generated.Medium ||
		created.DefaultReminders == nil || len(*created.DefaultReminders) != 1 {
		t.Fatalf("created = %+v", created)
	}
	again := c.register(feedsActor, "calendar-feeds", "zaw", zawRegistration("ZAW"), http.StatusOK)
	if again.Id != created.Id || !again.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("registering again changed it: %+v", again)
	}

	// The owner's settings, changed through /v1/sources, survive the
	// integration registering again with what it said before.
	path := "/v1/sources/" + created.Id.String()
	var listed generated.SourceList
	c.expect("user", http.MethodGet, "/v1/sources", nil, http.StatusOK, &listed)
	if len(listed.Items) != 1 || listed.Items[0].Kind != generated.CalendarSourceKindExternal || listed.Items[0].Url != "" ||
		deref(listed.Items[0].Provider) != "ZAW" || deref(listed.Items[0].Integration) != "calendar-feeds" || deref(listed.Items[0].ExternalId) != "zaw" {
		t.Fatalf("listed = %+v", listed.Items)
	}
	var owned generated.CalendarSource
	c.expect("user", http.MethodPatch, path, map[string]any{"name": "Müll", "defaultPriority": "high", "defaultReminders": nil}, http.StatusOK, &owned)
	if owned.Name != "Müll" || owned.DefaultPriority != generated.High || owned.DefaultReminders != nil {
		t.Fatalf("owner's change = %+v", owned)
	}
	kept := c.register(feedsActor, "calendar-feeds", "zaw", zawRegistration("ZAW"), http.StatusOK)
	if kept.Name != "Müll" || kept.DefaultPriority != generated.High || kept.DefaultReminders != nil {
		t.Fatalf("the owner's settings were overwritten: %+v", kept)
	}
	// What the integration changes is applied, as a Google calendar
	// renamed in Google renames its source; leaving a setting out says
	// nothing about it.
	renamed := c.register(feedsActor, "calendar-feeds", "zaw", map[string]any{"name": "ZAW Roßdorf", "provider": "ZAW feed", "readOnly": true}, http.StatusOK)
	if renamed.Name != "ZAW Roßdorf" || renamed.Provider != "ZAW feed" || renamed.DefaultReminders != nil || renamed.DefaultPriority != generated.High {
		t.Fatalf("renamed = %+v", renamed)
	}
	reminded := c.register(feedsActor, "calendar-feeds", "zaw", map[string]any{
		"name": "ZAW Roßdorf", "provider": "ZAW", "readOnly": true, "defaultPriority": "low",
		"defaultReminders": []map[string]any{{"kind": "localTime", "daysBefore": 1, "localTime": "20:00"}},
	}, http.StatusOK)
	if reminded.DefaultPriority != generated.Low || reminded.DefaultReminders == nil || deref((*reminded.DefaultReminders)[0].LocalTime) != "20:00" {
		t.Fatalf("reminded = %+v", reminded)
	}

	// A feed an integration syncs keeps its URL and filter there; the
	// owner's switch stays the owner's.
	c.refused("user", http.MethodPatch, path, map[string]any{"url": "https://calendar.example.org/zaw.ics"}, http.StatusConflict, "managed_by_integration")
	c.refused("user", http.MethodPatch, path, map[string]any{"filter": map[string]any{"include": []string{"Papier"}, "exclude": []string{}}}, http.StatusConflict, "managed_by_integration")
	c.refused("user", http.MethodPost, path+"/refresh", nil, http.StatusConflict, "managed_by_integration")
	c.expect("user", http.MethodPatch, path, map[string]any{"enabled": false}, http.StatusOK, &owned)
	if c.register(feedsActor, "calendar-feeds", "zaw", zawRegistration("ZAW Roßdorf"), http.StatusOK).Enabled {
		t.Fatal("registering enabled a source the owner disabled")
	}

	var got generated.IntegrationSource
	c.expect(feedsActor, http.MethodGet, sourcePath("calendar-feeds", "zaw"), nil, http.StatusOK, &got)
	var list generated.IntegrationSourceList
	c.expect(feedsActor, http.MethodGet, "/v1/integrations/calendar-feeds/sources", nil, http.StatusOK, &list)
	if got.Id != created.Id || len(list.Items) != 1 || list.Items[0].Id != created.Id {
		t.Fatalf("get = %+v, list = %+v", got, list)
	}

	// Ids in the path are URL-encoded, such as a Google calendar's.
	familyID := "en.german#holiday@group.v.calendar.google.com"
	family := c.register(googleActor, "google-calendar", familyID, map[string]any{"name": "Holidays", "provider": "Google", "readOnly": true}, http.StatusCreated)
	if family.ExternalId != familyID {
		t.Fatalf("family = %+v", family)
	}

	for _, body := range []map[string]any{
		{"provider": "ZAW", "readOnly": true},
		{"name": "ZAW", "readOnly": true},
		{"name": "ZAW", "provider": "ZAW"},
		{"name": " ", "provider": "ZAW", "readOnly": true},
		{"name": "ZAW", "provider": "ZAW", "readOnly": true, "defaultPriority": "urgent"},
		{"name": "ZAW", "provider": "ZAW", "readOnly": true, "defaultReminders": []map[string]any{{"kind": "relative"}}},
	} {
		c.refused(feedsActor, http.MethodPut, sourcePath("calendar-feeds", "other"), body, http.StatusBadRequest, "invalid_source")
	}

	c.expect(feedsActor, http.MethodDelete, sourcePath("calendar-feeds", "zaw"), nil, http.StatusNoContent, nil)
	c.refused(feedsActor, http.MethodGet, sourcePath("calendar-feeds", "zaw"), nil, http.StatusNotFound, "not_found")
	c.refused(feedsActor, http.MethodDelete, sourcePath("calendar-feeds", "zaw"), nil, http.StatusNotFound, "not_found")
}

func TestIntegrationEndpointsAdmitOnlyTheirIntegration(t *testing.T) {
	c := newIntegrationClient(t)
	c.register(googleActor, "google-calendar", "owner@example.com", map[string]any{"name": "Owner", "provider": "Google", "readOnly": false}, http.StatusCreated)
	sync := map[string]any{"mode": "snapshot", "items": []any{}}
	path := sourcePath("google-calendar", "owner@example.com")
	for _, actor := range []string{feedsActor, "user", "agent:life-os"} {
		c.refused(actor, http.MethodGet, path, nil, http.StatusForbidden, "forbidden")
		c.refused(actor, http.MethodGet, "/v1/integrations/google-calendar/sources", nil, http.StatusForbidden, "forbidden")
		c.refused(actor, http.MethodPut, path, map[string]any{"name": "Mine", "provider": "Google", "readOnly": false}, http.StatusForbidden, "forbidden")
		c.refused(actor, http.MethodPost, path+"/sync", sync, http.StatusForbidden, "forbidden")
		c.refused(actor, http.MethodDelete, path, nil, http.StatusForbidden, "forbidden")
	}
	// Reads without an actor are refused too; writes need one first.
	c.refused("", http.MethodGet, path, nil, http.StatusForbidden, "forbidden")
	c.refused("", http.MethodPost, path+"/sync", sync, http.StatusBadRequest, activity.ActorRequiredCode)
	for _, actor := range []string{"user", "agent:life-os", ""} {
		c.refused(actor, http.MethodGet, "/v1/integrations/changes", nil, http.StatusForbidden, "forbidden")
	}
	c.refused(googleActor, http.MethodGet, "/v1/integrations/Google/sources", nil, http.StatusBadRequest, "bad_request")
	// The integration itself and dev tools are admitted.
	c.expect(googleActor, http.MethodGet, path, nil, http.StatusOK, nil)
	c.expect("dev:claude-code", http.MethodGet, path, nil, http.StatusOK, nil)
	c.expect(feedsActor, http.MethodGet, "/v1/integrations/changes", nil, http.StatusOK, nil)
	c.expect("dev:claude-code", http.MethodPost, path+"/sync", sync, http.StatusOK, nil)
}

func TestIntegrationCannotTakeOverSourcesCalendarStillSyncs(t *testing.T) {
	c := newIntegrationClient(t)
	legacy := c.newSource("Club feed")
	var listed generated.IntegrationSourceList
	c.expect(feedsActor, http.MethodGet, "/v1/integrations/calendar-feeds/sources", nil, http.StatusOK, &listed)
	if len(listed.Items) != 1 || listed.Items[0].ExternalId != legacy.String() || !listed.Items[0].InProcess || listed.Items[0].Provider != "ICS" {
		t.Fatalf("listed = %+v", listed.Items)
	}
	path := sourcePath("calendar-feeds", legacy.String())
	c.refused(feedsActor, http.MethodPut, path, map[string]any{"name": "Club", "provider": "ICS", "readOnly": true}, http.StatusConflict, "source_in_process")
	c.refused(feedsActor, http.MethodPost, path+"/sync", map[string]any{"mode": "snapshot", "items": []any{}}, http.StatusConflict, "source_in_process")
	c.refused(feedsActor, http.MethodDelete, path, nil, http.StatusConflict, "source_in_process")
	if source, err := c.queries.GetSource(context.Background(), pgID(legacy)); err != nil || source.Name != "Club feed" || !source.InProcess {
		t.Fatalf("legacy source = %+v, %v", source, err)
	}
}

// feedItems are a feed's events: two pickups, a concert with its own
// reminders and priority, a cancelled event, and a past one.
func feedItems(without ...string) []map[string]any {
	all := []map[string]any{
		{"externalUid": "pickup-1", "title": "Papier", "allDay": true, "startDate": "2026-10-14", "endDate": "2026-10-14"},
		{"externalUid": "pickup-2", "title": "Bioabfall", "allDay": true, "startDate": "2026-10-21", "endDate": "2026-10-21"},
		{
			"externalUid": "concert", "title": "Concert", "allDay": false, "startAt": "2026-10-15T19:00:00+02:00", "endAt": "2026-10-15T21:30:00+02:00",
			"timeZone": "Europe/Berlin", "location": "Hall", "description": "Row 5", "priority": "high",
			"reminders": map[string]any{"useDefault": false, "reminders": []map[string]any{{"kind": "relative", "minutesBefore": 60}}},
		},
		{"externalUid": "called-off", "title": "Market", "allDay": false, "startAt": "2026-10-16T08:00:00Z", "endAt": "2026-10-16T12:00:00Z", "status": "cancelled"},
		{"externalUid": "past", "title": "Fair", "allDay": false, "startAt": "2026-10-01T08:00:00Z", "endAt": "2026-10-01T12:00:00Z"},
	}
	var out []map[string]any
	for _, item := range all {
		if !slices.Contains(without, item["externalUid"].(string)) {
			out = append(out, item)
		}
	}
	return out
}

func TestIntegrationSnapshotSyncIsIdempotentAndRemovesWhatItLeavesOut(t *testing.T) {
	c := newIntegrationClient(t)
	c.register(feedsActor, "calendar-feeds", "zaw", zawRegistration("ZAW"), http.StatusCreated)
	snapshot := func(window map[string]any, without ...string) generated.SyncIntegrationSourceResult {
		body := map[string]any{"mode": "snapshot", "items": feedItems(without...)}
		if window != nil {
			body["window"] = window
		}
		return c.sync(feedsActor, "calendar-feeds", "zaw", body)
	}
	if got := syncCountsOf(snapshot(nil)); got != "received=5 inserted=5 updated=0 removed=0 restored=0 unchanged=0" {
		t.Fatalf("first snapshot: %s", got)
	}
	if got := syncCountsOf(snapshot(nil)); got != "received=5 inserted=0 updated=0 removed=0 restored=0 unchanged=5" {
		t.Fatalf("the same snapshot again: %s", got)
	}
	items := c.events("sourceKind", "external")
	concert := byTitle(t, items, "Concert")
	if concert.Source.Kind != generated.EventSourceKindExternal || deref(concert.Source.Integration) != "calendar-feeds" || deref(concert.Source.Provider) != "ZAW" ||
		concert.Source.ReadOnly == nil || !*concert.Source.ReadOnly || deref(concert.Source.Name) != "ZAW" || deref(concert.Source.ExternalUid) != "concert" ||
		concert.Editable || concert.ReadOnlyReason == nil || deref(concert.TimeZone) != "Europe/Berlin" || deref(concert.Location) != "Hall" ||
		deref(concert.Description) != "Row 5" || concert.Priority != generated.High || concert.UseDefaultReminders || remindersOf(concert.Reminders) != "60" ||
		!concert.StartAt.Equal(time.Date(2026, 10, 15, 17, 0, 0, 0, time.UTC)) {
		t.Fatalf("concert = %+v", concert)
	}
	papier := byTitle(t, items, "Papier")
	if !papier.AllDay || papier.StartDate.Format(time.DateOnly) != "2026-10-14" || !papier.UseDefaultReminders || len(papier.EffectiveReminders) != 1 ||
		papier.Priority != generated.Medium {
		t.Fatalf("papier = %+v", papier)
	}
	if market := byTitle(t, items, "Market"); market.Status != generated.Cancelled || market.RemovedFromSourceAt != nil {
		t.Fatalf("market = %+v", market)
	}
	if len(c.events("sourceKind", "manual")) != 0 || len(c.events("editable", "true")) != 0 {
		t.Fatal("the feed's events count as Life OS or editable events")
	}

	// Left out: cancelled and marked removed (history), restored when back.
	if got := syncCountsOf(snapshot(nil, "pickup-2", "past")); got != "received=3 inserted=0 updated=0 removed=2 restored=0 unchanged=3" {
		t.Fatalf("snapshot without two: %s", got)
	}
	if gone := byTitle(t, c.events(), "Bioabfall"); gone.Status != generated.Cancelled || gone.RemovedFromSourceAt == nil {
		t.Fatalf("removed = %+v", gone)
	}
	if got := syncCountsOf(snapshot(nil)); got != "received=5 inserted=0 updated=0 removed=0 restored=2 unchanged=3" {
		t.Fatalf("snapshot with them back: %s", got)
	}
	if back := byTitle(t, c.events(), "Bioabfall"); back.Status != generated.Active || back.RemovedFromSourceAt != nil {
		t.Fatalf("restored = %+v", back)
	}

	// upcomingOnly: a feed listing only what is still to come leaves the
	// events that already started alone (ICS feeds); start: a window
	// leaves what ended before it alone (Google's time window).
	if got := syncCountsOf(snapshot(map[string]any{"upcomingOnly": true}, "past")); got != "received=4 inserted=0 updated=0 removed=0 restored=0 unchanged=4" {
		t.Fatalf("upcoming-only snapshot without a past event: %s", got)
	}
	if got := syncCountsOf(snapshot(map[string]any{"start": "2026-10-02T00:00:00Z"}, "past")); got != "received=4 inserted=0 updated=0 removed=0 restored=0 unchanged=4" {
		t.Fatalf("windowed snapshot without an event before the window: %s", got)
	}
	if got := syncCountsOf(snapshot(map[string]any{"start": "2026-09-01T00:00:00Z"}, "past")); got != "received=4 inserted=0 updated=0 removed=1 restored=0 unchanged=4" {
		t.Fatalf("windowed snapshot without an event in the window: %s", got)
	}

	// An empty snapshot with emptyGuard keeps the upcoming events.
	c.refused(feedsActor, http.MethodPost, sourcePath("calendar-feeds", "zaw")+"/sync", map[string]any{"mode": "snapshot", "items": []any{}, "emptyGuard": true}, http.StatusConflict, "empty_snapshot")
	if back := byTitle(t, c.events(), "Papier"); back.Status != generated.Active {
		t.Fatalf("after a refused empty snapshot: %+v", back)
	}

	// A disabled source refuses syncs and keeps its events.
	var source generated.IntegrationSource
	c.expect(feedsActor, http.MethodGet, sourcePath("calendar-feeds", "zaw"), nil, http.StatusOK, &source)
	c.expect("user", http.MethodPatch, "/v1/sources/"+source.Id.String(), map[string]any{"enabled": false}, http.StatusOK, nil)
	c.refused(feedsActor, http.MethodPost, sourcePath("calendar-feeds", "zaw")+"/sync", map[string]any{"mode": "snapshot", "items": []any{}}, http.StatusConflict, "source_disabled")
	c.refused(feedsActor, http.MethodPost, sourcePath("calendar-feeds", "nope")+"/sync", map[string]any{"mode": "snapshot", "items": []any{}}, http.StatusNotFound, "not_found")
}

func TestIntegrationDeltaSyncChangesOnlyWhatItNames(t *testing.T) {
	c := newIntegrationClient(t)
	c.register(googleActor, "google-calendar", "owner@example.com", map[string]any{"name": "Owner", "provider": "Google", "readOnly": true}, http.StatusCreated)
	delta := func(items []map[string]any, deleted ...string) string {
		body := map[string]any{"mode": "delta", "items": items}
		if deleted != nil {
			body["deleted"] = deleted
		}
		return syncCountsOf(c.sync(googleActor, "google-calendar", "owner@example.com", body))
	}
	dentist := map[string]any{"externalUid": "dentist", "title": "Dentist", "allDay": false, "startAt": "2026-10-12T09:30:00+02:00", "endAt": "2026-10-12T10:15:00+02:00"}
	trip := map[string]any{"externalUid": "trip", "title": "Trip", "allDay": true, "startDate": "2026-10-20", "endDate": "2026-10-22"}
	gym := map[string]any{"externalUid": "gym", "title": "Gym", "allDay": false, "startAt": "2026-10-13T18:00:00+02:00", "endAt": "2026-10-13T19:00:00+02:00"}
	if got := delta([]map[string]any{dentist, trip, gym}); got != "received=3 inserted=3 updated=0 removed=0 restored=0 unchanged=0" {
		t.Fatalf("first delta: %s", got)
	}
	if got := delta([]map[string]any{dentist, trip}); got != "received=2 inserted=0 updated=0 removed=0 restored=0 unchanged=2" {
		t.Fatalf("the same delta again: %s", got)
	}
	moved := map[string]any{"externalUid": "dentist", "title": "Dentist", "allDay": false, "startAt": "2026-10-12T11:00:00+02:00", "endAt": "2026-10-12T11:45:00+02:00"}
	if got := delta([]map[string]any{moved}, "trip", "unknown"); got != "received=1 inserted=0 updated=1 removed=1 restored=0 unchanged=0" {
		t.Fatalf("a change and deletions: %s", got)
	}
	if got := delta([]map[string]any{}, "trip"); got != "received=0 inserted=0 updated=0 removed=0 restored=0 unchanged=0" {
		t.Fatalf("deleted again: %s", got)
	}
	items := c.events()
	if byTitle(t, items, "Dentist").StartAt.Hour() != 9 {
		t.Fatalf("dentist = %+v", byTitle(t, items, "Dentist"))
	}
	if gone := byTitle(t, items, "Trip"); gone.Status != generated.Cancelled || gone.RemovedFromSourceAt == nil {
		t.Fatalf("trip = %+v", gone)
	}
	if kept := byTitle(t, items, "Gym"); kept.Status != generated.Active {
		t.Fatalf("an event the delta did not name changed: %+v", kept)
	}
	if got := delta([]map[string]any{trip}); got != "received=1 inserted=0 updated=0 removed=0 restored=1 unchanged=0" {
		t.Fatalf("trip back: %s", got)
	}
}

func TestIntegrationSeriesWithExceptionsRoundTrip(t *testing.T) {
	c := newIntegrationClient(t)
	c.register(googleActor, "google-calendar", "owner@example.com", map[string]any{"name": "Owner", "provider": "Google", "readOnly": true}, http.StatusCreated)
	standup := func(exceptions ...map[string]any) map[string]any {
		return map[string]any{
			"externalUid": "standup", "title": "Standup", "description": "Daily sync", "allDay": false,
			"startAt": "2026-10-12T09:00:00+02:00", "endAt": "2026-10-12T09:15:00+02:00", "timeZone": "Europe/Berlin",
			"recurrence": map[string]any{"frequency": "weekly", "interval": 1, "byWeekday": []string{"WE", "MO"}, "until": "2026-12-31T22:59:59Z"},
			"exceptions": exceptions, "reminders": map[string]any{"useDefault": true, "reminders": []any{}},
		}
	}
	skipped := map[string]any{"recurrenceId": "2026-10-14T09:00:00+02:00", "cancelled": true}
	movedOne := map[string]any{"recurrenceId": "2026-10-19T07:00:00Z", "changes": map[string]any{"title": "Standup (planning)", "startAt": "2026-10-19T08:00:00Z", "endAt": "2026-10-19T09:00:00Z"}}
	changed := map[string]any{"recurrenceId": "2026-10-21T07:00:00Z", "changes": map[string]any{"description": nil, "location": "Room 2", "priority": "high"}}
	sync := func(item map[string]any) string {
		return syncCountsOf(c.sync(googleActor, "google-calendar", "owner@example.com", map[string]any{"mode": "snapshot", "items": []any{item}}))
	}
	if got := sync(standup(skipped, movedOne, changed)); got != "received=1 inserted=1 updated=0 removed=0 restored=0 unchanged=0" {
		t.Fatalf("series: %s", got)
	}
	if got := sync(standup(skipped, movedOne, changed)); got != "received=1 inserted=0 updated=0 removed=0 restored=0 unchanged=1" {
		t.Fatalf("series again: %s", got)
	}
	var list generated.EventList
	c.expect("user", http.MethodGet, "/v1/events?"+q("from", "2026-10-12T00:00:00Z", "to", "2026-10-24T00:00:00Z", "includeCancelled", "true"), nil, http.StatusOK, &list)
	got := occurrenceLabels(list.Items)
	want := []string{
		"Standup 2026-10-12T07:00:00Z 2026-10-12T07:00:00Z",
		"Standup 2026-10-14T07:00:00Z 2026-10-14T07:00:00Z cancelled",
		"Standup (planning) 2026-10-19T08:00:00Z 2026-10-19T07:00:00Z modified",
		"Standup 2026-10-21T07:00:00Z 2026-10-21T07:00:00Z modified",
	}
	equal(t, "occurrences", got, want)
	last := list.Items[len(list.Items)-1]
	if last.Description != nil || deref(last.Location) != "Room 2" || last.Priority != generated.High || last.Recurrence == nil ||
		strings.Join(weekdayCodes(last.Recurrence.ByWeekday), ",") != "MO,WE" {
		t.Fatalf("changed occurrence = %+v", last)
	}

	// The stored exceptions are what was sent, in Calendar's form.
	series := byTitle(t, list.Items, "Standup")
	rows, err := c.queries.ListEventExceptions(context.Background(), []pgtype.UUID{pgUUID(series.Id)})
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]string{}
	for _, row := range rows {
		label := string(row.Changes)
		if row.CancelledAt.Valid {
			label += " cancelled"
		}
		stored[row.RecurrenceID] = label
	}
	if stored["2026-10-14T07:00:00Z"] != "{} cancelled" || !sameJSON([]byte(stored["2026-10-21T07:00:00Z"]), []byte(`{"description":null,"location":"Room 2","priority":"high"}`)) ||
		!sameJSON([]byte(stored["2026-10-19T07:00:00Z"]), []byte(`{"title":"Standup (planning)","startAt":"2026-10-19T08:00:00Z","endAt":"2026-10-19T09:00:00Z"}`)) {
		t.Fatalf("stored exceptions = %v", stored)
	}

	// Dropping an exception gives the occurrence back to its series.
	if got := sync(standup(skipped, movedOne)); got != "received=1 inserted=0 updated=1 removed=0 restored=0 unchanged=0" {
		t.Fatalf("one exception less: %s", got)
	}
	equal(t, "exceptions", c.exceptionKeys(series.Id), []string{"2026-10-14T07:00:00Z", "2026-10-19T07:00:00Z"})
}

func weekdayCodes(days *[]generated.Weekday) []string {
	if days == nil {
		return nil
	}
	out := make([]string, 0, len(*days))
	for _, day := range *days {
		out = append(out, string(day))
	}
	return out
}

func TestIntegrationSyncKeepsTheOwnersRemindersAndPriority(t *testing.T) {
	c := newIntegrationClient(t)
	c.register(googleActor, "google-calendar", "family", map[string]any{"name": "Family", "provider": "Google", "readOnly": true}, http.StatusCreated)
	item := func(uid, title string, minutes int) map[string]any {
		return map[string]any{
			"externalUid": uid, "title": title, "allDay": false, "startAt": "2026-10-14T18:00:00+02:00", "endAt": "2026-10-14T19:00:00+02:00",
			"reminders": map[string]any{"useDefault": true, "reminders": []map[string]any{{"kind": "relative", "minutesBefore": minutes}}},
		}
	}
	sync := func(minutes int) {
		c.sync(googleActor, "google-calendar", "family", map[string]any{"mode": "snapshot", "items": []any{item("dinner", "Dinner", minutes), item("call", "Call", minutes)}})
	}
	sync(30)
	dinner := byTitle(t, c.events(), "Dinner")
	// The owner gives one event their own reminders and priority (allowed
	// on a read-only source's event); anything else is refused.
	var changed generated.CalendarEvent
	c.expect("user", http.MethodPatch, "/v1/events/"+dinner.Id.String(), map[string]any{
		"reminders": []map[string]any{{"kind": "relative", "minutesBefore": 120}}, "useDefaultReminders": false, "priority": "high",
	}, http.StatusOK, &changed)
	c.refused("user", http.MethodPatch, "/v1/events/"+dinner.Id.String(), map[string]any{"title": "Mine"}, http.StatusConflict, "managed_by_source")
	c.refused("user", http.MethodDelete, "/v1/events/"+dinner.Id.String(), nil, http.StatusConflict, "managed_by_source")

	// Syncs keep the owner's choice, also when the source's reminders
	// change; the other event follows its source.
	sync(30)
	sync(45)
	items := c.events()
	if kept := byTitle(t, items, "Dinner"); remindersOf(kept.Reminders) != "120" || kept.UseDefaultReminders || kept.Priority != generated.High {
		t.Fatalf("the owner's dinner = %+v", kept)
	}
	if followed := byTitle(t, items, "Call"); remindersOf(followed.Reminders) != "45" || !followed.UseDefaultReminders || followed.Priority != generated.Medium {
		t.Fatalf("call = %+v", followed)
	}
	// A source that carries priorities sets them (as Google's Life OS
	// property does); default is none of the event's own.
	withPriority := item("call", "Call", 45)
	withPriority["priority"] = "low"
	c.sync(googleActor, "google-calendar", "family", map[string]any{"mode": "delta", "items": []any{withPriority}})
	if low := byTitle(t, c.events(), "Call"); low.Priority != generated.Low {
		t.Fatalf("call with priority = %+v", low)
	}
	withPriority["priority"] = "default"
	c.sync(googleActor, "google-calendar", "family", map[string]any{"mode": "delta", "items": []any{withPriority}})
	if medium := byTitle(t, c.events(), "Call"); medium.Priority != generated.Medium {
		t.Fatalf("call back to the default = %+v", medium)
	}
}

func TestIntegrationSyncValidation(t *testing.T) {
	c := newIntegrationClient(t)
	c.register(googleActor, "google-calendar", "owner", map[string]any{"name": "Owner", "provider": "Google", "readOnly": true}, http.StatusCreated)
	timed := func(uid string, change func(map[string]any)) map[string]any {
		item := map[string]any{"externalUid": uid, "title": "Event", "allDay": false, "startAt": "2026-10-14T18:00:00Z", "endAt": "2026-10-14T19:00:00Z"}
		if change != nil {
			change(item)
		}
		return item
	}
	series := func(exceptions ...map[string]any) map[string]any {
		return timed("series", func(item map[string]any) {
			item["recurrence"] = map[string]any{"frequency": "daily", "interval": 1, "count": 5}
			item["exceptions"] = exceptions
		})
	}
	cases := []struct {
		name, code, message string
		body                map[string]any
	}{
		{"no mode", "invalid_sync_request", "mode", map[string]any{"items": []any{}}},
		{"an unknown mode", "invalid_sync_request", "mode", map[string]any{"mode": "full", "items": []any{}}},
		{"no items", "invalid_sync_request", "items", map[string]any{"mode": "delta"}},
		{"deleted in a snapshot", "invalid_sync_request", "deleted", map[string]any{"mode": "snapshot", "items": []any{}, "deleted": []string{"a"}}},
		{"emptyGuard in a delta", "invalid_sync_request", "emptyGuard", map[string]any{"mode": "delta", "items": []any{}, "emptyGuard": true}},
		{"both windows", "invalid_sync_request", "window", map[string]any{"mode": "snapshot", "items": []any{}, "window": map[string]any{"start": "2026-01-01T00:00:00Z", "upcomingOnly": true}}},
		{"a bad feed cursor", "invalid_sync_request", "feedCursor", map[string]any{"mode": "delta", "items": []any{}, "feedCursor": "nope"}},
		{"a uid twice", "duplicate_uid", "items[1]", map[string]any{"mode": "delta", "items": []any{timed("a", nil), timed("a", nil)}}},
		{"a uid also deleted", "duplicate_uid", "deleted[0]", map[string]any{"mode": "delta", "items": []any{timed("a", nil)}, "deleted": []string{"a"}}},
		{"no uid", "invalid_item", "externalUid", map[string]any{"mode": "delta", "items": []any{timed("", nil)}}},
		{"no title", "invalid_item", "title", map[string]any{"mode": "delta", "items": []any{timed("a", func(item map[string]any) { delete(item, "title") })}}},
		{"an end before the start", "invalid_item", "endAt must be after startAt", map[string]any{"mode": "delta", "items": []any{timed("a", func(item map[string]any) { item["endAt"] = "2026-10-14T17:00:00Z" })}}},
		{"an all-day zone", "invalid_item", "timeZone", map[string]any{"mode": "delta", "items": []any{map[string]any{"externalUid": "a", "title": "Day", "allDay": true, "startDate": "2026-10-14", "endDate": "2026-10-14", "timeZone": "Europe/Berlin"}}}},
		{"an unknown status", "invalid_item", "status", map[string]any{"mode": "delta", "items": []any{timed("a", func(item map[string]any) { item["status"] = "tentative" })}}},
		{"an unknown priority", "invalid_item", "priority", map[string]any{"mode": "delta", "items": []any{timed("a", func(item map[string]any) { item["priority"] = "urgent" })}}},
		{"a bad reminder", "invalid_item", "relative", map[string]any{"mode": "delta", "items": []any{timed("a", func(item map[string]any) {
			item["reminders"] = map[string]any{"useDefault": false, "reminders": []map[string]any{{"kind": "relative"}}}
		})}}},
		{"a bad rule", "invalid_item", "recurrence", map[string]any{"mode": "delta", "items": []any{timed("a", func(item map[string]any) {
			item["recurrence"] = map[string]any{"frequency": "daily", "interval": 1, "count": 5, "until": "2026-12-01T00:00:00Z"}
		})}}},
		{"exceptions without a rule", "invalid_exception", "only for a series", map[string]any{"mode": "delta", "items": []any{timed("a", func(item map[string]any) {
			item["exceptions"] = []map[string]any{{"recurrenceId": "2026-10-15T18:00:00Z", "cancelled": true}}
		})}}},
		{"a date in a timed series", "invalid_exception", "recurrenceId", map[string]any{"mode": "delta", "items": []any{series(map[string]any{"recurrenceId": "2026-10-15", "cancelled": true})}}},
		{"an exception that changes nothing", "invalid_exception", "change something", map[string]any{"mode": "delta", "items": []any{series(map[string]any{"recurrenceId": "2026-10-15T18:00:00Z"})}}},
		{"half a move", "invalid_exception", "both its start and its end", map[string]any{"mode": "delta", "items": []any{series(map[string]any{"recurrenceId": "2026-10-15T18:00:00Z", "changes": map[string]any{"startAt": "2026-10-15T19:00:00Z"}})}}},
		{"an all-day move in a timed series", "invalid_exception", "startAt and endAt", map[string]any{"mode": "delta", "items": []any{series(map[string]any{"recurrenceId": "2026-10-15T18:00:00Z", "changes": map[string]any{"startDate": "2026-10-16", "endDate": "2026-10-16"}})}}},
		{"an exception twice", "invalid_exception", "already has", map[string]any{"mode": "delta", "items": []any{series(
			map[string]any{"recurrenceId": "2026-10-15T18:00:00Z", "cancelled": true}, map[string]any{"recurrenceId": "2026-10-15T20:00:00+02:00", "cancelled": true})}}},
	}
	for _, tc := range cases {
		message := c.refused(googleActor, http.MethodPost, sourcePath("google-calendar", "owner")+"/sync", tc.body, http.StatusBadRequest, tc.code)
		if !strings.Contains(message, tc.message) {
			t.Errorf("%s: message %q does not say %q", tc.name, message, tc.message)
		}
	}
	if items := c.events(); len(items) != 0 {
		t.Fatalf("a refused batch wrote events: %v", titles(items))
	}
}
