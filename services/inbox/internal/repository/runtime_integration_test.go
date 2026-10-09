package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	store "github.com/GAMch1k/life-os/services/inbox/internal/repository/generated"
)

func TestAttemptRuntimeHeartbeatsAndStaleDetection(t *testing.T) {
	queries, pool := migratedQueries(t)
	ctx := context.Background()
	item, err := queries.CreateInboxItem(ctx, store.CreateInboxItemParams{Content: "call the dentist", Source: "manual", SourceMetadata: []byte(`{}`), MediaReferences: []byte(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	attempt := func(key string) store.InboxProcessingAttempt {
		created, err := queries.CreateProcessingAttempt(ctx, store.CreateProcessingAttemptParams{InboxItemID: item.ID, IdempotencyKey: key, AgentName: "codex-inbox-processor", ModelProfile: "test", Actions: []byte(`[]`)})
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	text := func(value string) *string { return &value }
	live, abandoned, legacy, finished := attempt("live"), attempt("abandoned"), attempt("legacy"), attempt("finished")

	// spawning before the process exists, then running with the session id.
	for _, step := range []struct{ state, session *string }{{text("spawning"), nil}, {text("running"), text("thread-1")}} {
		updated, err := queries.RecordAttemptRuntime(ctx, store.RecordAttemptRuntimeParams{ID: live.ID, InboxItemID: item.ID, RuntimeState: step.state, RuntimeOwner: text("processor-a"), RuntimeSessionID: step.session})
		if err != nil || *updated.RuntimeState != *step.state || !updated.RuntimeHeartbeatAt.Valid {
			t.Fatalf("record %s = %+v, err = %v", *step.state, updated, err)
		}
	}
	// A later heartbeat without a session id keeps the recorded one.
	kept, err := queries.RecordAttemptRuntime(ctx, store.RecordAttemptRuntimeParams{ID: live.ID, InboxItemID: item.ID, RuntimeState: text("running"), RuntimeOwner: text("processor-a")})
	if err != nil || kept.RuntimeSessionID == nil || *kept.RuntimeSessionID != "thread-1" {
		t.Fatalf("heartbeat = %+v, err = %v", kept, err)
	}

	if _, err := queries.RecordAttemptRuntime(ctx, store.RecordAttemptRuntimeParams{ID: abandoned.ID, InboxItemID: item.ID, RuntimeState: text("running"), RuntimeOwner: text("processor-gone")}); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE inbox_processing_attempts SET runtime_heartbeat_at = now() - interval '10 minutes' WHERE id = $1", abandoned.ID)
	exec("UPDATE inbox_processing_attempts SET status = 'processing', updated_at = now() - interval '3 days' WHERE id = $1", legacy.ID)
	exec("UPDATE inbox_processing_attempts SET status = 'processed', updated_at = now() - interval '3 days' WHERE id = $1", finished.ID)

	stale, err := queries.ListStaleProcessingAttempts(ctx, 120)
	if err != nil {
		t.Fatal(err)
	}
	got := map[[16]byte]bool{}
	for _, attempt := range stale {
		got[attempt.ID.Bytes] = true
	}
	if len(stale) != 2 || !got[abandoned.ID.Bytes] || !got[legacy.ID.Bytes] {
		t.Fatalf("stale = %d attempts, want the abandoned and the legacy one", len(stale))
	}

	// A finished attempt's runtime can no longer change.
	if _, err := queries.RecordAttemptRuntime(ctx, store.RecordAttemptRuntimeParams{ID: finished.ID, InboxItemID: item.ID, RuntimeState: text("running"), RuntimeOwner: text("processor-a")}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("finished attempt runtime err = %v", err)
	}
}
