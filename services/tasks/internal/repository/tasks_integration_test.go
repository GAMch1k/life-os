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

	store "github.com/GAMch1k/life-os/services/tasks/internal/repository/generated"
)

func TestTaskPersistenceLifecycle(t *testing.T) {
	url := os.Getenv("TASKS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TASKS_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("tasks_test_%d", time.Now().UnixNano())
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
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") })
	for _, name := range []string{"000002_tasks.up.sql", "000003_automation_idempotency.up.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	queries := store.New(pool)
	due := time.Date(2026, time.October, 7, 9, 0, 0, 0, time.UTC)
	frequency := "weekly"
	interval := int32(1)
	created, err := queries.CreateTask(ctx, store.CreateTaskParams{Title: "Pay bill", DueAt: pgtype.Timestamptz{Time: due, Valid: true}, RecurrenceFrequency: &frequency, RecurrenceInterval: &interval})
	if err != nil {
		t.Fatal(err)
	}
	undated, err := queries.CreateTask(ctx, store.CreateTaskParams{Title: "Buy chicken for dinner"})
	if err != nil {
		t.Fatal(err)
	}
	open, err := queries.ListTasks(ctx, store.ListTasksParams{CompletedFilter: pointer(false), PageLimit: 10})
	if err != nil || len(open) != 2 || !containsTask(open, created.ID) || !containsTask(open, undated.ID) {
		t.Fatalf("open tasks = %#v, err = %v", open, err)
	}
	// Date-bounded views include unscheduled tasks by default: an absent due date
	// must not make an otherwise open task disappear. Consumers that need a strict
	// date-only view can opt out with IncludeUndated.
	dueBefore := pgtype.Timestamptz{Time: due.Add(24 * time.Hour), Valid: true}
	visible, err := queries.ListTasks(ctx, store.ListTasksParams{CompletedFilter: pointer(false), DueBefore: dueBefore, IncludeUndated: true, PageLimit: 10})
	if err != nil || len(visible) != 2 || !containsTask(visible, created.ID) || !containsTask(visible, undated.ID) {
		t.Fatalf("date-bounded tasks including undated = %#v, err = %v", visible, err)
	}
	strict, err := queries.ListTasks(ctx, store.ListTasksParams{CompletedFilter: pointer(false), DueBefore: dueBefore, IncludeUndated: false, PageLimit: 10})
	if err != nil || len(strict) != 1 || strict[0].ID != created.ID {
		t.Fatalf("strict date-bounded tasks = %#v, err = %v", strict, err)
	}
	if undated.DueAt.Valid {
		t.Fatalf("undated task was assigned a due date: %#v", undated.DueAt)
	}
	completed, err := queries.CompleteTask(ctx, created.ID)
	if err != nil || !completed.CompletedAt.Valid {
		t.Fatalf("completed = %#v, err = %v", completed, err)
	}
	closed, err := queries.ListTasks(ctx, store.ListTasksParams{CompletedFilter: pointer(true), PageLimit: 10})
	if err != nil || len(closed) != 1 || !closed[0].CompletedAt.Valid {
		t.Fatalf("completed tasks = %#v, err = %v", closed, err)
	}
	key, hash := "processor-task-1", "payload-a"
	first, err := queries.CreateTask(ctx, store.CreateTaskParams{Title: "Only once", AutomationIdempotencyKey: &key, AutomationPayloadHash: &hash})
	if err != nil {
		t.Fatal(err)
	}
	// This is the persistence boundary used when a processor crashes after the
	// owner has committed but before it can record the Inbox outcome.
	replayed, err := queries.CreateTask(ctx, store.CreateTaskParams{Title: "Only once", AutomationIdempotencyKey: &key, AutomationPayloadHash: &hash})
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("replayed task = %#v, err = %v; want %s", replayed, err, first.ID.String())
	}
}
func pointer(value bool) *bool { return &value }

func containsTask(tasks []store.Task, id pgtype.UUID) bool {
	for _, task := range tasks {
		if task.ID == id {
			return true
		}
	}
	return false
}
