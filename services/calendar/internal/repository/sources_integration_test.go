package repository_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/testdb"
)

// The sources migration applies to a calendar that already has events (all
// manual: before it no event had a source), and reverses cleanly.
func TestSourcesMigrationKeepsExistingEvents(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t, "000004")
	// Raw SQL: the generated queries expect the later migrations' columns.
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO events (title, all_day, start_at, end_at) VALUES ('Dentist', false, '2026-11-03T09:00:00Z', '2026-11-03T10:00:00Z') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var migration string
	for _, path := range testdb.Migrations(t) {
		if filepath.Base(path) == "000005_calendar_sources.up.sql" {
			migration = path
		}
	}
	if migration == "" {
		t.Fatal("000005_calendar_sources.up.sql not found")
	}
	testdb.Apply(t, pool, migration)
	var title, sourceKind string
	if err := pool.QueryRow(ctx, `SELECT title, source_kind FROM events WHERE id = $1`, id).Scan(&title, &sourceKind); err != nil || title != "Dentist" || sourceKind != "manual" {
		t.Fatalf("existing event after the migration: %q %q %v", title, sourceKind, err)
	}
	testdb.Apply(t, pool, strings.Replace(migration, ".up.sql", ".down.sql", 1))
	testdb.Apply(t, pool, migration)
	if _, err := pool.Exec(ctx, "SELECT id, name, url, filter_include FROM calendar_sources"); err != nil {
		t.Fatalf("after down and up again: %v", err)
	}
}

// Imported events must belong to a registered source; deleting the source
// deletes them. (A feed's refresh lease is calendar-feeds' since P11-08.)
func TestSourceRegistryConstraints(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t, "")
	queries := store.New(pool)
	feed := source(t, pool, "zaw")
	pickup := time.Date(2026, time.November, 17, 5, 0, 0, 0, time.UTC)
	imported := func(source pgtype.UUID, uid string) store.CreateEventParams {
		params := manualTimed("Papier", pickup, pickup.Add(time.Hour))
		params.SourceKind, params.SourceID, params.ExternalUid = "external", source, text(uid)
		return params
	}
	if _, err := queries.CreateEvent(ctx, imported(feed, "a")); err != nil {
		t.Fatal(err)
	}
	var pgError *pgconn.PgError
	if _, err := queries.CreateEvent(ctx, imported(pgtype.UUID{Bytes: googleuuid.New(), Valid: true}, "b")); !errors.As(err, &pgError) || pgError.ConstraintName != "events_source_id_fkey" {
		t.Fatalf("event of an unknown source: %v", err)
	}
	manual, err := queries.CreateEvent(ctx, manualTimed("Manual", pickup, pickup.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}

	if affected, err := queries.DeleteSource(ctx, feed); err != nil || affected != 1 {
		t.Fatalf("delete source: %d %v", affected, err)
	}
	events, err := queries.ListEvents(ctx, store.ListEventsParams{TimeZone: "UTC", RangeStart: pgtype.Timestamptz{Time: pickup.Add(-time.Hour), Valid: true}, RangeEnd: pgtype.Timestamptz{Time: pickup.Add(2 * time.Hour), Valid: true}, IncludeCancelled: true, PageLimit: 10})
	if err != nil || len(events) != 1 || events[0].Event.ID != manual.ID {
		t.Fatalf("after deleting the source: %+v %v", events, err)
	}
}

// The presets migration applies to existing sources (none is a preset) and
// reverses cleanly; a preset names at most one source. Raw SQL throughout:
// the generated queries expect the later migrations' columns.
func TestPresetsMigrationAndUniqueness(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t, "000005")
	if _, err := pool.Exec(ctx, `INSERT INTO calendar_sources (kind, name, url) VALUES ('ics', 'Existing', 'https://calendar.example.org/existing.ics')`); err != nil {
		t.Fatal(err)
	}
	var migration string
	for _, path := range testdb.Migrations(t) {
		if filepath.Base(path) == "000006_source_presets.up.sql" {
			migration = path
		}
	}
	if migration == "" {
		t.Fatal("000006_source_presets.up.sql not found")
	}
	testdb.Apply(t, pool, migration)
	var presets int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM calendar_sources WHERE preset IS NOT NULL OR preset_config IS NOT NULL`).Scan(&presets); err != nil || presets != 0 {
		t.Fatalf("existing source after the migration: %d presets, %v", presets, err)
	}

	create := func(name string) error {
		_, err := pool.Exec(ctx, `INSERT INTO calendar_sources (kind, name, url, preset, preset_config) VALUES ('ics', $1, 'https://calendar.example.org/zaw.ics', 'zaw', '{"name":"ZAW"}')`, name)
		return err
	}
	if err := create("first"); err != nil {
		t.Fatal(err)
	}
	var pgError *pgconn.PgError
	if err := create("second"); !errors.As(err, &pgError) || pgError.ConstraintName != "calendar_sources_preset_key" {
		t.Fatalf("a second source for the preset: %v", err)
	}
	for _, bad := range []string{`UPDATE calendar_sources SET preset = ' ' WHERE name = 'Existing'`, `UPDATE calendar_sources SET preset_config = '[]' WHERE name = 'first'`} {
		if _, err := pool.Exec(ctx, bad); !errors.As(err, &pgError) || pgError.Code != "23514" {
			t.Errorf("%s: %v", bad, err)
		}
	}

	testdb.Apply(t, pool, strings.Replace(migration, ".up.sql", ".down.sql", 1))
	testdb.Apply(t, pool, migration)
	var sources int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM calendar_sources WHERE preset IS NULL`).Scan(&sources); err != nil || sources != 2 {
		t.Fatalf("after down and up again: %d sources, %v", sources, err)
	}
}
