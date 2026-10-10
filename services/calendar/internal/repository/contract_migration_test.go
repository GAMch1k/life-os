package repository_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/services/calendar/internal/testdb"
)

// liveShaped builds calendar_db as the live stack has it before the
// contract migration (P11-09): Calendar's former in-process data from
// before the integrations (the owner's Google connection with two synced
// calendars, one read-only, their raw events, an export link and a pending
// authorization; the ZAW preset feed with its refresh state, and another
// feed), handed over by migrations 000014 to 000016 (every source external,
// out of process, with its legacy columns still filled), plus a source the
// google-calendar integration registered afterwards. Events: a Google series
// with a skipped occurrence and the owner's reminders and priority, a
// read-only Google event, a feed event, and a Life OS event.
type liveShaped struct {
	pool                                  *pgxpool.Pool
	owner, family, zaw, club, registered  string // source ids
	standup, trip, papier, haircut, party string // event ids
}

func seedLiveShaped(t *testing.T) liveShaped {
	t.Helper()
	ctx := context.Background()
	pool := testdb.Open(t, "000013")
	s := liveShaped{pool: pool}
	scalar := func(query string, args ...any) string {
		t.Helper()
		var out string
		if err := pool.QueryRow(ctx, query, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return out
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	connection := scalar(`INSERT INTO google_connections (account_email, refresh_token_ciphertext, refresh_token_nonce, lifeos_calendar_id)
		VALUES ('owner@example.com', '\x01', '\x02', 'lifeos@group.calendar.google.com') RETURNING id::text`)
	ownerCalendar := scalar(`INSERT INTO google_calendars (connection_id, google_calendar_id, summary, access_role, is_primary, sync_enabled, sync_token)
		VALUES ($1, 'owner@example.com', 'Owner', 'owner', true, true, 'token-1') RETURNING id::text`, connection)
	familyCalendar := scalar(`INSERT INTO google_calendars (connection_id, google_calendar_id, summary, access_role, sync_enabled)
		VALUES ($1, 'family@group.calendar.google.com', 'Family', 'reader', true) RETURNING id::text`, connection)
	exec(`INSERT INTO google_events (calendar_id, event_id, data) VALUES ($1, 'standup', '{"id":"standup"}')`, ownerCalendar)
	exec(`INSERT INTO google_oauth_states (state_hash, code_verifier, expires_at) VALUES ('\x03', 'verifier', now())`)
	s.owner = scalar(`INSERT INTO calendar_sources (kind, name, url, default_reminders, google_calendar_id)
		VALUES ('google', 'Owner', 'https://www.googleapis.com/calendar/v3/calendars/owner/events', '[{"kind":"relative","minutesBefore":10}]', $1) RETURNING id::text`, ownerCalendar)
	s.family = scalar(`INSERT INTO calendar_sources (kind, name, url, google_calendar_id)
		VALUES ('google', 'Family', 'https://www.googleapis.com/calendar/v3/calendars/family/events', $1) RETURNING id::text`, familyCalendar)
	s.zaw = scalar(`INSERT INTO calendar_sources (kind, name, url, filter_include, default_reminders, default_priority, preset, preset_config,
		                                          next_refresh_at, last_attempt_at, last_success_at, last_outcome, last_counts, etag, last_modified)
		VALUES ('ics', 'Müll', 'https://example.com/zaw.ics', '{Papier}', '[{"kind":"localTime","daysBefore":1,"localTime":"19:00"}]', 'low', 'zaw',
		        '{"name":"ZAW"}', now(), now(), now(), 'imported', '{"feedEvents":3}', '"v1"', 'Fri, 09 Oct 2026 02:30:00 GMT') RETURNING id::text`)
	s.club = scalar(`INSERT INTO calendar_sources (kind, name, url, enabled, last_outcome, last_error, consecutive_failures)
		VALUES ('ics', 'Club', 'https://example.org/club.ics', false, 'failed', 'HTTP 404', 3) RETURNING id::text`)
	s.standup = scalar(`INSERT INTO events (title, all_day, start_at, end_at, time_zone, recurrence_frequency, recurrence_interval, source_kind, source_id, external_uid,
		                                  reminders, use_default_reminders, source_reminders, priority)
		VALUES ('Standup', false, '2026-10-12T07:00:00Z', '2026-10-12T07:15:00Z', 'Europe/Berlin', 'weekly', 1, 'google', $1, 'standup',
		        '[{"kind":"relative","minutesBefore":45}]', false, '{"useDefault":true,"reminders":[]}', 'high') RETURNING id::text`, s.owner)
	exec(`INSERT INTO event_exceptions (event_id, recurrence_id, cancelled_at) VALUES ($1, '2026-10-19T07:00:00Z', now())`, s.standup)
	exec(`INSERT INTO google_exports (event_id, connection_id, google_calendar_id, google_event_id, state) VALUES (gen_random_uuid(), $1, 'lifeos@group.calendar.google.com', 'x1', 'synced')`, connection)
	s.trip = scalar(`INSERT INTO events (title, all_day, start_date, end_date, source_kind, source_id, external_uid) VALUES ('Trip', true, '2026-10-20', '2026-10-22', 'google', $1, 'trip') RETURNING id::text`, s.family)
	s.papier = scalar(`INSERT INTO events (title, all_day, start_date, end_date, source_kind, source_id, external_uid) VALUES ('Papier', true, '2026-10-14', '2026-10-14', 'ics', $1, 'papier@zaw') RETURNING id::text`, s.zaw)
	s.haircut = scalar(`INSERT INTO events (title, all_day, start_at, end_at) VALUES ('Haircut', false, '2026-10-18T09:00:00Z', '2026-10-18T10:00:00Z') RETURNING id::text`)
	for _, name := range []string{"000014_integration_api", "000015_calendar_feeds_handover", "000016_google_calendar_handover"} {
		testdb.Apply(t, pool, migrationPath(t, name+".up.sql"))
	}
	s.registered = scalar(`INSERT INTO calendar_sources (kind, name, integration, external_id, provider, read_only, in_process, integration_config)
		VALUES ('external', 'Work', 'google-calendar', 'work@example.com', 'Google', false, false, '{"name":"Work"}') RETURNING id::text`)
	s.party = scalar(`INSERT INTO events (title, all_day, start_at, end_at, source_kind, source_id, external_uid, read_only_reason)
		VALUES ('Party', false, '2026-10-24T18:00:00Z', '2026-10-24T22:00:00Z', 'external', $1, 'party_20261024', 'One occurrence of a rule Calendar cannot hold.') RETURNING id::text`, s.registered)
	return s
}

func (s liveShaped) scalar(t *testing.T, query string, args ...any) string {
	t.Helper()
	var out string
	if err := s.pool.QueryRow(context.Background(), query, args...).Scan(&out); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return out
}

func (s liveShaped) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// legacyColumns are the calendar_sources columns the contract drops.
var legacyColumns = []string{
	"url", "filter_include", "filter_exclude", "next_refresh_at", "refreshing_until", "last_attempt_at", "last_success_at",
	"last_outcome", "last_error", "last_counts", "consecutive_failures", "etag", "last_modified", "preset", "preset_config",
	"google_calendar_id", "in_process",
}

// legacyLeft counts the google_* tables and the legacy source columns there
// are.
func (s liveShaped) legacyLeft(t *testing.T) string {
	t.Helper()
	tables := s.scalar(t, `SELECT count(*)::text FROM information_schema.tables WHERE table_schema = current_schema() AND table_name LIKE 'google\_%'`)
	columns := s.scalar(t, `SELECT count(*)::text FROM information_schema.columns WHERE table_schema = current_schema()
		AND table_name = 'calendar_sources' AND column_name = ANY($1)`, legacyColumns)
	return tables + " tables, " + columns + " columns"
}

// The contract migration (000017) drops what only Calendar's in-process
// adapters used, on data shaped like the live data: every source and event
// stays with its identity, the owner's settings, reminders, and priority,
// and the skip, and the preset (ZAW) source keeps its locations private;
// nothing is written to the change feed, which follows the events of
// sources that accept changes as before. The kinds ics and google
// are refused from then on. Down gives the structure back (empty), and up
// runs again.
func TestCalendarContractMigration(t *testing.T) {
	s := seedLiveShaped(t)
	sources := `SELECT string_agg(concat_ws(' ', id, kind, name, enabled, default_reminders::text, default_priority, integration, external_id, provider,
		read_only, integration_config::text, created_at, updated_at), '|' ORDER BY id) FROM calendar_sources`
	events := `SELECT string_agg(concat_ws(' ', id, title, source_kind, source_id, external_uid, status, start_at, start_date, reminders::text,
		use_default_reminders, source_reminders::text, priority, read_only_reason, updated_at), '|' ORDER BY id) FROM events`
	exceptions := `SELECT string_agg(concat_ws(' ', event_id, recurrence_id, cancelled_at IS NOT NULL), '|') FROM event_exceptions`
	beforeSources, beforeEvents, beforeExceptions := s.scalar(t, sources), s.scalar(t, events), s.scalar(t, exceptions)
	journal := `SELECT COALESCE(max(seq), 0)::text FROM calendar_event_changes`
	beforeJournal := s.scalar(t, journal)
	if left := s.legacyLeft(t); left != "5 tables, 17 columns" {
		t.Fatalf("before the contract: %s", left)
	}

	up := migrationPath(t, "000017_calendar_contract.up.sql")
	testdb.Apply(t, s.pool, up)

	if left := s.legacyLeft(t); left != "0 tables, 0 columns" {
		t.Fatalf("after the contract: %s left", left)
	}
	if after := s.scalar(t, sources); after != beforeSources {
		t.Fatalf("sources after the contract:\n%s\nwant\n%s", after, beforeSources)
	}
	if after := s.scalar(t, events); after != beforeEvents {
		t.Fatalf("events after the contract:\n%s\nwant\n%s", after, beforeEvents)
	}
	if after := s.scalar(t, exceptions); after != beforeExceptions {
		t.Fatalf("exceptions after the contract: %s, want %s", after, beforeExceptions)
	}
	if after := s.scalar(t, journal); after != beforeJournal {
		t.Fatalf("the contract wrote change feed entries: %s, was %s", after, beforeJournal)
	}
	// The ZAW preset's events keep their street out of reminders: its
	// source's locations are private now.
	if private := s.scalar(t, `SELECT COALESCE(string_agg(id::text, ','), '') FROM calendar_sources WHERE private_location`); private != s.zaw {
		t.Fatalf("private-location sources = %q, want the ZAW feed %s", private, s.zaw)
	}

	// The change feed follows a Life OS event and the events of a source
	// that accepts changes, not those of a read-only one.
	s.exec(t, `UPDATE events SET title = 'Haircut at Mia''s' WHERE id = $1`, s.haircut)
	s.exec(t, `UPDATE events SET title = 'Daily standup' WHERE id = $1`, s.standup)
	s.exec(t, `UPDATE events SET title = 'Trip to Hamburg' WHERE id = $1`, s.trip)
	s.exec(t, `UPDATE events SET title = 'Papier und Pappe' WHERE id = $1`, s.papier)
	followed := s.scalar(t, `SELECT string_agg(event_id::text, ',' ORDER BY event_id) FROM calendar_event_changes WHERE seq > $1`, beforeJournal)
	want := []string{s.haircut, s.standup}
	if want[0] > want[1] {
		want[0], want[1] = want[1], want[0]
	}
	if followed != strings.Join(want, ",") {
		t.Fatalf("followed after the contract = %s, want %v", followed, want)
	}

	var pgError *pgconn.PgError
	for name, statement := range map[string]string{
		"an ics source":                  `INSERT INTO calendar_sources (kind, name, integration, external_id, provider) VALUES ('ics', 'X', 'calendar-feeds', 'x', 'ICS')`,
		"a google source":                `INSERT INTO calendar_sources (kind, name, integration, external_id, provider) VALUES ('google', 'X', 'google-calendar', 'x', 'Google')`,
		"a source without integration":   `INSERT INTO calendar_sources (kind, name, external_id, provider) VALUES ('external', 'X', 'x', 'X')`,
		"a source without external id":   `INSERT INTO calendar_sources (kind, name, integration, provider) VALUES ('external', 'X', 'x', 'X')`,
		"a source without provider":      `INSERT INTO calendar_sources (kind, name, integration, external_id) VALUES ('external', 'X', 'x', 'x')`,
		"an ics event":                   `UPDATE events SET source_kind = 'ics' WHERE id = '` + s.papier + `'`,
		"a google event":                 `UPDATE events SET source_kind = 'google' WHERE id = '` + s.trip + `'`,
		"a second source of an identity": `INSERT INTO calendar_sources (kind, name, integration, external_id, provider) VALUES ('external', 'X', 'google-calendar', 'work@example.com', 'Google')`,
	} {
		if _, err := s.pool.Exec(context.Background(), statement); !errors.As(err, &pgError) || (pgError.Code != "23514" && pgError.Code != "23502" && pgError.Code != "23505") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An integration registers a source as before.
	s.exec(t, `INSERT INTO calendar_sources (kind, name, integration, external_id, provider, read_only) VALUES ('external', 'Holidays', 'calendar-feeds', 'holidays', 'ICS', true)`)

	// Down gives the structure back, empty; every source stays external and
	// out of process (and so not given back by 000016's or 000015's down).
	testdb.Apply(t, s.pool, strings.Replace(up, ".up.sql", ".down.sql", 1))
	if left := s.legacyLeft(t); left != "5 tables, 17 columns" {
		t.Fatalf("after down: %s", left)
	}
	if got := s.scalar(t, `SELECT string_agg(DISTINCT concat_ws(' ', kind, in_process, url IS NULL, google_calendar_id IS NULL, preset IS NULL, consecutive_failures), ',') FROM calendar_sources`); got != "external f t t t 0" {
		t.Fatalf("sources after down: %s", got)
	}
	if got := s.scalar(t, `SELECT (SELECT count(*) FROM calendar_sources)::text || '/' || (SELECT count(*) FROM google_connections)::text`); got != "6/0" {
		t.Fatalf("after down: sources/connections = %s", got)
	}
	// The in-process era's rules hold again: a new source defaults to in
	// process, so it needs a legacy kind and a URL.
	s.exec(t, `INSERT INTO calendar_sources (kind, name, url) VALUES ('ics', 'Legacy', 'https://example.org/legacy.ics')`)
	if _, err := s.pool.Exec(context.Background(), `INSERT INTO calendar_sources (kind, name) VALUES ('ics', 'No URL')`); err == nil {
		t.Fatal("an in-process feed without a URL was accepted after down")
	}
	// The chain below still runs down from here.
	for _, name := range []string{"000016_google_calendar_handover", "000015_calendar_feeds_handover"} {
		testdb.Apply(t, s.pool, strings.Replace(migrationPath(t, name+".up.sql"), ".up.sql", ".down.sql", 1))
	}
	if got := s.scalar(t, `SELECT count(*)::text FROM calendar_sources WHERE in_process`); got != "1" {
		t.Fatalf("in-process sources after the handovers' down = %s", got)
	}
	testdb.Apply(t, s.pool, migrationPath(t, "000015_calendar_feeds_handover.up.sql"))
	testdb.Apply(t, s.pool, migrationPath(t, "000016_google_calendar_handover.up.sql"))

	// Up again refuses while the legacy feed is in process; without it, it
	// runs.
	if _, err := s.pool.Exec(context.Background(), readMigration(t, up)); err == nil || !strings.Contains(err.Error(), "000017 refused") {
		t.Fatalf("up with an in-process feed: %v", err)
	}
	s.exec(t, `DELETE FROM calendar_sources WHERE name = 'Legacy'`)
	testdb.Apply(t, s.pool, up)
	if left := s.legacyLeft(t); left != "0 tables, 0 columns" {
		t.Fatalf("after up again: %s left", left)
	}
	if got := s.scalar(t, `SELECT count(*)::text FROM calendar_sources`); got != "6" {
		t.Fatalf("sources after up again = %s", got)
	}
}

// The contract migration refuses, changing nothing, while Calendar still
// holds data no integration has taken over: a source synced in process (a
// feed before 000015, a Google calendar before 000016) or of kind ics or
// google, a source without its integration identity, or an event of kind
// ics or google.
func TestCalendarContractMigrationRefusesUnhandedData(t *testing.T) {
	cases := map[string]func(t *testing.T, s liveShaped){
		"a feed still refreshed in process": func(t *testing.T, s liveShaped) {
			s.exec(t, `UPDATE calendar_sources SET kind = 'ics', in_process = true WHERE id = $1`, s.zaw)
		},
		"a Google calendar still synced in process": func(t *testing.T, s liveShaped) {
			s.exec(t, `UPDATE calendar_sources SET kind = 'google', in_process = true WHERE id = $1`, s.owner)
		},
		"a feed added before the integration API": func(t *testing.T, s liveShaped) {
			s.exec(t, `INSERT INTO calendar_sources (kind, name, url) VALUES ('ics', 'Club 2', 'https://example.org/club2.ics')`)
		},
		"an event of kind ics": func(t *testing.T, s liveShaped) {
			s.exec(t, `UPDATE events SET source_kind = 'ics' WHERE id = $1`, s.papier)
		},
		"an event of kind google": func(t *testing.T, s liveShaped) {
			s.exec(t, `UPDATE events SET source_kind = 'google' WHERE id = $1`, s.standup)
		},
		"a source without a provider label": func(t *testing.T, s liveShaped) {
			s.exec(t, `UPDATE calendar_sources SET provider = NULL WHERE id = $1`, s.registered)
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			s := seedLiveShaped(t)
			prepare(t, s)
			state := `SELECT (SELECT count(*) FROM calendar_sources)::text || ' ' || (SELECT count(*) FROM events)::text || ' ' ||
				(SELECT count(*) FROM google_calendars)::text || ' ' || (SELECT count(*) FROM google_exports)::text || ' ' ||
				(SELECT count(*) FROM calendar_sources WHERE url IS NOT NULL)::text`
			before := s.scalar(t, state)
			_, err := s.pool.Exec(context.Background(), readMigration(t, migrationPath(t, "000017_calendar_contract.up.sql")))
			var pgError *pgconn.PgError
			if !errors.As(err, &pgError) || pgError.Code != "P0001" || !strings.Contains(pgError.Message, "000017 refused, nothing changed") ||
				!strings.Contains(pgError.Hint, "000015 and 000016") {
				t.Fatalf("contract migration: %v", err)
			}
			if left := s.legacyLeft(t); left != "5 tables, 17 columns" {
				t.Fatalf("after the refusal: %s", left)
			}
			if after := s.scalar(t, state); after != before {
				t.Fatalf("after the refusal: %s, was %s", after, before)
			}
			if _, err := s.pool.Exec(context.Background(), `INSERT INTO calendar_sources (kind, name, url) VALUES ('ics', 'Still legacy', 'https://example.org/l.ics')`); err != nil {
				t.Fatalf("the legacy kinds after the refusal: %v", err)
			}
		})
	}
}

// readMigration is a migration's SQL, for running one that may fail.
func readMigration(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// The whole chain still runs down from the contract, on live-shaped data,
// and up again from nothing.
func TestMigrationChainRunsDownAndUpThroughTheContract(t *testing.T) {
	s := seedLiveShaped(t)
	migrations := testdb.Migrations(t)
	testdb.Apply(t, s.pool, migrations[len(migrations)-1])
	if left := s.legacyLeft(t); left != "0 tables, 0 columns" {
		t.Fatalf("at the latest migration: %s left", left)
	}
	for index := len(migrations) - 1; index >= 0; index-- {
		testdb.Apply(t, s.pool, strings.Replace(migrations[index], ".up.sql", ".down.sql", 1))
	}
	if tables := s.scalar(t, `SELECT count(*)::text FROM information_schema.tables WHERE table_schema = current_schema()`); tables != "0" {
		t.Fatalf("after every down: %s tables", tables)
	}
	for _, path := range migrations {
		testdb.Apply(t, s.pool, path)
	}
	if left := s.legacyLeft(t); left != "0 tables, 0 columns" {
		t.Fatalf("after every up: %s left", left)
	}
}
