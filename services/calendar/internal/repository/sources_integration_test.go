package repository_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	queries := store.New(pool)
	start := time.Date(2026, time.November, 3, 9, 0, 0, 0, time.UTC)
	kept, err := queries.CreateEvent(ctx, manualTimed("Dentist", start, start.Add(time.Hour)))
	if err != nil {
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
	if got, err := queries.GetEvent(ctx, kept.ID); err != nil || got.Title != "Dentist" || got.SourceKind != "manual" {
		t.Fatalf("existing event after the migration: %+v %v", got, err)
	}
	testdb.Apply(t, pool, strings.Replace(migration, ".up.sql", ".down.sql", 1))
	testdb.Apply(t, pool, migration)
	if _, err := queries.ListSources(ctx); err != nil {
		t.Fatalf("after down and up again: %v", err)
	}
}

// Imported events must belong to a registered source; deleting the source
// deletes them. The refresh lease admits one refresh at a time and is taken
// over once it expires.
func TestSourceRegistryConstraintsAndLease(t *testing.T) {
	ctx := context.Background()
	queries := store.New(testdb.Open(t, ""))
	feed := source(t, queries, "zaw")
	pickup := time.Date(2026, time.November, 17, 5, 0, 0, 0, time.UTC)
	imported := func(source pgtype.UUID, uid string) store.CreateEventParams {
		params := manualTimed("Papier", pickup, pickup.Add(time.Hour))
		params.SourceKind, params.SourceID, params.ExternalUid = "ics", source, text(uid)
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

	now := time.Date(2026, time.October, 10, 10, 0, 0, 0, time.UTC)
	claim := func(at time.Time) error {
		_, err := queries.ClaimSourceRefresh(ctx, store.ClaimSourceRefreshParams{ID: feed, Now: pgtype.Timestamptz{Time: at, Valid: true}, LeaseUntil: pgtype.Timestamptz{Time: at.Add(3 * time.Minute), Valid: true}})
		return err
	}
	if err := claim(now); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := claim(now.Add(time.Minute)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second claim while leased: %v", err)
	}
	// A crashed refresh's lease expires and is taken over.
	if err := claim(now.Add(4 * time.Minute)); err != nil {
		t.Fatalf("claim after expiry: %v", err)
	}
	if err := queries.ReleaseSourceRefresh(ctx, feed); err != nil {
		t.Fatal(err)
	}
	if err := claim(now.Add(5 * time.Minute)); err != nil {
		t.Fatalf("claim after release: %v", err)
	}

	if affected, err := queries.DeleteSource(ctx, feed); err != nil || affected != 1 {
		t.Fatalf("delete source: %d %v", affected, err)
	}
	events, err := queries.ListEvents(ctx, store.ListEventsParams{TimeZone: "UTC", RangeStart: pgtype.Timestamptz{Time: pickup.Add(-time.Hour), Valid: true}, RangeEnd: pgtype.Timestamptz{Time: pickup.Add(2 * time.Hour), Valid: true}, IncludeCancelled: true, PageLimit: 10})
	if err != nil || len(events) != 1 || events[0].Event.ID != manual.ID {
		t.Fatalf("after deleting the source: %+v %v", events, err)
	}
}
