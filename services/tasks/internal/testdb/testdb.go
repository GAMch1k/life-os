// Package testdb gives Tasks' integration tests a private, fully migrated
// schema on the Postgres named by TASKS_TEST_DATABASE_URL (a throwaway
// database, never a live one). Tests skip when it is unset.
package testdb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open creates a fresh schema, applies every up migration in order up to and
// including version upTo (such as "000003"; all when empty), and drops the
// schema when the test ends.
func Open(t *testing.T, upTo string) *pgxpool.Pool {
	t.Helper()
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
	// Test packages run in parallel against one database. Install pgcrypto
	// once, outside the test schemas, so the migrations' CREATE EXTENSION IF
	// NOT EXISTS neither races nor ties the extension to a schema a test drops.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, "SELECT pg_advisory_lock(4210001)")
	if err == nil {
		_, err = conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public")
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock(4210001)")
	}
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	for _, path := range Migrations(t) {
		if base := filepath.Base(path); upTo != "" && base[:len(upTo)] > upTo {
			break
		}
		Apply(t, pool, path)
	}
	return pool
}

// Migrations lists the up migrations in order.
func Migrations(t *testing.T) []string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations", "*.up.sql"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("migrations: %v %v", paths, err)
	}
	sort.Strings(paths)
	return paths
}

// Apply runs one migration file.
func Apply(t *testing.T, pool *pgxpool.Pool, path string) {
	t.Helper()
	migration, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), string(migration)); err != nil {
		t.Fatalf("%s: %v", filepath.Base(path), err)
	}
}
