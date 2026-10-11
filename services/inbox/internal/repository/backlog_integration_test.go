package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	store "github.com/GAMch1k/life-os/services/inbox/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/inbox/internal/testdb"
)

// migratedQueries applies every up migration in order to a throwaway schema.
func migratedQueries(t *testing.T) (*store.Queries, *pgxpool.Pool) {
	t.Helper()
	pool := testdb.Open(t, "inbox_test")
	return store.New(pool), pool
}

func TestInboxBacklogSummarizesPendingWorkAndLatestFailure(t *testing.T) {
	queries, pool := migratedQueries(t)
	ctx := context.Background()

	empty, err := queries.GetInboxBacklog(ctx)
	if err != nil || empty.PendingCount != 0 || empty.OldestPendingAt.Valid {
		t.Fatalf("empty backlog = %+v, err = %v", empty, err)
	}

	capture := func(content string) store.InboxItem {
		item, err := queries.CreateInboxItem(ctx, store.CreateInboxItemParams{Content: content, Source: "manual", SourceMetadata: []byte(`{}`), MediaReferences: []byte(`[]`)})
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	oldest := capture("oldest pending")
	retrying := capture("retrying")
	review := capture("needs review")
	done := capture("done")
	if _, err := pool.Exec(ctx, "UPDATE inbox_items SET created_at = now() - interval '2 hours' WHERE id = $1", oldest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE inbox_items SET state = 'human_review' WHERE id = $1", review.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE inbox_items SET state = 'resolved' WHERE id = $1", done.ID); err != nil {
		t.Fatal(err)
	}
	attempt := func(item store.InboxItem, key, status, summary string) {
		if _, err := pool.Exec(ctx, `INSERT INTO inbox_processing_attempts (inbox_item_id, idempotency_key, status, agent_name, model_profile, error_summary)
			VALUES ($1, $2, $3, 'codex-inbox-processor', 'test', NULLIF($4, ''))`, item.ID, key, status, summary); err != nil {
			t.Fatal(err)
		}
	}
	// A pending item that already failed is retrying even while its next
	// attempt runs; a resolved item's failures are history.
	attempt(retrying, "a1", "retryable", "older failure")
	time.Sleep(5 * time.Millisecond)
	attempt(retrying, "a2", "retryable", "ERROR: You've hit your usage limit.")
	attempt(retrying, "a3", "processing", "")
	attempt(done, "d1", "retryable", "resolved item failure")
	attempt(oldest, "o1", "processed", "")

	backlog, err := queries.GetInboxBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if backlog.PendingCount != 2 || backlog.RetryingCount != 1 || backlog.HumanReviewCount != 1 {
		t.Fatalf("backlog = %+v", backlog)
	}
	if !backlog.OldestPendingAt.Valid || time.Since(backlog.OldestPendingAt.Time) < 90*time.Minute {
		t.Fatalf("oldest pending = %+v", backlog.OldestPendingAt)
	}
	failure, err := queries.GetLatestPendingFailure(ctx)
	if err != nil || failure.ErrorSummary == nil || *failure.ErrorSummary != "ERROR: You've hit your usage limit." {
		t.Fatalf("latest failure = %+v, err = %v", failure, err)
	}
}
