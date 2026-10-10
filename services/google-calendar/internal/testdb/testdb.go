// Package testdb gives the Google Calendar integration tests a private, fully
// migrated schema on the Postgres named by GOOGLE_CALENDAR_TEST_DATABASE_URL (a
// throwaway database, never a live one). Tests skip when it is unset.
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

// Open creates a fresh schema, applies every up migration in order, and
// drops the schema when the test ends.
func Open(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("GOOGLE_CALENDAR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GOOGLE_CALENDAR_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("google_calendar_test_%d_%d", time.Now().UnixNano(), sequence.Add(1))
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
	for _, path := range Migrations(t, "up") {
		Apply(t, pool, path)
	}
	return pool
}

// Migrations lists the migration files of one direction ("up" or "down"),
// up in order and down in reverse order.
func Migrations(t testing.TB, direction string) []string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations", "*."+direction+".sql"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("migrations: %v %v", paths, err)
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
