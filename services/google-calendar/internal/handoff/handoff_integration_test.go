package handoff_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/services/google-calendar/internal/google"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/handoff"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/testsupport"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testsupport.Cleanup()
	os.Exit(code)
}

func gtimed(dateTime string) *google.EventTime {
	return &google.EventTime{DateTime: dateTime, TimeZone: "Europe/Berlin"}
}

// legacy is what Calendar's in-process sync left in calendar_db, as the
// test seeded it.
type legacy struct {
	refreshToken string
	calendarID   string // the owner calendar's id in Calendar's table
	lifeOS       string // the Life OS calendar's Google id
	dentist      string // Calendar event ids
	trip         string
	haircut      string
	copyETag     string
}

// The handoff copies the connection Calendar kept into google_calendar_db,
// reading calendar_db only, and the service then carries on from it: the
// owner does not reconnect, no event is duplicated in Calendar or created
// again in Google, and the owner's reminders and priority survive.
func TestHandoffCarriesOnWithoutReconnectingOrDuplicating(t *testing.T) {
	var seeded legacy
	var counts, again handoff.Counts
	h := testsupport.NewWith(t, testsupport.Options{
		UpTo: "000014",
		Prepare: func(h *testsupport.Harness) {
			seeded = seedLegacy(t, h)
		},
		Before: func(h *testsupport.Harness) {
			legacyURL := schemaURL(t, h.DatabaseURL, h.CalendarPool)
			legacyPool, err := handoff.OpenLegacy(context.Background(), legacyURL)
			if err != nil {
				t.Fatal(err)
			}
			defer legacyPool.Close()
			// Calendar's database is only read.
			if _, err := legacyPool.Exec(context.Background(), `UPDATE google_connections SET status = 'error'`); err == nil || !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("a write through the handoff's connection: %v", err)
			}
			if counts, err = handoff.Run(context.Background(), legacyPool, h.Pool); err != nil {
				t.Fatal(err)
			}
			if again, err = handoff.Run(context.Background(), legacyPool, h.Pool); err != nil {
				t.Fatal(err)
			}
		},
	}, testsupport.OwnerCalendar)

	want := handoff.Counts{
		Connections: handoff.Pair{Read: 1, Copied: 1}, Calendars: handoff.Pair{Read: 2, Copied: 2}, States: handoff.Pair{},
		Events: handoff.Pair{Read: 3, Copied: 3}, Exports: handoff.Pair{Read: 1, Copied: 1}, Sources: handoff.Pair{Read: 2, Copied: 2},
		SentEvents: handoff.Pair{Read: 2, Copied: 2},
	}
	if counts != want {
		t.Fatalf("counts = %+v, want %+v", counts, want)
	}
	if again.Connections.Copied+again.Calendars.Copied+again.Events.Copied+again.Exports.Copied+again.SentEvents.Copied != 0 || again.Connections.Read != 1 {
		t.Fatalf("a second handoff copied %+v", again)
	}
	for _, line := range append(counts.Lines(), again.Lines()...) {
		if strings.Contains(line, seeded.refreshToken) {
			t.Fatalf("the handoff printed a token: %s", line)
		}
	}

	// The connection works as it was: the sealed token opens with the same
	// key, and the calendars sync from their sync tokens.
	var connection struct {
		Status         string
		AccountEmail   string `json:"accountEmail"`
		LifeOsCalendar *struct {
			GoogleCalendarID string `json:"googleCalendarId"`
		} `json:"lifeOsCalendar"`
	}
	h.Call(http.MethodGet, "/v1/connection", nil, http.StatusOK, &connection)
	if connection.Status != "connected" || connection.AccountEmail != "owner@example.com" || connection.LifeOsCalendar == nil || connection.LifeOsCalendar.GoogleCalendarID != seeded.lifeOS {
		t.Fatalf("connection = %+v", connection)
	}
	exchanges, _, _ := h.Fake.Counts()
	h.Run()
	h.Clock.Advance(3 * time.Minute)
	h.Run()
	if now, refreshes, _ := h.Fake.Counts(); now != exchanges || refreshes == 0 || h.Fake.LastRefresh != seeded.refreshToken {
		t.Fatalf("exchanges %d -> %d, refreshes %d, last refresh token handed over: %v", exchanges, now, refreshes, h.Fake.LastRefresh == seeded.refreshToken)
	}
	for _, request := range h.Fake.Requests(testsupport.OwnerCalendar.ID)[1:] {
		if request.Get("syncToken") == "" || request.Has("timeMin") {
			t.Fatalf("a listing after the handoff = %v", request)
		}
	}
	var calendars struct {
		Items []struct {
			GoogleCalendarID string `json:"googleCalendarId"`
			Sync             struct {
				State    string  `json:"state"`
				SourceID *string `json:"sourceId"`
				Error    *string `json:"lastError"`
			} `json:"sync"`
		} `json:"items"`
	}
	h.Call(http.MethodGet, "/v1/calendars", nil, http.StatusOK, &calendars)
	if len(calendars.Items) != 2 {
		t.Fatalf("calendars = %+v", calendars)
	}
	for _, item := range calendars.Items {
		if item.Sync.State != "synced" || item.Sync.SourceID == nil {
			t.Fatalf("calendar after the handoff = %+v", item)
		}
	}

	// Nothing duplicated or lost in Calendar; the owner's reminders and
	// priority stay.
	var listed struct {
		Items []struct {
			ID, Title, Priority string
			Reminders           []struct{ MinutesBefore *int } `json:"reminders"`
			Source              struct {
				Kind        string  `json:"kind"`
				ExternalUID *string `json:"externalUid"`
			} `json:"source"`
		} `json:"items"`
	}
	h.Calendar.Call(http.MethodGet, "/v1/events?from=2026-10-01T00:00:00Z&to=2026-11-01T00:00:00Z&includeCancelled=true", nil, http.StatusOK, &listed)
	titles := map[string]int{}
	for _, item := range listed.Items {
		titles[item.Title]++
		switch item.Title {
		case "Dentist":
			if item.ID != seeded.dentist || item.Source.Kind != "external" || item.Priority != "high" || len(item.Reminders) != 1 || *item.Reminders[0].MinutesBefore != 45 {
				t.Fatalf("dentist after the handoff = %+v", item)
			}
		case "Haircut":
			if item.ID != seeded.haircut || item.Source.Kind != "manual" {
				t.Fatalf("haircut = %+v", item)
			}
		}
	}
	if titles["Dentist"] != 1 || titles["Haircut"] != 1 || titles["Concert"] != 1 || titles["Trip to Hamburg"] != 1 || len(listed.Items) != 4 {
		t.Fatalf("events after the handoff = %v (%d)", titles, len(listed.Items))
	}
	// The trip, which Google deleted while Calendar was off, is removed.
	for _, item := range listed.Items {
		if item.Title == "Trip to Hamburg" && item.ID != seeded.trip {
			t.Fatalf("trip = %+v", item)
		}
	}
	var trip struct {
		Status              string     `json:"status"`
		RemovedFromSourceAt *time.Time `json:"removedFromSourceAt"`
	}
	h.Calendar.Call(http.MethodGet, "/v1/events/"+seeded.trip, nil, http.StatusOK, &trip)
	if trip.Status != "cancelled" || trip.RemovedFromSourceAt == nil {
		t.Fatalf("trip after the handoff = %+v", trip)
	}

	// Nothing was written to Google: the exported copy is not created
	// again, and no Google event was changed.
	if writes := h.Fake.Writes(); len(writes) != 0 {
		t.Fatalf("writes to Google after the handoff = %+v", writes)
	}
	var exports struct {
		Items []struct{ State string } `json:"items"`
	}
	h.Call(http.MethodGet, "/v1/exports?eventId="+seeded.haircut, nil, http.StatusOK, &exports)
	if len(exports.Items) != 1 || exports.Items[0].State != "synced" {
		t.Fatalf("exports = %+v", exports)
	}

	// And it carries on: a change of the Life OS event reaches its copy.
	h.Calendar.Call(http.MethodPatch, "/v1/events/"+seeded.haircut, map[string]any{"location": "Salon Kamm"}, http.StatusOK, nil)
	h.Run()
	writes := h.Fake.Writes()
	if len(writes) != 1 || writes[0].Method != http.MethodPatch || writes[0].Calendar != seeded.lifeOS || writes[0].IfMatch != seeded.copyETag {
		t.Fatalf("writes after a change = %+v", writes)
	}
}

