package testdb

import (
	"context"
	"testing"
)

func TestMigrationsAreReversible(t *testing.T) {
	pool := Open(t)
	ctx := context.Background()
	tables := func() int {
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'feeds'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if tables() != 1 {
		t.Fatalf("%d tables after migrating up", tables())
	}
	for _, path := range Migrations(t, ServiceMigrations(), "down") {
		Apply(t, pool, path)
	}
	if tables() != 0 {
		t.Fatalf("%d tables after migrating down", tables())
	}
	for _, path := range Migrations(t, ServiceMigrations(), "up") {
		Apply(t, pool, path)
	}
	if tables() != 1 {
		t.Fatalf("%d tables after migrating up again", tables())
	}
}
