package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/testdb"
)

// These tests drive the real HTTP API (actor gate included) against a fresh,
// fully migrated schema on CALENDAR_TEST_DATABASE_URL. The owner lives in
// Berlin, and "now" is fixed at Saturday 2026-10-10 12:00 Berlin.
var fixedNow = time.Date(2026, time.October, 10, 10, 0, 0, 0, time.UTC)

// fiberTestConfig leaves a database round trip more than Fiber's 1s default.
var fiberTestConfig = fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true}

type apiClient struct {
	t       *testing.T
	app     *httpserver.App
	handler *Handler
	queries *store.Queries
}

func newAPIClient(t *testing.T) apiClient {
	t.Helper()
	pool := testdb.Open(t, "")
	queries := store.New(pool)
	handler := NewHandler(pool, "Europe/Berlin")
	handler.now = func() time.Time { return fixedNow }
	return apiClient{t: t, app: newTestApp(handler), handler: handler, queries: queries}
}

// do sends a request (writes as dev:test) and returns status and body.
func (c apiClient) do(method, target string, body any, headers ...string) (int, []byte) {
	c.t.Helper()
	var reader io.Reader
	switch value := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(value)
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("content-type", "application/json")
	req.Header.Set(activity.ActorHeader, "dev:test")
	for index := 0; index+1 < len(headers); index += 2 {
		req.Header.Set(headers[index], headers[index+1])
	}
	resp, err := c.app.Test(req, fiberTestConfig)
	if err != nil {
		c.t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp.StatusCode, raw
}

func (c apiClient) event(method, target string, body any, wantStatus int) generated.CalendarEvent {
	c.t.Helper()
	status, raw := c.do(method, target, body)
	if status != wantStatus {
		c.t.Fatalf("%s %s: %d %s, want %d", method, target, status, raw, wantStatus)
	}
	var out generated.CalendarEvent
	if err := json.Unmarshal(raw, &out); err != nil {
		c.t.Fatalf("%s %s: %v in %s", method, target, err, raw)
	}
	return out
}

func (c apiClient) list(target string) generated.EventList {
	c.t.Helper()
	status, raw := c.do(http.MethodGet, target, nil)
	if status != http.StatusOK {
		c.t.Fatalf("GET %s: %d %s", target, status, raw)
	}
	var out generated.EventList
	if err := json.Unmarshal(raw, &out); err != nil {
		c.t.Fatal(err)
	}
	return out
}

// titles lists every page of target (following nextCursor) as titles.
func (c apiClient) titles(target string) []string {
	c.t.Helper()
	var titles []string
	next := target
	for pages := 0; ; pages++ {
		if pages > 50 {
			c.t.Fatalf("%s: too many pages", target)
		}
		page := c.list(next)
		for _, item := range page.Items {
			titles = append(titles, item.Title)
		}
		if page.NextCursor == nil {
			return titles
		}
		next = target + "&cursor=" + url.QueryEscape(*page.NextCursor)
	}
}

// seed stores an event the way Calendar's own source adapters will, bypassing
// the API (which only creates manual events).
func (c apiClient) seed(params store.CreateEventParams) store.Event {
	c.t.Helper()
	if params.Status == "" {
		params.Status = "active"
	}
	if params.SourceKind == "" {
		params.SourceKind = "manual"
	}
	if params.Reminders == nil {
		params.Reminders, params.UseDefaultReminders = []byte(`[]`), true
	}
	item, err := c.queries.CreateEvent(context.Background(), params)
	if err != nil {
		c.t.Fatalf("seed %q: %v", params.Title, err)
	}
	return item
}

// newSource adds a feed calendar-feeds syncs (kind external, read-only,
// known there by the source's own id); imported events must belong to a
// source.
func (c apiClient) newSource(name string) googleuuid.UUID {
	c.t.Helper()
	var id pgtype.UUID
	err := c.handler.pool.QueryRow(context.Background(), `
WITH new AS (SELECT gen_random_uuid() AS id)
INSERT INTO calendar_sources (id, kind, name, integration, external_id, provider)
VALUES ((SELECT id FROM new), 'external', $1, 'calendar-feeds', (SELECT id FROM new)::text, 'ICS')
RETURNING id`, name).Scan(&id)
	if err != nil {
		c.t.Fatalf("source %q: %v", name, err)
	}
	return id.Bytes
}

func timedParams(title, start string, minutes int) store.CreateEventParams {
	begin := *instant(start)
	return store.CreateEventParams{Title: title, StartAt: pgtype.Timestamptz{Time: begin, Valid: true}, EndAt: pgtype.Timestamptz{Time: begin.Add(time.Duration(minutes) * time.Minute), Valid: true}}
}

func importedParams(source googleuuid.UUID, uid, title, start string) store.CreateEventParams {
	params := timedParams(title, start, 30)
	params.SourceKind, params.SourceID, params.ExternalUid = "external", pgtype.UUID{Bytes: source, Valid: true}, &uid
	return params
}

func q(values ...string) string {
	query := url.Values{}
	for index := 0; index+1 < len(values); index += 2 {
		query.Add(values[index], values[index+1])
	}
	return query.Encode()
}

func equal(t *testing.T, label string, got, want []string) {
	t.Helper()
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Errorf("%s:\n got  %q\n want %q", label, got, want)
	}
}

