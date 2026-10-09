package repository_test

import (
	"context"
	"testing"

	store "github.com/GAMch1k/life-os/services/inbox/internal/repository/generated"
)

func TestInboxPersistenceLifecycle(t *testing.T) {
	queries, _ := migratedQueries(t)
	ctx := context.Background()
	created, err := queries.CreateInboxItem(ctx, store.CreateInboxItemParams{Content: "remember milk", Source: "manual", SourceMetadata: []byte(`{"screen":"dashboard"}`), MediaReferences: []byte(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	items, err := queries.ListInboxItems(ctx, store.ListInboxItemsParams{UnresolvedFilter: boolPointer(true), PageLimit: 10})
	if err != nil || len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("unresolved = %#v, err = %v", items, err)
	}
	resolved, err := queries.SetInboxItemState(ctx, store.SetInboxItemStateParams{ID: created.ID, State: "resolved"})
	if err != nil || !resolved.ResolvedAt.Valid {
		t.Fatalf("resolved = %#v, err = %v", resolved, err)
	}
	items, err = queries.ListInboxItems(ctx, store.ListInboxItemsParams{UnresolvedFilter: boolPointer(true), PageLimit: 10})
	if err != nil || len(items) != 0 {
		t.Fatalf("unresolved after resolution = %#v, err = %v", items, err)
	}

	// A retry preserves the failed attempt and uses a distinct attempt key;
	// this is real PostgreSQL coverage for the lifecycle that formerly failed
	// on the per-item idempotency unique constraint.
	retryItem, err := queries.CreateInboxItem(ctx, store.CreateInboxItemParams{Content: "retry me", Source: "manual", SourceMetadata: []byte(`{}`), MediaReferences: []byte(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	first, err := queries.CreateProcessingAttempt(ctx, store.CreateProcessingAttemptParams{InboxItemID: retryItem.ID, IdempotencyKey: "operation-key", AgentName: "test", ModelProfile: "test", Actions: []byte(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.UpdateProcessingAttempt(ctx, store.UpdateProcessingAttemptParams{ID: first.ID, Status: "retryable", Actions: []byte(`[]`)}); err != nil {
		t.Fatal(err)
	}
	second, err := queries.CreateProcessingAttempt(ctx, store.CreateProcessingAttemptParams{InboxItemID: retryItem.ID, IdempotencyKey: "operation-key.retry.2", AgentName: "test", ModelProfile: "test", Actions: []byte(`[]`)})
	if err != nil || second.ID == first.ID {
		t.Fatalf("retry attempt = %#v, err = %v", second, err)
	}
	attempts, err := queries.ListProcessingAttempts(ctx, retryItem.ID)
	if err != nil || len(attempts) != 2 || attempts[1].Status != "retryable" {
		t.Fatalf("retry history = %#v, err = %v", attempts, err)
	}
}
func boolPointer(value bool) *bool { return &value }
