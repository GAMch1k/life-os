package api

import (
	"net/http"
	"testing"

	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
)

func TestFoodChangesAreRecordedInActivity(t *testing.T) {
	c := newAPIClient(t)
	seen := 0
	cause := googleuuid.New()

	// The AI adds two items from a capture; the store is new.
	added := expect[generated.AddShoppingItemsResponse](c, http.MethodPost, "/v1/shopping-items", map[string]any{"items": []any{
		map[string]any{"name": "Vollmilch", "amount": "2 l", "storeName": "Lidl", "note": "private note", "source": "capture", "sourceRef": "inbox-9"},
		map[string]any{"name": "Brot"},
	}}, http.StatusOK, activity.ActorHeader, "agent:codex-inbox-processor", activity.CorrelationIDHeader, "corr-capture", activity.CausationIDHeader, cause.String()).Items
	events := c.recorder.since(&seen)
	if eventTypes(events) != storeCreated+" "+shoppingItemAdded+" "+shoppingItemAdded {
		t.Fatalf("events = %s", eventTypes(events))
	}
	milk := added[0].Item
	created := events[1]
	if created.Actor != activity.Agent("codex-inbox-processor") || created.CorrelationID != "corr-capture" || created.CausationID == nil || *created.CausationID != cause ||
		created.Source != "food" || created.Subject != (activity.Subject{Type: "food_shopping_item", ID: milk.Id.String()}) || created.Outcome != activity.Succeeded ||
		created.ID != activity.EventID(shoppingItemAdded, milk.Id.String()) {
		t.Fatalf("created = %+v", created)
	}
	if created.Summary != "Added “Vollmilch” (2 l, Lidl) to the shopping list" || created.Data["aisle"] != "dairy" || created.Data["storeName"] != "Lidl" ||
		created.Data["sourceRef"] != "inbox-9" || created.Data["note"] != nil {
		t.Fatalf("created summary/data = %q %v", created.Summary, created.Data)
	}
	if events[0].Summary != "Store “Lidl” created" || events[0].Subject.Type != "food_store" || events[0].Actor != activity.Agent("codex-inbox-processor") {
		t.Fatalf("store = %+v", events[0])
	}
	if events[2].Summary != "Added “Brot” to the shopping list" {
		t.Fatalf("brot = %q", events[2].Summary)
	}

	// The owner adds milk again in the Dashboard: a merge.
	expect[generated.AddShoppingItemsResponse](c, http.MethodPost, "/v1/shopping-items", map[string]any{"items": []any{map[string]any{"name": "vollmilch", "amount": "1 l"}}},
		http.StatusOK, activity.ActorHeader, "user")
	if events = c.recorder.since(&seen); eventTypes(events) != shoppingItemMerged || events[0].Summary != "“Vollmilch” was already on the shopping list: amount updated" ||
		events[0].Actor != activity.User() || events[0].Subject.ID != milk.Id.String() {
		t.Fatalf("merged = %+v", events)
	}
	// No-ops record nothing: an update that changes nothing, a second "bought".
	path := "/v1/shopping-items/" + milk.Id.String()
	expect[generated.ShoppingItem](c, http.MethodPatch, path, map[string]any{"amount": "1 l"}, http.StatusOK)
	if events = c.recorder.since(&seen); len(events) != 0 {
		t.Fatalf("a no-op update recorded %s", eventTypes(events))
	}
	expect[generated.ShoppingItem](c, http.MethodPatch, path, map[string]any{"name": "Milch", "note": nil, "aisle": "drinks"}, http.StatusOK)
	expect[generated.ShoppingItem](c, http.MethodPost, path+"/bought", nil, http.StatusOK)
	expect[generated.ShoppingItem](c, http.MethodPost, path+"/bought", nil, http.StatusOK)
	expect[generated.ShoppingItemResult](c, http.MethodPost, path+"/needed", nil, http.StatusOK)
	expect[generated.ShoppingItem](c, http.MethodPost, path+"/bought", nil, http.StatusOK)
	expect[generated.ClearBoughtResult](c, http.MethodPost, "/v1/shopping-items/clear-bought", nil, http.StatusOK)
	expect[generated.ClearBoughtResult](c, http.MethodPost, "/v1/shopping-items/clear-bought", nil, http.StatusOK)
	if status, _ := c.do(http.MethodDelete, "/v1/shopping-items/"+added[1].Item.Id.String(), nil); status != http.StatusNoContent {
		t.Fatal("delete failed")
	}
	events = c.recorder.since(&seen)
	want := shoppingItemUpdated + " " + shoppingItemBought + " " + shoppingItemReopened + " " + shoppingItemBought + " " + shoppingItemsCleared + " " + shoppingItemDeleted
	if eventTypes(events) != want {
		t.Fatalf("recorded %q\nwant     %q", eventTypes(events), want)
	}
	summaries := []string{
		"Shopping item “Milch” updated: name, note, aisle", "Bought “Milch”", "“Milch” is needed again", "Bought “Milch”",
		"Cleared 1 bought item from the shopping list", "Removed “Brot” from the shopping list",
	}
	for index, summary := range summaries {
		if events[index].Summary != summary || events[index].Actor != activity.Dev("test") {
			t.Errorf("event %d (%s) = %q by %+v, want %q", index, events[index].Type, events[index].Summary, events[index].Actor, summary)
		}
	}
	if events[1].ID == events[3].ID {
		t.Error("two purchases share an event id")
	}
	if changes, _ := events[0].Data["changes"].([]any); len(changes) != 3 {
		t.Errorf("changes = %v", events[0].Data["changes"])
	}

	// A reopen that merges names both items.
	results := c.add(map[string]any{"name": "Eggs"})
	eggs := results[0].Item.Id.String()
	expect[generated.ShoppingItem](c, http.MethodPost, "/v1/shopping-items/"+eggs+"/bought", nil, http.StatusOK)
	newer := c.add(map[string]any{"name": "eggs"})[0].Item
	c.recorder.since(&seen)
	expect[generated.ShoppingItemResult](c, http.MethodPost, "/v1/shopping-items/"+eggs+"/needed", nil, http.StatusOK)
	if events = c.recorder.since(&seen); eventTypes(events) != shoppingItemReopened || events[0].Subject.ID != newer.Id.String() || events[0].Data["mergedFromId"] != eggs ||
		events[0].Summary != "“eggs” is needed again, merged into the item already on the list" {
		t.Fatalf("merged reopen = %+v", events)
	}

	// Stores, aisle rules, and the pantry.
	stores := expect[generated.StoreList](c, http.MethodGet, "/v1/stores", nil, http.StatusOK).Items
	expect[generated.Store](c, http.MethodPatch, "/v1/stores/"+stores[0].Id.String(), map[string]any{"name": "Lidl Mitte", "sortOrder": 5}, http.StatusOK)
	expect[generated.Store](c, http.MethodPatch, "/v1/stores/"+stores[0].Id.String(), map[string]any{"archived": true}, http.StatusOK)
	expect[generated.Store](c, http.MethodPatch, "/v1/stores/"+stores[0].Id.String(), map[string]any{"archived": false}, http.StatusOK)
	rule := expect[generated.AisleRule](c, http.MethodPost, "/v1/aisle-rules", map[string]any{"keyword": "Kerze", "aisle": "household", "match": "substring"}, http.StatusCreated)
	if status, _ := c.do(http.MethodDelete, "/v1/aisle-rules/"+rule.Id.String(), nil); status != http.StatusNoContent {
		t.Fatal("rule delete failed")
	}
	pantry := expect[generated.AddPantryItemsResponse](c, http.MethodPost, "/v1/pantry-items", map[string]any{"items": []any{map[string]any{"name": "Joghurt", "expiresOn": "2026-10-14", "note": "secret"}}}, http.StatusCreated).Items[0]
	pantryPath := "/v1/pantry-items/" + pantry.Id.String()
	expect[generated.PantryItem](c, http.MethodPatch, pantryPath, map[string]any{"expiresOn": "2026-10-15", "openedOn": "2026-10-11"}, http.StatusOK)
	expect[generated.PantryItem](c, http.MethodPost, pantryPath+"/used-up", nil, http.StatusOK)
	expect[generated.PantryItem](c, http.MethodPost, pantryPath+"/thrown-away", nil, http.StatusOK)
	expect[generated.PantryItem](c, http.MethodPost, pantryPath+"/restock", nil, http.StatusOK)
	expect[generated.PantryItem](c, http.MethodPost, pantryPath+"/restock", nil, http.StatusOK)
	if status, _ := c.do(http.MethodDelete, pantryPath, nil); status != http.StatusNoContent {
		t.Fatal("pantry delete failed")
	}
	events = c.recorder.since(&seen)
	want = storeUpdated + " " + storeArchived + " " + storeRestored + " " + aisleRuleCreated + " " + aisleRuleDeleted + " " +
		pantryItemAdded + " " + pantryItemUpdated + " " + pantryItemUsedUp + " " + pantryItemThrownAway + " " + pantryItemRestocked + " " + pantryItemDeleted
	if eventTypes(events) != want {
		t.Fatalf("recorded %q\nwant     %q", eventTypes(events), want)
	}
	summaries = []string{
		"Store “Lidl Mitte” updated: name, sort order", "Store “Lidl Mitte” archived", "Store “Lidl Mitte” restored",
		"Aisle rule created: “kerze” → household (also inside words)", "Aisle rule deleted: “kerze” → household (also inside words)",
		"Added “Joghurt” to the pantry (fridge, expires 2026-10-14)", "Pantry item “Joghurt” updated: expiry date, opened date",
		"Used up “Joghurt”", "Threw away “Joghurt”", "“Joghurt” is back in stock", "Deleted pantry item “Joghurt”",
	}
	for index, summary := range summaries {
		if events[index].Summary != summary {
			t.Errorf("event %d (%s) summary = %q, want %q", index, events[index].Type, events[index].Summary, summary)
		}
	}
	for _, event := range events[5:] {
		if event.Subject != (activity.Subject{Type: "food_pantry_item", ID: pantry.Id.String()}) || event.Data["note"] != nil {
			t.Errorf("pantry event %s = %+v", event.Type, event)
		}
	}
}

func TestMergesInOneBatchAreEachRecorded(t *testing.T) {
	c := newAPIClient(t)
	c.add(map[string]any{"name": "Eggs"}, map[string]any{"name": "eggs", "amount": "6"}, map[string]any{"name": "EGGS", "amount": "10"})
	events := c.recorder.all()
	if eventTypes(events) != shoppingItemAdded+" "+shoppingItemMerged+" "+shoppingItemMerged || events[1].ID == events[2].ID {
		t.Fatalf("events = %+v", events)
	}
	if events[1].Summary != "“Eggs” was already on the shopping list: amount updated" || events[2].Data["amount"] != "10" {
		t.Fatalf("merges = %q %v", events[1].Summary, events[2].Data)
	}
}
