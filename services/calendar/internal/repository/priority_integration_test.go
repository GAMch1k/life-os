package repository_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/services/calendar/internal/testdb"
)

// The priority migration gives every existing event no priority of its own
// (medium) and every source medium as its default, accepts only low, medium,
// and high, and reverses cleanly: going down drops the priorities, also
// those of occurrences, and keeps the events.
func TestPriorityMigration(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t, "000010")
	var sourceID, series pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO calendar_sources (kind, name, url) VALUES ('ics', 'ZAW', 'https://calendar.example.org/zaw.ics') RETURNING id`).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO events (title, all_day, start_at, end_at, recurrence_frequency, recurrence_interval)
		VALUES ('Gym', false, '2026-10-10T05:00:00Z', '2026-10-10T06:00:00Z', 'weekly', 1) RETURNING id`).Scan(&series); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO events (title, all_day, start_date, end_date, source_kind, source_id, external_uid)
		VALUES ('Papier', true, '2026-10-15', '2026-10-15', 'ics', $1, 'paper@zaw')`, sourceID); err != nil {
		t.Fatal(err)
	}
	var migration string
	for _, path := range testdb.Migrations(t) {
		if filepath.Base(path) == "000011_event_priority.up.sql" {
			migration = path
		}
	}
	if migration == "" {
		t.Fatal("000011_event_priority.up.sql not found")
	}
	testdb.Apply(t, pool, migration)

	// Rows are read with SQL: the generated queries know the later columns.
	var title string
	var priority *string
	if err := pool.QueryRow(ctx, `SELECT title, priority FROM events WHERE id = $1`, series).Scan(&title, &priority); err != nil || priority != nil || title != "Gym" {
		t.Fatalf("migrated event = %q %v, %v", title, priority, err)
	}
	var zaw, other string
	if err := pool.QueryRow(ctx, `SELECT default_priority FROM calendar_sources WHERE id = $1`, sourceID).Scan(&zaw); err != nil || zaw != "medium" {
		t.Fatalf("migrated source priority = %q, %v", zaw, err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO calendar_sources (kind, name, url) VALUES ('ics', 'other', 'https://calendar.example.org/other.ics') RETURNING default_priority`).Scan(&other); err != nil || other != "medium" {
		t.Fatalf("a source created without a default priority = %q, %v", other, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE events SET priority = 'high' WHERE id = $1`, series); err != nil {
		t.Fatalf("high: %v", err)
	}
	violations := map[string]struct {
		constraint string
		statement  string
	}{
		"an unknown priority":         {"events_priority_check", `UPDATE events SET priority = 'urgent' WHERE id = $1`},
		"an unknown default priority": {"calendar_sources_default_priority_check", `UPDATE calendar_sources SET default_priority = 'urgent' WHERE id <> $1`},
		"no default priority":         {"", `UPDATE calendar_sources SET default_priority = NULL WHERE id <> $1`},
	}
	for name, tc := range violations {
		_, err := pool.Exec(ctx, tc.statement, series)
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.ConstraintName != tc.constraint {
			t.Errorf("%s: err = %v, want a %q violation", name, err, tc.constraint)
		}
	}
	// One occurrence only has its own priority, one is also skipped, one
	// also has its own title.
	for rid, changes := range map[string]string{
		"2026-10-17T05:00:00Z": `{"priority":"low"}`,
		"2026-10-24T05:00:00Z": `{"priority":"low"}`,
		"2026-10-31T06:00:00Z": `{"priority":"medium","title":"Gym with Anna"}`,
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO event_exceptions (event_id, recurrence_id, changes) VALUES ($1, $2, $3)`, series, rid, changes); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE event_exceptions SET cancelled_at = now() WHERE recurrence_id = '2026-10-24T05:00:00Z'`); err != nil {
		t.Fatal(err)
	}

	testdb.Apply(t, pool, strings.Replace(migration, ".up.sql", ".down.sql", 1))
	var columns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND column_name IN ('priority', 'default_priority')`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("down: priority columns = %d, %v", columns, err)
	}
	rows, err := pool.Query(ctx, `SELECT recurrence_id, changes::text, cancelled_at IS NOT NULL FROM event_exceptions ORDER BY recurrence_id`)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for rows.Next() {
		var rid, changes string
		var cancelled bool
		if err := rows.Scan(&rid, &changes, &cancelled); err != nil {
			t.Fatal(err)
		}
		label := rid + " " + changes
		if cancelled {
			label += " cancelled"
		}
		left = append(left, label)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(left, " | "), `2026-10-24T05:00:00Z {} cancelled | 2026-10-31T06:00:00Z {"title": "Gym with Anna"}`; got != want {
		t.Fatalf("down: exceptions\n got  %s\n want %s", got, want)
	}
	var events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&events); err != nil || events != 2 {
		t.Fatalf("down: events = %d, %v", events, err)
	}
	testdb.Apply(t, pool, migration)
	if err := pool.QueryRow(ctx, `SELECT priority FROM events WHERE id = $1`, series).Scan(&priority); err != nil || priority != nil {
		t.Fatalf("down and up again: %v, %v", priority, err)
	}
}
