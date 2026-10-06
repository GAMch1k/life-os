package repository_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	store "github.com/GAMch1k/life-os/services/inbox/internal/repository/generated"
)

func TestInboxPersistenceLifecycle(t *testing.T) {
	url := os.Getenv("INBOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("INBOX_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("inbox_test_%d", time.Now().UnixNano())
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
	migration, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", "000002_inbox_items.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	queries := store.New(pool)
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
}
func boolPointer(value bool) *bool { return &value }
