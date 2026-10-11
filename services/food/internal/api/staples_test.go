package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

func TestReasonText(t *testing.T) {
	for reason, want := range map[generated.RestockReason]string{
		generated.RestockReasonRunningLow: "running low",
		generated.RestockReasonUsedUp:     "used up",
		generated.RestockReasonThrownAway: "thrown away",
	} {
		if got := reasonText(reason); got != want {
			t.Errorf("%s: %q, want %q", reason, got, want)
		}
	}
}

// TestPantryResultIsThePantryItem checks that a lifecycle result says
// everything a pantry item says, so a field added to PantryItem is not
// forgotten in PantryItemResult.
func TestPantryResultIsThePantryItem(t *testing.T) {
	at := pgtype.Timestamptz{Time: time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC), Valid: true}
	day := pgtype.Date{Time: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC), Valid: true}
	item := store.PantryItem{
		ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, Name: "Milch", NormalizedName: "milch", Amount: pointer("1 l"), Location: "fridge",
		ExpiresOn: day, OpenedOn: day, Note: pointer("oat"), State: "in_stock", Source: "staple", SourceRef: pointer("s1"),
		FromShoppingItemID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, RunningLowAt: at,
		CreatedAt: at, UpdatedAt: at, CreatedBy: "user:owner", UpdatedBy: "agent:life-os",
	}
	view, err := json.Marshal(pantryView(item))
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(pantryResult(item, nil))
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	if err := json.Unmarshal(view, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("result has %d fields, the item %d: %s vs %s", len(got), len(want), result, view)
	}
	for key, value := range want {
		if a, b := toJSON(t, value), toJSON(t, got[key]); a != b {
			t.Errorf("%s: %s, want %s", key, b, a)
		}
	}
	if want["runningLow"] != true {
		t.Errorf("runningLow = %v", want["runningLow"])
	}
	with, err := json.Marshal(pantryResult(item, &generated.Restock{Status: generated.RestockStatusAdded}))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(with, &got); err != nil || got["restock"] == nil {
		t.Fatalf("restock missing: %s %v", with, err)
	}
}

func toJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