// schemaURL is the test database's URL with Calendar's schema as the
// search path (what HANDOFF_CALENDAR_DATABASE_URL names in production).
func schemaURL(t *testing.T, databaseURL string, calendarPool *pgxpool.Pool) string {
	t.Helper()
	var schema string
	if err := calendarPool.QueryRow(context.Background(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// seedLegacy builds what Calendar's in-process sync left (Calendar
// migrated up to 000014): the owner calendar and the Life OS calendar
// syncing, with their sync tokens, raw events, and sources; Calendar's
// events of the owner calendar (the dentist with the owner's own reminder
// and priority, and the trip); and a Life OS event exported to the Life OS
// calendar. Google then changes on its own while Calendar is off: the trip
// is deleted and a concert added.
func seedLegacy(t *testing.T, h *testsupport.Harness) legacy {
	t.Helper()
	ctx := context.Background()
	fake := h.Fake
	client := google.NewClient(testsupport.Config(), fake.Endpoints(), nil)
	verifier, _ := google.NewVerifier()
	token, err := client.Exchange(ctx, fake.IssueCode(google.Challenge(verifier)), verifier)
	if err != nil {
		t.Fatal(err)
	}
	sealer, _ := google.NewSealer(testsupport.TokenKey)
	ciphertext, nonce, err := sealer.Seal([]byte(token.RefreshToken))
	if err != nil {
		t.Fatal(err)
	}
	dentist := google.Event{
		ID: "dentist", Summary: "Dentist", Start: gtimed("2026-10-12T09:30:00+02:00"), End: gtimed("2026-10-12T10:15:00+02:00"),
		Reminders: &google.Reminders{Overrides: []google.Reminder{{Method: "popup", Minutes: 30}}},
	}
	trip := google.Event{ID: "trip", Summary: "Trip to Hamburg", Start: &google.EventTime{Date: "2026-10-20"}, End: &google.EventTime{Date: "2026-10-23"}}
	fake.SetCalendarDetails(testsupport.OwnerCalendar.ID, "Europe/Berlin", google.Reminder{Method: "popup", Minutes: 10})
	fake.PutEvent(testsupport.OwnerCalendar.ID, dentist, trip)
	lifeOS, err := client.InsertCalendar(ctx, token.AccessToken, google.LifeOSCalendarName, "Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	haircut := googleuuid.New()
	start, end := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC), time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	berlin, _ := time.LoadLocation("Europe/Berlin")
	body := google.ExportBody(haircut, google.Fields{Title: "Haircut", StartAt: start, EndAt: end, UseDefaultReminders: true}, nil, berlin, nil)
	body["id"] = google.ExportID(haircut)
	copied, err := client.InsertEvent(ctx, token.AccessToken, lifeOS.ID, body)
	if err != nil {
		t.Fatal(err)
	}
	listing := func(calendarID string) google.EventsPage {
		page, err := client.Events(ctx, token.AccessToken, calendarID, google.EventsQuery{TimeMin: testsupport.FixedNow.Add(-google.SyncLookback)})
		if err != nil || page.NextSyncToken == "" {
			t.Fatalf("listing %s: %v", calendarID, err)
		}
		return page
	}
	owner, life := listing(testsupport.OwnerCalendar.ID), listing(lifeOS.ID)

	pool := h.CalendarPool
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var connection, ownerID, lifeOSID string
	if err := pool.QueryRow(ctx, `INSERT INTO google_connections (account_email, scopes, refresh_token_ciphertext, refresh_token_nonce, export_backfilled_at, lifeos_calendar_id, lifeos_calendar_created_at)
		VALUES ('owner@example.com', $1, $2, $3, now(), $4, now()) RETURNING id`, google.Scopes, ciphertext, nonce, lifeOS.ID).Scan(&connection); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		googleID, summary, token string
		primary                  bool
		out                      *string
	}{{testsupport.OwnerCalendar.ID, "Owner", owner.NextSyncToken, true, &ownerID}, {lifeOS.ID, "Life OS", life.NextSyncToken, false, &lifeOSID}} {
		if err := pool.QueryRow(ctx, `INSERT INTO google_calendars (connection_id, google_calendar_id, summary, access_role, is_primary, sync_enabled, time_zone,
			    sync_token, sync_window_start, next_sync_at, last_synced_at, derived_at)
			VALUES ($1, $2, $3, 'owner', $4, true, 'Europe/Berlin', $5, $6, $7, $7, $7) RETURNING id`,
			connection, row.googleID, row.summary, row.primary, row.token, testsupport.FixedNow.Add(-google.SyncLookback), testsupport.FixedNow).Scan(row.out); err != nil {
			t.Fatal(err)
		}
	}
	store := func(calendarID string, events []google.Event) {
		for _, event := range events {
			raw, _ := json.Marshal(event)
			exec(`INSERT INTO google_events (calendar_id, event_id, recurring_event_id, data) VALUES ($1, $2, NULLIF($3, ''), $4)`, calendarID, event.ID, event.RecurringEventID, raw)
		}
	}
	store(ownerID, owner.Items)
	store(lifeOSID, life.Items)
	var ownerSource, lifeOSSource string
	for _, row := range []struct {
		calendar, googleID, name string
		out                      *string
	}{{ownerID, testsupport.OwnerCalendar.ID, "Owner", &ownerSource}, {lifeOSID, lifeOS.ID, "Life OS", &lifeOSSource}} {
		if err := pool.QueryRow(ctx, `INSERT INTO calendar_sources (kind, name, url, default_reminders, google_calendar_id, integration, external_id, provider, read_only, integration_config)
			VALUES ('google', $1, 'https://www.googleapis.com/calendar/v3/calendars/x/events', '[{"kind":"relative","minutesBefore":10}]', $2,
			        'google-calendar', $3, 'Google', false, jsonb_build_object('name', $1::text)) RETURNING id`, row.name, row.calendar, row.googleID).Scan(row.out); err != nil {
			t.Fatal(err)
		}
	}
	var dentistID, tripID string
	if err := pool.QueryRow(ctx, `INSERT INTO events (title, all_day, start_at, end_at, time_zone, source_kind, source_id, external_uid, reminders, use_default_reminders, source_reminders, priority)
		VALUES ('Dentist', false, '2026-10-12T07:30:00Z', '2026-10-12T08:15:00Z', 'Europe/Berlin', 'google', $1, 'dentist',
		        '[{"kind":"relative","minutesBefore":45}]', false, '{"useDefault": false, "reminders": [{"kind":"relative","minutesBefore":30}]}', 'high') RETURNING id`,
		ownerSource).Scan(&dentistID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO events (title, all_day, start_date, end_date, source_kind, source_id, external_uid, reminders, use_default_reminders, source_reminders)
		VALUES ('Trip to Hamburg', true, '2026-10-20', '2026-10-22', 'google', $1, 'trip', '[]', false, '{"useDefault": false, "reminders": []}') RETURNING id`,
		ownerSource).Scan(&tripID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO events (id, title, all_day, start_at, end_at) VALUES ($1, 'Haircut', false, $2, $3)`, haircut, start, end)
	exec(`INSERT INTO google_exports (event_id, connection_id, google_calendar_id, google_event_id, etag, state, exported_at, next_attempt_at)
		VALUES ($1, $2, $3, $4, $5, 'synced', now(), NULL)`, haircut, connection, lifeOS.ID, copied.ID, copied.ETag)

	// While Calendar is off, Google changes.
	fake.DeleteEvent(testsupport.OwnerCalendar.ID, "trip")
	fake.PutEvent(testsupport.OwnerCalendar.ID, google.Event{ID: "concert", Summary: "Concert", Start: gtimed("2026-10-24T20:00:00+02:00"), End: gtimed("2026-10-24T22:00:00+02:00")})
	fake.ResetWrites()
	return legacy{refreshToken: token.RefreshToken, calendarID: ownerID, lifeOS: lifeOS.ID, dentist: dentistID, trip: tripID, haircut: haircut.String(), copyETag: copied.ETag}
}
