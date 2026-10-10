package repository

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/services/calendar/internal/testdb"
)

// The Google Calendar handover (000016) gives the sources Calendar's
// in-process Google sync kept to the google-calendar integration: not in
// process, kind external, their events too, keeping every event with its
// uid and the owner's reminders and priority. Feeds and Life OS events stay
// as they are, the journal attributes the change to the integration, and
// going down gives the sources back.
func TestGoogleCalendarHandoverMigration(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t, "000014")
	var connection, calendar, readOnlyCalendar, source, readOnlySource, feed string
	if err := pool.QueryRow(ctx, `INSERT INTO google_connections (account_email, refresh_token_ciphertext, refresh_token_nonce)
		VALUES ('owner@example.com', '\x01', '\x02') RETURNING id`).Scan(&connection); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, role string
		out      *string
	}{{"owner@example.com", "owner", &calendar}, {"holidays@group.v.calendar.google.com", "reader", &readOnlyCalendar}} {
		if err := pool.QueryRow(ctx, `INSERT INTO google_calendars (connection_id, google_calendar_id, summary, access_role, sync_enabled)
			VALUES ($1, $2, 'Cal', $3, true) RETURNING id`, connection, row.id, row.role).Scan(row.out); err != nil {
			t.Fatal(err)
		}
	}
	// In-process Google sources as Calendar created them (and 000014 gave
	// their identity).
	for _, row := range []struct {
		calendar, externalID string
		readOnly             bool
		out                  *string
	}{{calendar, "owner@example.com", false, &source}, {readOnlyCalendar, "holidays@group.v.calendar.google.com", true, &readOnlySource}} {
		if err := pool.QueryRow(ctx, `INSERT INTO calendar_sources (kind, name, url, google_calendar_id, integration, external_id, provider, read_only)
			VALUES ('google', 'Owner', 'https://www.googleapis.com/calendar/v3/calendars/x/events', $1, 'google-calendar', $2, 'Google', $3) RETURNING id`,
			row.calendar, row.externalID, row.readOnly).Scan(row.out); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `INSERT INTO calendar_sources (kind, name, url, integration, external_id, provider)
		VALUES ('ics', 'ZAW', 'https://calendar.example.org/zaw.ics', 'calendar-feeds', gen_random_uuid()::text, 'ZAW') RETURNING id`).Scan(&feed); err != nil {
		t.Fatal(err)
	}
	var dentist, series, holiday, paper, manual string
	if err := pool.QueryRow(ctx, `INSERT INTO events (title, all_day, start_at, end_at, source_kind, source_id, external_uid, reminders, use_default_reminders, source_reminders, priority)
		VALUES ('Dentist', false, '2026-10-12T08:00:00Z', '2026-10-12T09:00:00Z', 'google', $1, 'g-1', '[{"kind":"relative","minutesBefore":45}]', false,
		        '{"useDefault": true, "reminders": []}', 'high') RETURNING id`, source).Scan(&dentist); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO events (title, all_day, start_at, end_at, source_kind, source_id, external_uid, recurrence_frequency, recurrence_interval)
		VALUES ('Team call', false, '2026-10-13T07:00:00Z', '2026-10-13T07:30:00Z', 'google', $1, 'g-2', 'weekly', 1) RETURNING id`, source).Scan(&series); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO event_exceptions (event_id, recurrence_id, cancelled_at) VALUES ($1, '2026-10-20T07:00:00Z', now())`, series); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO events (title, all_day, start_date, end_date, source_kind, source_id, external_uid)
		VALUES ('Holiday', true, '2026-10-03', '2026-10-03', 'google', $1, 'h-1') RETURNING id`, readOnlySource).Scan(&holiday); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO events (title, all_day, start_date, end_date, source_kind, source_id, external_uid)
		VALUES ('Papier', true, '2026-10-15', '2026-10-15', 'ics', $1, 'paper@zaw') RETURNING id`, feed).Scan(&paper); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO events (title, all_day, start_at, end_at) VALUES ('Gym', false, '2026-10-10T05:00:00Z', '2026-10-10T06:00:00Z') RETURNING id`).Scan(&manual); err != nil {
		t.Fatal(err)
	}
	var journalBefore int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(max(seq), 0) FROM calendar_event_changes`).Scan(&journalBefore); err != nil {
		t.Fatal(err)
	}

	migration := handoverMigration(t)
	testdb.Apply(t, pool, migration)

	sourceState := func(id string) (kind string, inProcess bool) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT kind, in_process FROM calendar_sources WHERE id = $1`, id).Scan(&kind, &inProcess); err != nil {
			t.Fatal(err)
		}
		return kind, inProcess
	}
	for _, id := range []string{source, readOnlySource} {
		if kind, inProcess := sourceState(id); kind != "external" || inProcess {
			t.Fatalf("handed-over source = %s, in process %v", kind, inProcess)
		}
	}
	if kind, inProcess := sourceState(feed); kind != "ics" || !inProcess {
		t.Fatalf("feed = %s, in process %v", kind, inProcess)
	}
	kindOf := func(id string) string {
		t.Helper()
		var kind string
		if err := pool.QueryRow(ctx, `SELECT source_kind FROM events WHERE id = $1`, id).Scan(&kind); err != nil {
			t.Fatal(err)
		}
		return kind
	}
	for id, want := range map[string]string{dentist: "external", series: "external", holiday: "external", paper: "ics", manual: "manual"} {
		if got := kindOf(id); got != want {
			t.Fatalf("event %s kind = %s, want %s", id, got, want)
		}
	}
	// The owner's reminders and priority, the uid, and the skip stay.
	var reminders, sourceReminders, priority, uid string
	var useDefault bool
	if err := pool.QueryRow(ctx, `SELECT reminders::text, use_default_reminders, source_reminders::text, priority, external_uid FROM events WHERE id = $1`, dentist).
		Scan(&reminders, &useDefault, &sourceReminders, &priority, &uid); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reminders, `"minutesBefore": 45`) || useDefault || !strings.Contains(sourceReminders, `"useDefault": true`) || priority != "high" || uid != "g-1" {
		t.Fatalf("handed-over event: reminders %s (default %v), source %s, priority %s, uid %s", reminders, useDefault, sourceReminders, priority, uid)
	}
	var skips int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_exceptions WHERE event_id = $1 AND cancelled_at IS NOT NULL`, series).Scan(&skips); err != nil || skips != 1 {
		t.Fatalf("skips = %d, %v", skips, err)
	}
	// The journal names the integration for its writable source's events
	// (a read-only source's are not followed), so it skips them as its own.
	rows, err := pool.Query(ctx, `SELECT event_id::text, actor FROM calendar_event_changes WHERE seq > $1 ORDER BY seq`, journalBefore)
	if err != nil {
		t.Fatal(err)
	}
	journal := map[string]string{}
	for rows.Next() {
		var event, actor string
		if err := rows.Scan(&event, &actor); err != nil {
			t.Fatal(err)
		}
		journal[event] = actor
	}
	rows.Close()
	if len(journal) != 2 || journal[dentist] != "system:google-calendar" || journal[series] != "system:google-calendar" {
		t.Fatalf("journal after the handover = %v", journal)
	}
	// The legacy tables stay until P11-09.
	var calendars int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM google_calendars`).Scan(&calendars); err != nil || calendars != 2 {
		t.Fatalf("legacy calendars = %d, %v", calendars, err)
	}
	// Applying it again changes nothing.
	testdb.Apply(t, pool, migration)

	// A source the integration registers afterwards has no legacy calendar.
	var registered string
	if err := pool.QueryRow(ctx, `INSERT INTO calendar_sources (kind, name, integration, external_id, provider, read_only, in_process)
		VALUES ('external', 'Family', 'google-calendar', 'family@group.calendar.google.com', 'Google', false, false) RETURNING id`).Scan(&registered); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO events (title, all_day, start_at, end_at, source_kind, source_id, external_uid, read_only_reason)
		VALUES ('Birthday', false, '2026-10-14T08:00:00Z', '2026-10-14T09:00:00Z', 'external', $1, 'f-1_20261014', 'One occurrence')`, registered); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE events SET read_only_reason = 'One occurrence' WHERE id = $1`, holiday); err != nil {
		t.Fatal(err)
	}

	testdb.Apply(t, pool, strings.Replace(migration, ".up.sql", ".down.sql", 1))
	for _, id := range []string{source, readOnlySource} {
		if kind, inProcess := sourceState(id); kind != "google" || !inProcess {
			t.Fatalf("source after down = %s, in process %v", kind, inProcess)
		}
	}
	if kind, inProcess := sourceState(registered); kind != "external" || inProcess {
		t.Fatalf("registered source after down = %s, in process %v", kind, inProcess)
	}
	for id, want := range map[string]string{dentist: "google", holiday: "google", paper: "ics", manual: "manual"} {
		if got := kindOf(id); got != want {
			t.Fatalf("event %s kind after down = %s, want %s", id, got, want)
		}
	}
	testdb.Apply(t, pool, migration)
	if kind, _ := sourceState(source); kind != "external" {
		t.Fatalf("source after up again = %s", kind)
	}
}

func handoverMigration(t *testing.T) string {
	t.Helper()
	for _, path := range testdb.Migrations(t) {
		if filepath.Base(path) == "000016_google_calendar_handover.up.sql" {
			return path
		}
	}
	t.Fatal("000016_google_calendar_handover.up.sql not found")
	return ""
}
