// Package testdb gives the calendar-feeds integration tests a private, fully
// migrated schema on the Postgres named by CALENDAR_FEEDS_TEST_DATABASE_URL
// (a throwaway database, never a live one). Tests skip when it is unset.
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

var sequence atomic.Int64

// URL is the throwaway database's URL; the test is skipped without one.
func URL(t testing.TB) string {
	t.Helper()
	url := os.Getenv("CALENDAR_FEEDS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CALENDAR_FEEDS_TEST_DATABASE_URL is not set")
	}
	return url
}

// Schema creates a fresh schema and returns a pool whose search_path is it;
// the schema is dropped when the test ends.
func Schema(t testing.TB, prefix string) *pgxpool.Pool {
	t.Helper()
	url := URL(t)
	ctx := context.Background()
	schema := fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), sequence.Add(1))
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	return pool
}

// Open creates a fresh schema with every up migration of calendar-feeds.
func Open(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := Schema(t, "calendar_feeds_test")
	for _, path := range Migrations(t, ServiceMigrations(), "up") {
		Apply(t, pool, path)
	}
	return pool
}

func here() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}

// ServiceMigrations is calendar-feeds' migrations directory.
func ServiceMigrations() string {
	return filepath.Join(here(), "..", "..", "db", "migrations")
}

// Migrations lists the migration files of one direction ("up" or "down") in
// dir, up in order and down in reverse order.
func Migrations(t testing.TB, dir, direction string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*."+direction+".sql"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("migrations in %s: %v %v", dir, paths, err)
	}
	sort.Strings(paths)
	if strings.EqualFold(direction, "down") {
		for i, j := 0, len(paths)-1; i < j; i, j = i+1, j-1 {
			paths[i], paths[j] = paths[j], paths[i]
		}
	}
	return paths
}

// Apply runs one migration file.
func Apply(t testing.TB, pool *pgxpool.Pool, path string) {
	t.Helper()
	migration, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), string(migration)); err != nil {
		t.Fatalf("%s: %v", filepath.Base(path), err)
	}
}
