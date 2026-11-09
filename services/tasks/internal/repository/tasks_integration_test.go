package repository_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/tasks/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/tasks/internal/testdb"
)

// testQueries runs the migrations up to 000003 in a throwaway schema of
// TASKS_TEST_DATABASE_URL (testdb), or skips the test when it is not set.
// testdb installs pgcrypto once, outside the schemas, as the other test
// packages running in parallel on the same database expect.
func testQueries(t *testing.T) (context.Context, *store.Queries) {
	t.Helper()
	return context.Background(), store.New(testdb.Open(t, "000003"))
}

func TestTaskPersistenceLifecycle(t *testing.T) {
	ctx, queries := testQueries(t)
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
	search := "chicken"
	found, err := queries.ListTasks(ctx, store.ListTasksParams{SearchQuery: &search, PageLimit: 10})
	if err != nil || len(found) != 1 || found[0].ID != undated.ID {
		t.Fatalf("task search = %#v, err = %v", found, err)
	}
	renamed := "Pay electricity bill"
	notes := "Review the amount before paying"
	movedDue := due.AddDate(0, 0, 7)
	updated, err := queries.UpdateTask(ctx, store.UpdateTaskParams{ID: created.ID, Title: &renamed, Notes: &notes, DueAt: pgtype.Timestamptz{Time: movedDue, Valid: true}})
	if err != nil || updated.Title != renamed || updated.Notes == nil || *updated.Notes != notes || !updated.DueAt.Valid || !updated.DueAt.Time.Equal(movedDue) {
		t.Fatalf("updated task = %#v, err = %v", updated, err)
	}
	cleared, err := queries.UpdateTask(ctx, store.UpdateTaskParams{ID: created.ID, ClearDueAt: true})
	if err != nil || cleared.DueAt.Valid {
		t.Fatalf("cleared task = %#v, err = %v", cleared, err)
	}
	completed, err := queries.CompleteTask(ctx, created.ID)
	if err != nil || !completed.CompletedAt.Valid {
		t.Fatalf("completed = %#v, err = %v", completed, err)
	}
	reopened, err := queries.ReopenTask(ctx, created.ID)
	if err != nil || reopened.CompletedAt.Valid {
		t.Fatalf("reopened = %#v, err = %v", reopened, err)
	}
	completed, err = queries.CompleteTask(ctx, created.ID)
	if err != nil || !completed.CompletedAt.Valid {
		t.Fatalf("recompleted = %#v, err = %v", completed, err)
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

// The list is ordered open Tasks first (soonest due, undated last and newest
// first), then completed Tasks (most recently completed first), and paging
// with the last row as the cursor walks that order without gaps or repeats,
// for every filter the Dashboard uses.
func TestTaskListPagesInListOrder(t *testing.T) {
	ctx, queries := testQueries(t)
	base := time.Date(2026, time.October, 10, 9, 0, 0, 0, time.UTC)
	create := func(title string, due *time.Time) store.Task {
		t.Helper()
		params := store.CreateTaskParams{Title: title}
		if due != nil {
			params.DueAt = pgtype.Timestamptz{Time: *due, Valid: true}
		}
		item, err := queries.CreateTask(ctx, params)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	at := func(days int) *time.Time {
		value := base.AddDate(0, 0, days)
		return &value
	}
	// Two Tasks share a due date, so created_at and id decide between them.
	later := create("due in three days", at(3))
	sameA := create("due tomorrow A", at(1))
	sameB := create("due tomorrow B", at(1))
	overdue := create("overdue", at(-2))
	undatedOld := create("undated older", nil)
	undatedNew := create("undated newer", nil)
	doneFirst := create("completed first", at(-5))
	doneSecond := create("completed second", nil)
	doneThird := create("completed third", at(4))
	for _, item := range []store.Task{doneFirst, doneSecond, doneThird} {
		if _, err := queries.CompleteTask(ctx, item.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Same due date: newest created first.
	tomorrow := []store.Task{sameB, sameA}
	open := append([]store.Task{overdue}, tomorrow...)
	open = append(open, later, undatedNew, undatedOld)
	completed := []store.Task{doneThird, doneSecond, doneFirst}

	for name, test := range map[string]struct {
		params store.ListTasksParams
		want   []store.Task
	}{
		"everything":    {store.ListTasksParams{}, append(append([]store.Task{}, open...), completed...)},
		"open":          {store.ListTasksParams{CompletedFilter: pointer(false)}, open},
		"completed":     {store.ListTasksParams{CompletedFilter: pointer(true)}, completed},
		"open undated":  {store.ListTasksParams{CompletedFilter: pointer(false), UndatedFilter: pointer(true)}, []store.Task{undatedNew, undatedOld}},
		"open dated":    {store.ListTasksParams{CompletedFilter: pointer(false), UndatedFilter: pointer(false)}, []store.Task{overdue, sameB, sameA, later}},
		"due by Monday": {store.ListTasksParams{CompletedFilter: pointer(false), DueBefore: pgtype.Timestamptz{Time: *at(2), Valid: true}}, []store.Task{overdue, sameB, sameA}},
	} {
		for _, size := range []int32{1, 2, 4, 100} {
			got := []store.Task{}
			params := test.params
			params.PageLimit = size
			for page := 0; ; page++ {
				if page > 20 {
					t.Fatalf("%s by %d: paging did not end", name, size)
				}
				items, err := queries.ListTasks(ctx, params)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, items...)
				if len(items) < int(size) {
					break
				}
				last := items[len(items)-1]
				params.AfterCompletedAt, params.AfterDueAt, params.AfterCreatedAt, params.AfterID = last.CompletedAt, last.DueAt, last.CreatedAt, last.ID
			}
			if titles(got) != titles(test.want) {
				t.Errorf("%s by %d:\n got %s\nwant %s", name, size, titles(got), titles(test.want))
			}
		}
	}
}

func titles(tasks []store.Task) string {
	names := make([]string, 0, len(tasks))
	for _, task := range tasks {
		names = append(names, task.Title)
	}
	return strings.Join(names, ", ")
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
