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
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name LIKE 'google_%'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if tables() != 9 {
		t.Fatalf("%d tables after migrating up", tables())
	}
	for _, path := range Migrations(t, "down") {
		Apply(t, pool, path)
	}
	if tables() != 0 {
		t.Fatalf("%d tables after migrating down", tables())
	}
	for _, path := range Migrations(t, "up") {
		Apply(t, pool, path)
	}
	if tables() != 9 {
		t.Fatalf("%d tables after migrating up again", tables())
	}
}
