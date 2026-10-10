package repository_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/testdb"
)

// The integration API migration (000014) gives every existing source a
// stable identity in its integration, on data shaped like the live data: the
// owner's Google calendar and a shared read-only one, each synced with
// events (a series with a skipped occurrence), the ZAW preset feed the owner
// renamed, another feed, and a Life OS event. It changes no event, starts
// an empty change feed, and reverses cleanly.
func TestIntegrationAPIMigration(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t, "000013")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	scalar := func(query string, args ...any) string {
		t.Helper()
		var out string
		if err := pool.QueryRow(ctx, query, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return out
	}
	connection := scalar(`INSERT INTO google_connections (account_email, refresh_token_ciphertext, refresh_token_nonce) VALUES ('owner@example.com', '\x01', '\x02') RETURNING id::text`)
	ownerCalendar := scalar(`INSERT INTO google_calendars (connection_id, google_calendar_id, summary, access_role, is_primary, sync_enabled) VALUES ($1, 'owner@example.com', 'Owner', 'owner', true, true) RETURNING id::text`, connection)
	familyCalendar := scalar(`INSERT INTO google_calendars (connection_id, google_calendar_id, summary, access_role, sync_enabled) VALUES ($1, 'family@group.calendar.google.com', 'Family', 'reader', true) RETURNING id::text`, connection)
	ownerSource := scalar(`INSERT INTO calendar_sources (kind, name, url, default_reminders, google_calendar_id) VALUES ('google', 'Owner', 'https://www.googleapis.com/calendar/v3/calendars/owner/events', '[{"kind":"relative","minutesBefore":10}]', $1) RETURNING id::text`, ownerCalendar)
	familySource := scalar(`INSERT INTO calendar_sources (kind, name, url, google_calendar_id) VALUES ('google', 'Family', 'https://www.googleapis.com/calendar/v3/calendars/family/events', $1) RETURNING id::text`, familyCalendar)
	zawReminders := `[{"kind":"localTime","daysBefore":1,"localTime":"19:00"}]`
	zawSource := scalar(`INSERT INTO calendar_sources (kind, name, url, filter_include, default_reminders, default_priority, preset, preset_config, next_refresh_at)
		VALUES ('ics', 'Müll', 'https://example.com/zaw.ics', '{Papier,Bioabfall}', $1, 'high', 'zaw', $2, now()) RETURNING id::text`,
		zawReminders, `{"name":"ZAW","url":"https://example.com/zaw.ics","include":["Papier","Bioabfall"],"exclude":[],"defaultReminders":`+zawReminders+`}`)
	clubSource := scalar(`INSERT INTO calendar_sources (kind, name, url) VALUES ('ics', 'Club', 'https://example.org/club.ics') RETURNING id::text`)
	standup := scalar(`INSERT INTO events (title, all_day, start_at, end_at, time_zone, recurrence_frequency, recurrence_interval, source_kind, source_id, external_uid, source_reminders)
		VALUES ('Standup', false, '2026-10-12T07:00:00Z', '2026-10-12T07:15:00Z', 'Europe/Berlin', 'weekly', 1, 'google', $1, 'standup', '{"useDefault":true,"reminders":[]}') RETURNING id::text`, ownerSource)
	exec(`INSERT INTO event_exceptions (event_id, recurrence_id, cancelled_at) VALUES ($1, '2026-10-19T07:00:00Z', now())`, standup)
	exec(`INSERT INTO events (title, all_day, start_date, end_date, source_kind, source_id, external_uid) VALUES ('Trip', true, '2026-10-20', '2026-10-22', 'google', $1, 'trip')`, familySource)
	exec(`INSERT INTO events (title, all_day, start_date, end_date, source_kind, source_id, external_uid) VALUES ('Papier', true, '2026-10-14', '2026-10-14', 'ics', $1, 'papier@zaw')`, zawSource)
	exec(`INSERT INTO events (title, all_day, start_at, end_at) VALUES ('Haircut', false, '2026-10-18T09:00:00Z', '2026-10-18T10:00:00Z')`)
	eventsNow := `SELECT string_agg(concat_ws(' ', e.id, e.title, e.source_kind, e.source_id, e.external_uid, e.status, e.start_at, e.start_date), '|' ORDER BY e.id) FROM events AS e`
	before := scalar(eventsNow)

	var migration string
	for _, path := range testdb.Migrations(t) {
		if filepath.Base(path) == "000014_integration_api.up.sql" {
			migration = path
		}
	}
	if migration == "" {
		t.Fatal("000014_integration_api.up.sql not found")
	}
	testdb.Apply(t, pool, migration)

	identity := func(source string) string {
		t.Helper()
		return scalar(`SELECT concat_ws(' ', kind, integration, external_id, provider, read_only::text, in_process::text) FROM calendar_sources WHERE id = $1`, source)
	}
	for source, want := range map[string]string{
		ownerSource:  "google google-calendar owner@example.com Google false true",
		familySource: "google google-calendar family@group.calendar.google.com Google true true",
		zawSource:    "ics calendar-feeds " + zawSource + " ZAW true true",
		clubSource:   "ics calendar-feeds " + clubSource + " ICS true true",
	} {
		if got := identity(source); got != want {
			t.Errorf("source identity = %q, want %q", got, want)
		}
	}
	// What the integration last set: the ZAW preset's environment values
	// (so the owner's rename survives), else today's values.
	config := func(source string) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal([]byte(scalar(`SELECT integration_config::text FROM calendar_sources WHERE id = $1`, source)), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if zaw := config(zawSource); zaw["name"] != "ZAW" || zaw["defaultPriority"] != "high" || zaw["defaultReminders"] == nil {
		t.Errorf("ZAW config = %v", zaw)
	}
	if club := config(clubSource); club["name"] != "Club" || club["defaultPriority"] != "medium" || club["defaultReminders"] != nil {
		t.Errorf("club config = %v", club)
	}
	if owner := config(ownerSource); owner["name"] != "Owner" || owner["defaultReminders"] == nil {
		t.Errorf("owner config = %v", owner)
	}
	if after := scalar(eventsNow); after != before ||
		scalar(`SELECT count(*)::text FROM event_exceptions`) != "1" || scalar(`SELECT count(*)::text FROM calendar_event_changes`) != "0" ||
		scalar(`SELECT pruned_through::text FROM calendar_change_feed`) != "0" {
		t.Fatalf("events or the feed changed: %s", after)
	}
	queries := store.New(pool)
	found, err := queries.GetIntegrationSource(ctx, store.GetIntegrationSourceParams{Integration: "google-calendar", ExternalID: "owner@example.com"})
	if err != nil || found.Name != "Owner" || !found.InProcess || found.Url == nil {
		t.Fatalf("owner source = %+v, %v", found, err)
	}

	for name, statement := range map[string]string{
		"a second source with the same identity": `INSERT INTO calendar_sources (kind, name, url, integration, external_id) VALUES ('ics', 'X', 'https://x', 'calendar-feeds', '` + clubSource + `')`,
		"an external source Calendar syncs":      `INSERT INTO calendar_sources (kind, name, integration, external_id) VALUES ('external', 'X', 'x', 'x')`,
		"an ics source an integration syncs":     `INSERT INTO calendar_sources (kind, name, url, in_process, integration, external_id) VALUES ('ics', 'X', 'https://x', false, 'x', 'y')`,
		"an external source without identity":    `INSERT INTO calendar_sources (kind, name, in_process) VALUES ('external', 'X', false)`,
		"a feed without a URL":                   `INSERT INTO calendar_sources (kind, name) VALUES ('ics', 'X')`,
		"an empty URL":                           `INSERT INTO calendar_sources (kind, name, url, in_process, integration, external_id) VALUES ('external', 'X', '', false, 'x', 'z')`,
		"a read-only reason on a feed event":     `UPDATE events SET read_only_reason = 'no' WHERE title = 'Papier'`,
	} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}

	// From now on every change of a Life OS event is journaled, by
	// system:calendar when the change names no actor; imported events of
	// read-only sources are not followed.
	exec(`UPDATE events SET title = 'Haircut at Mia''s' WHERE title = 'Haircut'`)
	exec(`UPDATE events SET title = 'Trip to Hamburg' WHERE title = 'Trip'`)
	if got := scalar(`SELECT string_agg(actor, ',') FROM calendar_event_changes`); got != "system:calendar" {
		t.Fatalf("journal after two changes = %q", got)
	}
	// An integration's source, with an event: rolling back deletes it.
	external := scalar(`INSERT INTO calendar_sources (kind, name, in_process, read_only, integration, external_id, provider) VALUES ('external', 'Work', false, false, 'outlook', 'work', 'Outlook') RETURNING id::text`)
	exec(`INSERT INTO events (title, all_day, start_date, end_date, source_kind, source_id, external_uid) VALUES ('Offsite', true, '2026-11-02', '2026-11-03', 'external', $1, 'offsite')`, external)

	testdb.Apply(t, pool, strings.Replace(migration, ".up.sql", ".down.sql", 1))
	if columns := scalar(`SELECT count(*)::text FROM information_schema.columns WHERE table_schema = current_schema()
		AND (table_name, column_name) IN (('calendar_sources', 'integration'), ('calendar_sources', 'in_process'), ('events', 'read_only_reason'))`); columns != "0" {
		t.Fatalf("down left %s columns", columns)
	}
	if tables := scalar(`SELECT count(*)::text FROM information_schema.tables WHERE table_schema = current_schema() AND table_name IN ('calendar_event_changes', 'calendar_change_feed')`); tables != "0" {
		t.Fatalf("down left %s tables", tables)
	}
	if got := scalar(`SELECT count(*)::text FROM calendar_sources`) + "/" + scalar(`SELECT count(*)::text FROM events`); got != "4/4" {
		t.Fatalf("after down: sources/events = %s", got)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO calendar_sources (kind, name) VALUES ('ics', 'X')`); err == nil {
		t.Fatal("a source without a URL was accepted after down")
	}
	testdb.Apply(t, pool, migration)
	if got := identity(ownerSource); got != "google google-calendar owner@example.com Google false true" {
		t.Fatalf("up again: %q", got)
	}
}
