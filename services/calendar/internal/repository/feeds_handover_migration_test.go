package repository_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/services/calendar/internal/testdb"
)

// The calendar feeds handover migration (P11-08) hands the feeds Calendar
// refreshed itself to calendar-feeds: their sources and events become kind
// external, out of process, with their identity, legacy feed columns, and
// events otherwise unchanged and nothing written to the change feed (the
// feeds are read-only). Google sources, a feed calendar-feeds registered
// itself, and Life OS events are left alone. A later migration of the same
// run can still alter events (the journal triggers fire at once), and the
// migration reverses.
func TestCalendarFeedsHandoverMigration(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t, "000013")
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
	googleSource := scalar(`INSERT INTO calendar_sources (kind, name, url, google_calendar_id) VALUES ('google', 'Owner', 'https://www.googleapis.com/calendar/v3/calendars/owner/events', $1) RETURNING id::text`, ownerCalendar)
	zaw := scalar(`INSERT INTO calendar_sources (kind, name, url, filter_include, preset, preset_config, etag, last_outcome, next_refresh_at)
		VALUES ('ics', 'ZAW', 'https://example.com/zaw.ics', '{Papier}', 'zaw', '{"name":"ZAW"}', '"v1"', 'imported', '2026-10-11T02:30:00Z') RETURNING id::text`)
	club := scalar(`INSERT INTO calendar_sources (kind, name, url, enabled) VALUES ('ics', 'Club', 'https://example.org/club.ics', false) RETURNING id::text`)
	scalar(`INSERT INTO events (title, all_day, start_date, end_date, source_kind, source_id, external_uid) VALUES ('Papier', true, '2026-10-14', '2026-10-14', 'ics', $1, 'papier@zaw') RETURNING id::text`, zaw)
	scalar(`INSERT INTO events (title, all_day, start_at, end_at, source_kind, source_id, external_uid, status, cancelled_at, removed_from_source_at)
		VALUES ('Training', false, '2026-10-15T17:00:00Z', '2026-10-15T18:00:00Z', 'ics', $1, 'training#20261015T170000Z', 'cancelled', now(), now()) RETURNING id::text`, club)
	scalar(`INSERT INTO events (title, all_day, start_at, end_at, source_kind, source_id, external_uid) VALUES ('Standup', false, '2026-10-12T07:00:00Z', '2026-10-12T07:15:00Z', 'google', $1, 'standup') RETURNING id::text`, googleSource)
	scalar(`INSERT INTO events (title, all_day, start_at, end_at) VALUES ('Haircut', false, '2026-10-18T09:00:00Z', '2026-10-18T10:00:00Z') RETURNING id::text`)
	testdb.Apply(t, pool, migrationPath(t, "000014_integration_api.up.sql"))
	registered := scalar(`INSERT INTO calendar_sources (kind, name, integration, external_id, provider, in_process) VALUES ('external', 'Holidays', 'calendar-feeds', 'holidays-feed', 'ICS', false) RETURNING id::text`)

	sources := `SELECT string_agg(concat_ws(' ', name, kind, in_process, integration, external_id = id::text, provider, url, filter_include::text, preset, etag, last_outcome, enabled), '|' ORDER BY created_at, id) FROM calendar_sources`
	events := `SELECT string_agg(concat_ws(' ', title, source_kind, source_id, external_uid, status, removed_from_source_at IS NOT NULL, updated_at), '|' ORDER BY title) FROM events`
	beforeSources, beforeEvents := scalar(sources), scalar(events)

	up := migrationPath(t, "_calendar_feeds_handover.up.sql")
	raw, err := os.ReadFile(up)
	if err != nil {
		t.Fatal(err)
	}
	// One transaction with a later migration that alters events, as
	// golang-migrate may run them.
	if _, err := pool.Exec(ctx, string(raw)+"\nALTER TABLE events ADD COLUMN handover_probe INTEGER; ALTER TABLE events DROP COLUMN handover_probe;"); err != nil {
		t.Fatalf("handover with a later ALTER TABLE: %v", err)
	}
	afterSources, afterEvents := scalar(sources), scalar(events)
	want := strings.NewReplacer(
		"ZAW ics t calendar-feeds", "ZAW external f calendar-feeds",
		"Club ics t calendar-feeds", "Club external f calendar-feeds",
	).Replace(beforeSources)
	if afterSources != want {
		t.Fatalf("sources after the handover:\n%s\nwant\n%s", afterSources, want)
	}
	wantEvents := strings.NewReplacer(" ics "+zaw, " external "+zaw, " ics "+club, " external "+club).Replace(beforeEvents)
	if afterEvents != wantEvents {
		t.Fatalf("events after the handover:\n%s\nwant\n%s", afterEvents, wantEvents)
	}
	if entries := scalar(`SELECT count(*)::text FROM calendar_event_changes`); entries != "0" {
		t.Fatalf("the handover wrote %s change feed entries", entries)
	}
	if kind := scalar(`SELECT kind FROM calendar_sources WHERE id = $1`, registered); kind != "external" {
		t.Fatalf("the registered feed = %s", kind)
	}

	// Down gives the feeds back to the in-process refresher; the feed
	// calendar-feeds registered itself (no URL in Calendar) stays.
	testdb.Apply(t, pool, strings.Replace(up, ".up.sql", ".down.sql", 1))
	if back := scalar(sources); back != beforeSources {
		t.Fatalf("sources after down:\n%s\nwant\n%s", back, beforeSources)
	}
	if back := scalar(events); back != beforeEvents {
		t.Fatalf("events after down:\n%s\nwant\n%s", back, beforeEvents)
	}
	testdb.Apply(t, pool, up)
	if again := scalar(sources); again != afterSources {
		t.Fatalf("sources after up again:\n%s", again)
	}
}

func migrationPath(t *testing.T, suffix string) string {
	t.Helper()
	for _, path := range testdb.Migrations(t) {
		if strings.HasSuffix(filepath.Base(path), suffix) {
			return path
		}
	}
	t.Fatalf("no migration *%s", suffix)
	return ""
}
