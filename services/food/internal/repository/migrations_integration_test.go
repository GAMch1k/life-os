package repository

import (
	"testing"

	"github.com/GAMch1k/life-os/services/food/internal/domain"
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

// TestAisleRankFollowsTheAisles checks that the aisle_rank column orders the
// aisles as domain.Aisles lists them.
func TestAisleRankFollowsTheAisles(t *testing.T) {
	pool := testdb.Open(t, "")
	ctx := t.Context()
	for _, aisle := range domain.Aisles {
		var rank int
		err := pool.QueryRow(ctx, `INSERT INTO shopping_items (name, normalized_name, aisle, aisle_set_by, created_by, updated_by)
			VALUES ($1, $1, $1, 'manual', 'dev:test', 'dev:test') RETURNING aisle_rank`, aisle).Scan(&rank)
		if err != nil || rank != domain.AisleRank(aisle) {
			t.Errorf("%s: rank %d, want %d (%v)", aisle, rank, domain.AisleRank(aisle), err)
		}
	}
}
