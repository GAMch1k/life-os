package repository

import (
	"testing"

	"github.com/GAMch1k/life-os/services/food/internal/testdb"
)

// TestMigrationGoesDownAndUp checks that 000002 is reversible: down drops
// every Food table, and up again recreates them with the built-in aisle
// rules.
func TestMigrationGoesDownAndUp(t *testing.T) {
	pool := testdb.Open(t, "")
	ctx := t.Context()
	var rules int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM aisle_rules WHERE created_by = 'system:food'").Scan(&rules); err != nil || rules < 150 {
		t.Fatalf("%d built-in rules, %v", rules, err)
	}
	testdb.Apply(t, pool, testdb.Migration(t, "000002", "down"))
	var tables int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema()").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("%d tables left after down, %v", tables, err)
	}
	testdb.Apply(t, pool, testdb.Migration(t, "000002", "up"))
	var again int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM aisle_rules").Scan(&again); err != nil || again != rules {
		t.Fatalf("%d rules after up, want %d, %v", again, rules, err)
	}
}
