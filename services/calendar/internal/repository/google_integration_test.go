package repository

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/services/calendar/internal/testdb"
)

// The Google connection tables keep at most one connection, cascade its
// calendar list, and go away cleanly.
func TestGoogleConnectionMigration(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t, "000009")
	insert := `INSERT INTO google_connections (account_email, refresh_token_ciphertext, refresh_token_nonce) VALUES ($1, '\x01', '\x02') RETURNING id`
	var connection string
	if err := pool.QueryRow(ctx, insert, "owner@example.com").Scan(&connection); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insert, "other@example.com"); err == nil || !strings.Contains(err.Error(), "singleton") {
		t.Fatalf("a second connection: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO google_calendars (connection_id, google_calendar_id, summary, access_role) VALUES ($1, 'owner@example.com', 'Owner', 'owner')`, connection); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO google_calendars (connection_id, google_calendar_id, summary, access_role) VALUES ($1, 'x', 'X', 'admin')`, connection); err == nil {
		t.Fatal("an unknown access role was accepted")
	}
	if _, err := pool.Exec(ctx, `UPDATE google_connections SET status = 'revoked'`); err == nil {
		t.Fatal("an unknown status was accepted")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM google_connections`); err != nil {
		t.Fatal(err)
	}
	var calendars int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM google_calendars`).Scan(&calendars); err != nil || calendars != 0 {
		t.Fatalf("calendars after disconnect = %d, %v", calendars, err)
	}

	var migration string
	for _, path := range testdb.Migrations(t) {
		if filepath.Base(path) == "000009_google_connection.up.sql" {
			migration = path
		}
	}
	if migration == "" {
		t.Fatal("000009_google_connection.up.sql not found")
	}
	testdb.Apply(t, pool, strings.Replace(migration, ".up.sql", ".down.sql", 1))
	var tables int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name LIKE 'google_%'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("google tables after down = %d, %v", tables, err)
	}
	testdb.Apply(t, pool, migration)
}

// Synced Google calendars are sources of kind google tied to their
// calendar: deleting the calendar deletes its source and events. Going down
// removes the synced events and keeps feeds and Life OS events.
func TestGoogleSyncMigration(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t, "")
	var connection, calendar, other string
	if err := pool.QueryRow(ctx, `INSERT INTO google_connections (account_email, refresh_token_ciphertext, refresh_token_nonce) VALUES ('owner@example.com', '\x01', '\x02') RETURNING id`).Scan(&connection); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id  string
		out *string
	}{{"owner@example.com", &calendar}, {"family@group.calendar.google.com", &other}} {
		if err := pool.QueryRow(ctx, `INSERT INTO google_calendars (connection_id, google_calendar_id, summary, access_role, sync_enabled) VALUES ($1, $2, 'Cal', 'owner', true) RETURNING id`, connection, row.id).Scan(row.out); err != nil {
			t.Fatal(err)
		}
	}
	var source, feed string
	if err := pool.QueryRow(ctx, `INSERT INTO calendar_sources (kind, name, url, google_calendar_id) VALUES ('google', 'Owner', 'https://www.googleapis.com/calendar/v3/calendars/owner/events', $1) RETURNING id`, calendar).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO calendar_sources (kind, name, url) VALUES ('ics', 'ZAW', 'https://example.com/feed.ics') RETURNING id`).Scan(&feed); err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"a google source without its calendar": `INSERT INTO calendar_sources (kind, name, url) VALUES ('google', 'X', 'https://x')`,
		"a feed tied to a calendar":            `INSERT INTO calendar_sources (kind, name, url, google_calendar_id) VALUES ('ics', 'X', 'https://x', '` + other + `')`,
		"a second source for a calendar":       `INSERT INTO calendar_sources (kind, name, url, google_calendar_id) VALUES ('google', 'X', 'https://x', '` + calendar + `')`,
		"source reminders on a manual event":   `INSERT INTO events (title, all_day, start_date, end_date, source_reminders) VALUES ('X', true, '2026-10-12', '2026-10-12', '{}')`,
	} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	insertEvent := `INSERT INTO events (title, all_day, start_date, end_date, source_kind, source_id, external_uid, source_reminders) VALUES ($1, true, '2026-10-12', '2026-10-12', $2, $3, $4, $5)`
	if _, err := pool.Exec(ctx, insertEvent, "Synced", "google", source, "abc", `{"useDefault":true,"reminders":[]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insertEvent, "Pickup", "ics", feed, "zaw-1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO events (title, all_day, start_date, end_date) VALUES ('Mine', true, '2026-10-12', '2026-10-12')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO google_events (calendar_id, event_id, data) VALUES ($1, 'abc', '{"id":"abc"}')`, calendar); err != nil {
		t.Fatal(err)
	}
	count := func(query string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Deleting the calendar (unticked, disconnected, or unlisted) takes its
	// source, events, and stored Google events with it.
	if _, err := pool.Exec(ctx, `DELETE FROM google_calendars WHERE id = $1`, calendar); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM events`); n != 2 || count(`SELECT count(*) FROM calendar_sources`) != 1 || count(`SELECT count(*) FROM google_events`) != 0 {
		t.Fatalf("after deleting the calendar: %d events", n)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO calendar_sources (kind, name, url, google_calendar_id) VALUES ('google', 'Family', 'https://x', $1) RETURNING id`, other).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insertEvent, "Family dinner", "google", source, "def", `{"useDefault":true,"reminders":[]}`); err != nil {
		t.Fatal(err)
	}

	var migration string
	for _, path := range testdb.Migrations(t) {
		if filepath.Base(path) == "000010_google_sync.up.sql" {
			migration = path
		}
	}
	if migration == "" {
		t.Fatal("000010_google_sync.up.sql not found")
	}
	testdb.Apply(t, pool, strings.Replace(migration, ".up.sql", ".down.sql", 1))
	if n := count(`SELECT count(*) FROM events`); n != 2 || count(`SELECT count(*) FROM calendar_sources WHERE kind = 'ics'`) != 1 ||
		count(`SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND (table_name, column_name) IN (('events', 'source_reminders'), ('google_calendars', 'sync_token'), ('calendar_sources', 'google_calendar_id'))`) != 0 {
		t.Fatalf("after down: %d events", n)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO calendar_sources (kind, name, url) VALUES ('google', 'X', 'https://x')`); err == nil {
		t.Fatal("a google source was accepted after down")
	}
	testdb.Apply(t, pool, migration)
	if n := count(`SELECT count(*) FROM google_calendars WHERE next_sync_at IS NOT NULL`); n != 1 {
		t.Fatalf("calendars due after up: %d", n)
	}
}