func TestEventLifecycleOverHTTP(t *testing.T) {
	c := newAPIClient(t)
	created := c.event(http.MethodPost, "/v1/events", map[string]any{
		"title": "Dentist", "description": "Bring card", "location": "Main St 1", "allDay": false,
		"startAt": "2026-11-17T09:30:00+01:00", "endAt": "2026-11-17T10:15:00+01:00", "timeZone": "Europe/Berlin",
		"reminders": []map[string]any{{"kind": "relative", "minutesBefore": 30}, {"kind": "localTime", "daysBefore": 1, "localTime": "19:00"}},
	}, http.StatusCreated)
	if created.Status != generated.Active || created.Source.Kind != generated.EventSourceKindManual || created.Source.SourceId != nil || created.CancelledAt != nil {
		t.Fatalf("created = %+v", created)
	}
	// The offset instant is stored and returned as the same instant in UTC,
	// with the zone it was scheduled in kept beside it.
	status, raw := c.do(http.MethodGet, "/v1/events/"+created.Id.String(), nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"startAt":"2026-11-17T08:30:00Z"`) || !strings.Contains(string(raw), `"endAt":"2026-11-17T09:15:00Z"`) || !strings.Contains(string(raw), `"timeZone":"Europe/Berlin"`) {
		t.Fatalf("GET: %d %s", status, raw)
	}
	if !strings.Contains(string(raw), `"reminders":[{"kind":"relative","minutesBefore":30},{"daysBefore":1,"kind":"localTime","localTime":"19:00"}]`) {
		t.Fatalf("reminders did not round trip: %s", raw)
	}

	updated := c.event(http.MethodPatch, "/v1/events/"+created.Id.String(), `{"location":null,"description":"Bring card and X-ray","reminders":[]}`, http.StatusOK)
	if updated.Location != nil || *updated.Description != "Bring card and X-ray" || len(updated.Reminders) != 0 || !updated.UseDefaultReminders || updated.Title != "Dentist" || !updated.StartAt.Equal(*created.StartAt) {
		t.Fatalf("partial update = %+v", updated)
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) || !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("timestamps: created %v/%v, updated %v/%v", created.CreatedAt, created.UpdatedAt, updated.CreatedAt, updated.UpdatedAt)
	}
	allDay := c.event(http.MethodPatch, "/v1/events/"+created.Id.String(), `{"allDay":true,"startDate":"2026-11-17","endDate":"2026-11-18"}`, http.StatusOK)
	if !allDay.AllDay || allDay.StartAt != nil || allDay.EndAt != nil || allDay.TimeZone != nil || allDay.StartDate.String() != "2026-11-17" || allDay.EndDate.String() != "2026-11-18" {
		t.Fatalf("switch to all-day = %+v", allDay)
	}
	if status, raw := c.do(http.MethodPatch, "/v1/events/"+created.Id.String(), `{"endDate":"2026-11-16"}`); status != http.StatusBadRequest || !strings.Contains(string(raw), "endDate must not precede startDate") {
		t.Fatalf("invalid update: %d %s", status, raw)
	}

	window := "/v1/events?" + q("from", "2026-11-01T00:00:00Z", "to", "2026-12-01T00:00:00Z")
	cancelled := c.event(http.MethodPatch, "/v1/events/"+created.Id.String(), `{"status":"cancelled"}`, http.StatusOK)
	if cancelled.Status != generated.Cancelled || cancelled.CancelledAt == nil || !cancelled.CancelledAt.Equal(fixedNow) {
		t.Fatalf("cancelled = %+v", cancelled)
	}
	equal(t, "active listing after cancel", c.titles(window), nil)
	equal(t, "listing with cancelled", c.titles(window+"&includeCancelled=true"), []string{"Dentist"})
	if kept := c.event(http.MethodGet, "/v1/events/"+created.Id.String(), nil, http.StatusOK); kept.Status != generated.Cancelled {
		t.Fatalf("cancelled event is not kept as history: %+v", kept)
	}
	restored := c.event(http.MethodPatch, "/v1/events/"+created.Id.String(), `{"status":"active"}`, http.StatusOK)
	if restored.Status != generated.Active || restored.CancelledAt != nil {
		t.Fatalf("restored = %+v", restored)
	}
	equal(t, "listing after restore", c.titles(window), []string{"Dentist"})

	missing := "/v1/events/" + googleuuid.NewString()
	if status, _ := c.do(http.MethodPatch, missing, `{"title":"x"}`); status != http.StatusNotFound {
		t.Errorf("PATCH unknown: %d", status)
	}
	if status, _ := c.do(http.MethodDelete, "/v1/events/"+created.Id.String(), nil); status != http.StatusNoContent {
		t.Fatalf("DELETE: %d", status)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		if status, _ := c.do(method, "/v1/events/"+created.Id.String(), nil); status != http.StatusNotFound {
			t.Errorf("%s after delete: %d", method, status)
		}
	}

	// Idempotent creation, as the Inbox processor uses through MCP.
	body := `{"title":"Anna's birthday","allDay":true,"startDate":"2026-10-12","endDate":"2026-10-12"}`
	_, first := c.do(http.MethodPost, "/v1/events", body, "Idempotency-Key", "capture-1")
	status, again := c.do(http.MethodPost, "/v1/events", body, "Idempotency-Key", "capture-1")
	if status != http.StatusCreated || !bytes.Equal(first, again) {
		t.Fatalf("replay: %d %s, want the original %s", status, again, first)
	}
	if status, _ := c.do(http.MethodPost, "/v1/events", strings.Replace(body, "Anna", "Ben", 1), "Idempotency-Key", "capture-1"); status != http.StatusConflict {
		t.Errorf("key reuse with another body: %d, want 409", status)
	}

	// The actor gate holds in front of the real database too.
	req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	if resp, err := c.app.Test(req, fiberTestConfig); err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("write without actor: %v %v", resp, err)
	}
	equal(t, "events after the refused write", c.titles("/v1/events?"+q("from", "2026-10-12T00:00:00+02:00", "to", "2026-10-13T00:00:00+02:00")), []string{"Anna's birthday"})
}

func TestAllDayEventsMatchTheOwnersDays(t *testing.T) {
	c := newAPIClient(t)
	for _, body := range []string{
		`{"title":"Friday market","allDay":true,"startDate":"2026-10-09","endDate":"2026-10-09"}`,
		`{"title":"Saturday market","allDay":true,"startDate":"2026-10-10","endDate":"2026-10-10"}`,
		// 00:30 on Saturday in Berlin, still Friday in UTC.
		`{"title":"Night owl","allDay":false,"startAt":"2026-10-09T22:30:00Z","endAt":"2026-10-09T23:30:00Z"}`,
		// Berlin leaves summer time during the night after 25 October.
		`{"title":"Autumn weekend","allDay":true,"startDate":"2026-10-24","endDate":"2026-10-25"}`,
	} {
		c.event(http.MethodPost, "/v1/events", body, http.StatusCreated)
	}
	// The owner's Saturday, as the Dashboard asks for it: Berlin midnight to
	// midnight. The UTC date of `from` is Friday, which used to leak in.
	saturday := q("from", "2026-10-09T22:00:00Z", "to", "2026-10-10T22:00:00Z")
	equal(t, "owner's Saturday (default Berlin)", c.titles("/v1/events?"+saturday), []string{"Saturday market", "Night owl"})
	equal(t, "owner's Saturday (explicit Berlin)", c.titles("/v1/events?"+saturday+"&timeZone=Europe/Berlin"), []string{"Saturday market", "Night owl"})
	// The same instants seen from UTC cover parts of Friday and Saturday.
	equal(t, "the same range in UTC", c.titles("/v1/events?"+saturday+"&timeZone=UTC"), []string{"Friday market", "Night owl", "Saturday market"})
	// New York's Saturday starts after Friday's market there has ended.
	equal(t, "New York's Saturday", c.titles("/v1/events?"+q("from", "2026-10-10T04:00:00Z", "to", "2026-10-11T04:00:00Z", "timeZone", "America/New_York")), []string{"Saturday market"})
	// The weekend ends at Monday 00:00 CET (23:00 UTC), an hour later in UTC
	// than it would in summer time.
	equal(t, "Sunday 23:30 CET", c.titles("/v1/events?"+q("from", "2026-10-25T22:30:00Z", "to", "2026-10-25T22:45:00Z")), []string{"Autumn weekend"})
	equal(t, "Monday 00:00 CET", c.titles("/v1/events?"+q("from", "2026-10-25T23:00:00Z", "to", "2026-10-26T23:00:00Z")), nil)
	// It starts at Saturday 00:00 CEST (22:00 UTC on Friday).
	equal(t, "Friday 23:30 CEST", c.titles("/v1/events?"+q("from", "2026-10-23T21:30:00Z", "to", "2026-10-23T22:00:00Z")), nil)
	equal(t, "Saturday 00:00 CEST", c.titles("/v1/events?"+q("from", "2026-10-23T22:00:00Z", "to", "2026-10-23T22:01:00Z")), []string{"Autumn weekend"})
}

func TestListPagesWithACursor(t *testing.T) {
	c := newAPIClient(t)
	for _, body := range []string{
		`{"title":"E","allDay":false,"startAt":"2026-11-05T09:00:00Z","endAt":"2026-11-05T10:00:00Z"}`,
		`{"title":"A","allDay":true,"startDate":"2026-11-01","endDate":"2026-11-03"}`,
		`{"title":"C","allDay":false,"startAt":"2026-11-02T08:00:00Z","endAt":"2026-11-02T09:00:00Z"}`,
		`{"title":"B","allDay":false,"startAt":"2026-11-01T08:00:00Z","endAt":"2026-11-01T09:00:00Z"}`,
		`{"title":"D","allDay":true,"startDate":"2026-11-04","endDate":"2026-11-04"}`,
	} {
		c.event(http.MethodPost, "/v1/events", body, http.StatusCreated)
	}
	window := "/v1/events?" + q("from", "2026-10-31T23:00:00Z", "to", "2026-11-30T23:00:00Z", "limit", "2")
	first := c.list(window)
	if len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatalf("first page: %+v", first)
	}
	equal(t, "all pages", c.titles(window), []string{"A", "B", "C", "D", "E"})
	equal(t, "one page", c.titles(strings.Replace(window, "limit=2", "limit=100", 1)), []string{"A", "B", "C", "D", "E"})
	searchCursor := encodeSearchCursor(searchPosition{Anchor: fixedNow, ID: googleuuid.New()})
	if status, _ := c.do(http.MethodGet, window+"&cursor="+url.QueryEscape(searchCursor), nil); status != http.StatusBadRequest {
		t.Errorf("a search cursor on list: %d, want 400", status)
	}
}

func TestSearchFindsEventsAcrossMonths(t *testing.T) {
	c := newAPIClient(t)
	zaw, other := c.newSource("zaw"), googleuuid.New()
	description := func(params store.CreateEventParams, text string) store.CreateEventParams {
		params.Description = &text
		return params
	}
	location := func(params store.CreateEventParams, text string) store.CreateEventParams {
		params.Location = &text
		return params
	}
	c.seed(description(timedParams("Dentist checkup", "2026-03-05T08:00:00Z", 60), "Dr. Weber"))
	c.seed(timedParams("Dentist follow-up", "2026-12-01T08:00:00Z", 60))
	c.seed(location(timedParams("Lunch", "2026-11-01T11:00:00Z", 60), "Next to the dentist"))
	c.seed(description(timedParams("Team meeting", "2027-02-01T09:00:00Z", 60), "discuss dentist insurance"))
	c.seed(store.CreateEventParams{Title: "Summer holiday", AllDay: true, StartDate: pgtype.Date{Time: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Valid: true}, EndDate: pgtype.Date{Time: time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC), Valid: true}, Description: ptr("100% offline")})
	c.seed(importedParams(zaw, "zaw-papier-1", "Papier", "2026-10-15T04:00:00Z"))
	c.seed(importedParams(zaw, "zaw-papier-2", "Papier", "2026-11-12T05:00:00Z"))
	c.seed(importedParams(zaw, "zaw-papier-0", "Papier", "2025-12-20T05:00:00Z"))
	c.seed(importedParams(zaw, "zaw-rest-1", "Restmüll 14-täglich", "2026-10-21T04:00:00Z"))
	gone := importedParams(zaw, "zaw-papier-x", "Papier", "2026-09-01T04:00:00Z")
	gone.Status, gone.CancelledAt, gone.RemovedFromSourceAt = "cancelled", pgtype.Timestamptz{Time: fixedNow, Valid: true}, pgtype.Timestamptz{Time: fixedNow, Valid: true}
	c.seed(gone)

	search := func(values ...string) []string { return c.titles("/v1/events/search?" + q(values...)) }
	// Title matches outrank location matches, which outrank notes; among
	// equals, upcoming events come first, then the most recent past ones.
	equal(t, "dentist", search("q", "dentist"), []string{"Dentist follow-up", "Dentist checkup", "Lunch", "Team meeting"})
	equal(t, "every word must match", search("q", "dentist weber"), []string{"Dentist checkup"})
	equal(t, "case-insensitive", search("q", "DENTIST FOLLOW"), []string{"Dentist follow-up"})
	equal(t, "inside a compound word", search("q", "müll"), []string{"Restmüll 14-täglich"})
	equal(t, "wildcards are literal", search("q", "%"), []string{"Summer holiday"})
	equal(t, "nothing", search("q", "zahnarzt"), nil)

	papier := c.list("/v1/events/search?" + q("q", "papier"))
	if len(papier.Items) != 3 || papier.Items[0].Source.Kind != generated.EventSourceKindExternal || *papier.Items[0].Source.ExternalUid != "zaw-papier-1" || *papier.Items[0].Source.SourceId != zaw {
		t.Fatalf("papier: %+v", papier.Items)
	}
	// Upcoming soonest first, then past most recent first; the event its
	// source removed is history, shown only on request.
	equal(t, "papier", search("q", "papier"), []string{"Papier", "Papier", "Papier"})
	starts := func(list generated.EventList) []string {
		var out []string
		for _, item := range list.Items {
			out = append(out, item.StartAt.Format(time.DateOnly)+" "+string(item.Status))
		}
		return out
	}
	equal(t, "papier order", starts(papier), []string{"2026-10-15 active", "2026-11-12 active", "2025-12-20 active"})
	withCancelled := c.list("/v1/events/search?" + q("q", "papier", "includeCancelled", "true"))
	equal(t, "papier with cancelled", starts(withCancelled), []string{"2026-10-15 active", "2026-11-12 active", "2026-09-01 cancelled", "2025-12-20 active"})
	if removed := withCancelled.Items[2]; removed.RemovedFromSourceAt == nil || !removed.RemovedFromSourceAt.Equal(fixedNow) {
		t.Errorf("removed event: %+v", removed)
	}

	// Filters: source and date range.
	equal(t, "manual only", search("q", "papier", "sourceKind", "manual"), nil)
	equal(t, "from this feed", search("q", "papier", "sourceKind", "external", "sourceId", zaw.String()), []string{"Papier", "Papier", "Papier"})
	equal(t, "from another feed", search("q", "papier", "sourceId", other.String()), nil)
	equal(t, "manual dentist", search("q", "dentist", "sourceKind", "manual"), []string{"Dentist follow-up", "Dentist checkup", "Lunch", "Team meeting"})
	equal(t, "papier from November", starts(c.list("/v1/events/search?"+q("q", "papier", "from", "2026-11-01T00:00:00Z"))), []string{"2026-11-12 active"})
	equal(t, "papier before 2026", starts(c.list("/v1/events/search?"+q("q", "papier", "to", "2026-01-01T00:00:00Z"))), []string{"2025-12-20 active"})
	equal(t, "dentist in autumn", search("q", "dentist", "from", "2026-10-01T00:00:00Z", "to", "2026-12-31T00:00:00Z"), []string{"Dentist follow-up", "Lunch"})

	// Paging one result at a time returns the same order without gaps or
	// repeats, even when "now" moves on between pages: the cursor keeps the
	// first page's split between upcoming and past.
	target := "/v1/events/search?" + q("q", "papier", "includeCancelled", "true", "limit", "1")
	var paged []string
	next := target
	for page := 0; page < 10; page++ {
		result := c.list(next)
		paged = append(paged, starts(result)...)
		if page == 0 {
			c.handler.now = func() time.Time { return time.Date(2026, time.November, 20, 0, 0, 0, 0, time.UTC) }
		}
		if result.NextCursor == nil {
			break
		}
		next = target + "&cursor=" + url.QueryEscape(*result.NextCursor)
	}
	equal(t, "papier page by page", paged, []string{"2026-10-15 active", "2026-11-12 active", "2026-09-01 cancelled", "2025-12-20 active"})
}

func TestImportedEventsSitAlongsideUserEvents(t *testing.T) {
	c := newAPIClient(t)
	feed := c.newSource("zaw")
	imported := c.seed(importedParams(feed, "zaw-bio-17", "Bioabfall", "2026-11-17T05:00:00Z"))
	gone := importedParams(feed, "zaw-bio-03", "Bioabfall", "2026-11-03T05:00:00Z")
	gone.Status, gone.CancelledAt, gone.RemovedFromSourceAt = "cancelled", pgtype.Timestamptz{Time: fixedNow, Valid: true}, pgtype.Timestamptz{Time: fixedNow, Valid: true}
	removed := c.seed(gone)
	// A client cannot create an imported event: the source is ignored.
	manual := c.event(http.MethodPost, "/v1/events", `{"title":"Bring bins out","allDay":false,"startAt":"2026-11-16T18:00:00Z","endAt":"2026-11-16T18:15:00Z","source":{"kind":"external","sourceId":"`+feed.String()+`","externalUid":"forged"}}`, http.StatusCreated)
	if manual.Source.Kind != generated.EventSourceKindManual || manual.Source.SourceId != nil || manual.Source.ExternalUid != nil {
		t.Fatalf("manual event source = %+v", manual.Source)
	}

	window := "/v1/events?" + q("from", "2026-11-01T00:00:00+01:00", "to", "2026-12-01T00:00:00+01:00")
	all := c.list(window)
	if len(all.Items) != 2 || all.Items[0].Title != "Bring bins out" || all.Items[1].Id != uuid(imported.ID) {
		t.Fatalf("listing: %+v", all.Items)
	}
	source := all.Items[1].Source
	if source.Kind != generated.EventSourceKindExternal || *source.SourceId != feed || *source.ExternalUid != "zaw-bio-17" || len(all.Items[1].Reminders) != 0 || !all.Items[1].UseDefaultReminders {
		t.Fatalf("imported event: %+v", all.Items[1])
	}
	equal(t, "manual filter", c.titles(window+"&sourceKind=manual"), []string{"Bring bins out"})
	equal(t, "external filter", c.titles(window+"&sourceKind=external"), []string{"Bioabfall"})
	// The legacy kinds are gone from the contract.
	for _, kind := range []string{"ics", "google"} {
		if status, raw := c.do(http.MethodGet, window+"&sourceKind="+kind, nil); status != http.StatusBadRequest || !strings.Contains(string(raw), "sourceKind must be manual or external") {
			t.Errorf("sourceKind=%s: %d %s", kind, status, raw)
		}
	}
	equal(t, "this feed", c.titles(window+"&sourceId="+feed.String()), []string{"Bioabfall"})
	equal(t, "with history", c.titles(window+"&includeCancelled=true"), []string{"Bioabfall", "Bring bins out", "Bioabfall"})
	if history := c.event(http.MethodGet, "/v1/events/"+uuid(removed.ID).String(), nil, http.StatusOK); history.Status != generated.Cancelled || history.RemovedFromSourceAt == nil {
		t.Fatalf("removed event: %+v", history)
	}

	// The owner can override an imported event's reminders (the source's
	// default is "the evening before"); its source identity stays put.
	override := c.event(http.MethodPatch, "/v1/events/"+uuid(imported.ID).String(), `{"reminders":[{"kind":"localTime","daysBefore":1,"localTime":"20:30"}],"source":{"kind":"manual"}}`, http.StatusOK)
	if override.Source.Kind != generated.EventSourceKindExternal || *override.Source.ExternalUid != "zaw-bio-17" || len(override.Reminders) != 1 || *override.Reminders[0].LocalTime != "20:30" {
		t.Fatalf("override: %+v", override)
	}
}
