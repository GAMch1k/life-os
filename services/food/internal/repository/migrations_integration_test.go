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
	// Later migrations go down first.
	testdb.Apply(t, pool, testdb.Migration(t, "000004", "down"))
	testdb.Apply(t, pool, testdb.Migration(t, "000003", "down"))
	testdb.Apply(t, pool, testdb.Migration(t, "000002", "down"))
	var tables int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema()").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("%d tables left after down, %v", tables, err)
	}
	testdb.Apply(t, pool, testdb.Migration(t, "000002", "up"))
	testdb.Apply(t, pool, testdb.Migration(t, "000003", "up"))
	testdb.Apply(t, pool, testdb.Migration(t, "000004", "up"))
	var again int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM aisle_rules").Scan(&again); err != nil || again != rules {
		t.Fatalf("%d rules after up, want %d, %v", again, rules, err)
	}
}

// TestRecipeMigrationGoesDownAndUp checks that 000004 is reversible: down
// drops the recipes and the meal plan and keeps items added from recipes as
// manual ones.
func TestRecipeMigrationGoesDownAndUp(t *testing.T) {
	pool := testdb.Open(t, "")
	ctx := t.Context()
	for _, sql := range []string{
		`INSERT INTO recipes (title, normalized_title, created_by, updated_by) VALUES ('Soup', 'soup', 'dev:test', 'dev:test')`,
		`INSERT INTO meal_plan_entries (day, slot, title, created_by, updated_by) VALUES ('2026-10-12', 'dinner', 'Soup', 'dev:test', 'dev:test')`,
		`INSERT INTO shopping_items (name, normalized_name, aisle, aisle_set_by, source, source_ref, created_by, updated_by)
			VALUES ('Leek', 'leek', 'produce', 'rule', 'recipe', 'r1', 'dev:test', 'dev:test')`,
		`INSERT INTO pantry_items (name, normalized_name, location, source, created_by, updated_by) VALUES ('Leek', 'leek', 'fridge', 'recipe', 'dev:test', 'dev:test')`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	testdb.Apply(t, pool, testdb.Migration(t, "000004", "down"))
	var tables, recipeSources int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name IN ('recipes', 'recipe_ingredients', 'meal_plan_entries')`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("%d recipe tables after down, %v", tables, err)
	}
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM shopping_items WHERE source = 'manual') + (SELECT count(*) FROM pantry_items WHERE source = 'manual')`).Scan(&recipeSources); err != nil || recipeSources != 2 {
		t.Fatalf("%d items kept as manual, %v", recipeSources, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE shopping_items SET source = 'recipe'`); err == nil {
		t.Fatal("source recipe accepted after down")
	}
	testdb.Apply(t, pool, testdb.Migration(t, "000004", "up"))
	if _, err := pool.Exec(ctx, `UPDATE shopping_items SET source = 'recipe'`); err != nil {
		t.Fatalf("source recipe after up: %v", err)
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
