// Package testdb gives Food's integration tests a private, migrated schema on
// the Postgres named by FOOD_TEST_DATABASE_URL (a throwaway database, never a
// live one). Tests skip when it is unset.
package testdb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var counter atomic.Int64

// Open creates a fresh schema, applies every up migration in order up to and
// including version upTo (such as "000002"; all when empty), and drops the
// schema when the test ends.
func Open(t *testing.T, upTo string) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("FOOD_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FOOD_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("food_test_%d_%d", time.Now().UnixNano(), counter.Add(1))
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	for _, path := range Migrations(t, "up") {
		if base := filepath.Base(path); upTo != "" && base[:len(upTo)] > upTo {
			break
		}
		Apply(t, pool, path)
	}
	return pool
}

// Migrations lists the up (or down) migrations in ascending order.
func Migrations(t *testing.T, direction string) []string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations", "*."+direction+".sql"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("migrations: %v %v", paths, err)
	}
	sort.Strings(paths)
	return paths
}

// Migration is the path of one migration, such as Migration(t, "000002", "down").
func Migration(t *testing.T, version, direction string) string {
	t.Helper()
	for _, path := range Migrations(t, direction) {
		if strings.HasPrefix(filepath.Base(path), version+"_") {
			return path
		}
	}
	t.Fatalf("no %s migration %s", direction, version)
	return ""
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
