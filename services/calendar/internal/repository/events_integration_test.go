package repository_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

func TestEventPersistenceSupportsTimedAndAllDayEvents(t *testing.T) {
	url := os.Getenv("CALENDAR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CALENDAR_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("calendar_test_%d", time.Now().UnixNano())
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") })
	for _, name := range []string{"000002_events.up.sql", "000003_automation_idempotency.up.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	queries := store.New(pool)
	start := time.Date(2026, time.October, 6, 9, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	frequency := "yearly"
	interval := int32(1)
	timed, err := queries.CreateEvent(ctx, store.CreateEventParams{Title: "Standup", AllDay: false, StartAt: pgtype.Timestamptz{Time: start, Valid: true}, EndAt: pgtype.Timestamptz{Time: end, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	allDay, err := queries.CreateEvent(ctx, store.CreateEventParams{Title: "Birthday", AllDay: true, StartDate: pgtype.Date{Time: start, Valid: true}, EndDate: pgtype.Date{Time: start, Valid: true}, RecurrenceFrequency: &frequency, RecurrenceInterval: &interval})
	if err != nil {
		t.Fatal(err)
	}
	items, err := queries.ListEvents(ctx, store.ListEventsParams{FromDate: pgtype.Date{Time: start, Valid: true}, ToDate: pgtype.Date{Time: start, Valid: true}, FromTime: pgtype.Timestamptz{Time: start, Valid: true}, ToTime: pgtype.Timestamptz{Time: end, Valid: true}, PageLimit: 10})
	if err != nil || len(items) != 2 {
		t.Fatalf("events=%#v err=%v", items, err)
	}
	if _, err = queries.UpdateEvent(ctx, store.UpdateEventParams{ID: timed.ID, Title: stringPointer("Planning")}); err != nil {
		t.Fatal(err)
	}
	if affected, err := queries.DeleteEvent(ctx, allDay.ID); err != nil || affected != 1 {
		t.Fatalf("delete=%d err=%v", affected, err)
	}
	key, hash := "processor-event-1", "payload-a"
	first, err := queries.CreateEvent(ctx, store.CreateEventParams{Title: "Only once", AllDay: true, StartDate: pgtype.Date{Time: start, Valid: true}, EndDate: pgtype.Date{Time: start, Valid: true}, AutomationIdempotencyKey: &key, AutomationPayloadHash: &hash})
	if err != nil {
		t.Fatal(err)
	}
	// A replay after an Inbox bookkeeping crash must recover the original row.
	replayed, err := queries.CreateEvent(ctx, store.CreateEventParams{Title: "Only once", AllDay: true, StartDate: pgtype.Date{Time: start, Valid: true}, EndDate: pgtype.Date{Time: start, Valid: true}, AutomationIdempotencyKey: &key, AutomationPayloadHash: &hash})
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("replayed event = %#v, err = %v; want %s", replayed, err, first.ID.String())
	}
}
func stringPointer(value string) *string { return &value }
