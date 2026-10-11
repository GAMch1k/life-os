package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
)

func (c apiClient) staple(body map[string]any) generated.Staple {
	c.t.Helper()
	return expect[generated.Staple](c, http.MethodPost, "/v1/staples", body, http.StatusCreated)
}

func (c apiClient) stock(items ...map[string]any) []generated.PantryItem {
	c.t.Helper()
	return expect[generated.AddPantryItemsResponse](c, http.MethodPost, "/v1/pantry-items", map[string]any{"items": items}, http.StatusCreated).Items
}

func (c apiClient) pantryAction(item generated.PantryItem, action string, body any) generated.PantryItemResult {
	c.t.Helper()
	return expect[generated.PantryItemResult](c, http.MethodPost, "/v1/pantry-items/"+item.Id.String()+"/"+action, body, http.StatusOK)
}

func (c apiClient) runningLow(body map[string]any) generated.RunningLowResult {
	c.t.Helper()
	return expect[generated.RunningLowResult](c, http.MethodPost, "/v1/staples/running-low", body, http.StatusOK)
}

func stapleNames(items []generated.Staple) string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Name)
	}
	return strings.Join(out, ", ")
}

func TestStaplesAreCreatedChangedAndDeleted(t *testing.T) {
	c := newAPIClient(t)
	seen := 0
	milk := c.staple(map[string]any{"name": " Milch ", "amount": "2 l", "storeName": "Aldi"})
	if milk.Name != "Milch" || milk.NormalizedName != "milch" || *milk.Amount != "2 l" || *milk.StoreName != "Aldi" || milk.StoreId == nil ||
		milk.Aisle != nil || milk.OnList || milk.InStock || milk.RunningLow || milk.CreatedBy.Kind != generated.Dev {
		t.Fatalf("milk = %+v", milk)
	}
	butter := c.staple(map[string]any{"name": "Butter", "aisle": "dairy"})
	if *butter.Aisle != "dairy" || butter.StoreId != nil {
		t.Fatalf("butter = %+v", butter)
	}
	// One staple per normalised name.
	c.expectError(http.MethodPost, "/v1/staples", map[string]any{"name": "MILCH"}, http.StatusConflict, "staple_exists")
	c.expectError(http.MethodPost, "/v1/staples", map[string]any{"name": "  "}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/staples", map[string]any{"name": "Eier", "aisle": "cellar"}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/staples", map[string]any{"name": "Eier", "storeId": milk.StoreId, "storeName": "Aldi"}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/staples", map[string]any{"name": "Eier", "amount": strings.Repeat("x", 51)}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/staples", map[string]any{"name": "Eier"}, http.StatusBadRequest, "actor_required", "X-Life-OS-Actor", "")
	if got := stapleNames(expect[generated.StapleList](c, http.MethodGet, "/v1/staples", nil, http.StatusOK).Items); got != "Butter, Milch" {
		t.Fatalf("staples = %s", got)
	}

	path := "/v1/staples/" + milk.Id.String()
	changed := expect[generated.Staple](c, http.MethodPatch, path, map[string]any{"amount": nil, "storeId": nil, "aisle": "dairy"}, http.StatusOK)
	if changed.Amount != nil || changed.StoreId != nil || changed.StoreName != nil || *changed.Aisle != "dairy" {
		t.Fatalf("changed = %+v", changed)
	}
	// A change that changes nothing records nothing.
	expect[generated.Staple](c, http.MethodPatch, path, map[string]any{"aisle": "dairy"}, http.StatusOK)
	c.expectError(http.MethodPatch, "/v1/staples/"+butter.Id.String(), map[string]any{"name": "milch"}, http.StatusConflict, "staple_exists")
	c.expectError(http.MethodPatch, path, map[string]any{}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPatch, path, map[string]any{"name": nil}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPatch, path, map[string]any{"aisle": "cellar"}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPatch, "/v1/staples/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10", map[string]any{"amount": "1 l"}, http.StatusNotFound, "not_found")
	if got := expect[generated.Staple](c, http.MethodGet, path, nil, http.StatusOK); got.Id != milk.Id {
		t.Fatalf("get = %+v", got)
	}
	expect[any](c, http.MethodDelete, path, nil, http.StatusNoContent)
	c.expectError(http.MethodGet, path, nil, http.StatusNotFound, "not_found")
	c.expectError(http.MethodDelete, path, nil, http.StatusNotFound, "not_found")

	events := c.recorder.since(&seen)
	if got := eventTypes(events); got != "food.store.created food.staple.created food.staple.created food.staple.updated food.staple.deleted" {
		t.Fatalf("events = %s", got)
	}
	summaries := []string{"“Milch” is now a staple", "“Butter” is now a staple", "Staple “Milch” updated: amount, store, aisle", "“Milch” is no longer a staple"}
	for index, want := range summaries {
		if event := events[index+1]; event.Summary != want || event.Subject.Type != "food_staple" {
			t.Errorf("event %d = %q on %s, want %q", index, event.Summary, event.Subject.Type, want)
		}
	}
}

func TestAStapleGoesOnTheListWhenRunningLowUsedUpOrThrownAway(t *testing.T) {
	c := newAPIClient(t)
	milk := c.staple(map[string]any{"name": "Milch", "amount": "2 l", "storeName": "Aldi"})
	seen := len(c.recorder.all())
	first := c.stock(map[string]any{"name": "milch", "expiresOn": "2026-10-14"})[0]
	c.recorder.since(&seen)

	// Running low: flagged, and the staple goes on the list as the staple.
	low := c.pantryAction(first, "running-low", nil)
	if !low.RunningLow || low.RunningLowAt == nil || low.Restock == nil {
		t.Fatalf("low = %+v", low)
	}
	restock := *low.Restock
	item := restock.ShoppingItem
	if restock.Status != generated.RestockStatusAdded || restock.Reason != generated.RestockReasonRunningLow || *restock.StapleId != milk.Id ||
		restock.Message != "Added “Milch” to the list: running low (staple)" ||
		item.Name != "Milch" || *item.Amount != "2 l" || *item.StoreName != "Aldi" || item.Source != generated.ItemSourceStaple ||
		*item.SourceRef != milk.Id.String() || item.State != generated.Needed {
		t.Fatalf("restock = %+v", restock)
	}
	events := c.recorder.since(&seen)
	if got := eventTypes(events); got != "food.pantry_item.running_low food.shopping_item.added" {
		t.Fatalf("events = %s", got)
	}
	if events[0].Summary != "“milch” is running low" || events[1].Summary != restock.Message ||
		events[1].Data["reason"] != "running_low" || events[1].Data["stapleId"] != milk.Id.String() {
		t.Fatalf("events = %+v", events)
	}
	if got := pantryNames(c.pantry("runningLow=true").Items); got != "milch" {
		t.Fatalf("running low = %s", got)
	}
	if got := pantryNames(c.pantry("runningLow=false").Items); got != "" {
		t.Fatalf("not running low = %s", got)
	}
	staples := expect[generated.StapleList](c, http.MethodGet, "/v1/staples", nil, http.StatusOK).Items
	if !staples[0].OnList || !staples[0].InStock || !staples[0].RunningLow {
		t.Fatalf("staple = %+v", staples[0])
	}

	// Again: the flag stays, and the list keeps one Milch.
	again := c.pantryAction(first, "running-low", nil)
	if *again.RunningLowAt != *low.RunningLowAt || again.Restock.Status != generated.RestockStatusOnList ||
		again.Restock.Message != "“Milch” is already on the list" || again.Restock.ShoppingItem.Id != item.Id {
		t.Fatalf("again = %+v", again)
	}
	if got := eventTypes(c.recorder.since(&seen)); got != "" {
		t.Fatalf("again recorded %s", got)
	}

	// Used up while on the list: nothing more goes on it; the flag goes.
	used := c.pantryAction(first, "used-up", nil)
	if used.State != generated.PantryItemStateUsedUp || used.RunningLow || used.Restock.Status != generated.RestockStatusOnList ||
		used.Restock.Reason != generated.RestockReasonUsedUp {
		t.Fatalf("used = %+v", used)
	}
	if n := c.count("shopping_items", "normalized_name = 'milch'"); n != 1 {
		t.Fatalf("%d Milch on the list", n)
	}
	if got := eventTypes(c.recorder.since(&seen)); got != "food.pantry_item.used_up" {
		t.Fatalf("used up recorded %s", got)
	}
	// Used up again changes nothing.
	if repeat := c.pantryAction(first, "used-up", nil); repeat.Restock != nil {
		t.Fatalf("repeat = %+v", repeat)
	}

	// Bought, then the next one is thrown away: Milch goes on the list again.
	expect[generated.ShoppingItem](c, http.MethodPost, "/v1/shopping-items/"+item.Id.String()+"/bought", nil, http.StatusOK)
	second := c.stock(map[string]any{"name": "Milch"})[0]
	c.recorder.since(&seen)
	thrown := c.pantryAction(second, "thrown-away", nil)
	if thrown.Restock.Status != generated.RestockStatusAdded || thrown.Restock.ShoppingItem.Id == item.Id ||
		thrown.Restock.Message != "Added “Milch” to the list: thrown away (staple)" {
		t.Fatalf("thrown = %+v", thrown.Restock)
	}
	if got := eventTypes(c.recorder.since(&seen)); got != "food.pantry_item.thrown_away food.shopping_item.added" {
		t.Fatalf("thrown away recorded %s", got)
	}
	if n := c.count("shopping_items", "normalized_name = 'milch' AND state = 'needed'"); n != 1 {
		t.Fatalf("%d Milch needed", n)
	}

	// Back in stock: the flag of a restocked item starts cleared.
	third := c.stock(map[string]any{"name": "Milch"})[0]
	c.pantryAction(third, "running-low", nil)
	c.pantryAction(third, "used-up", nil)
	if back := c.pantryAction(third, "restock", nil); back.RunningLow || back.Restock != nil {
		t.Fatalf("back = %+v", back)
	}
}

func TestAStapleOnTheListAlreadyIsLeftAlone(t *testing.T) {
	c := newAPIClient(t)
	c.staple(map[string]any{"name": "Milch", "amount": "2 l"})
	needed := c.add(map[string]any{"name": "Milch", "amount": "1 l", "note": "the oat one"})[0].Item
	milk := c.stock(map[string]any{"name": "Milch"})[0]
	used := c.pantryAction(milk, "used-up", nil)
	if used.Restock.Status != generated.RestockStatusOnList || used.Restock.ShoppingItem.Id != needed.Id {
		t.Fatalf("used = %+v", used.Restock)
	}
	list := c.list("").Items
	if len(list) != 1 || *list[0].Amount != "1 l" || *list[0].Note != "the oat one" || list[0].Source != generated.ItemSourceManual {
		t.Fatalf("list = %+v", list)
	}
}

func TestAStapleKeepsItsAisleAndLeavesAnArchivedStoreOut(t *testing.T) {
	c := newAPIClient(t)
	soap := c.staple(map[string]any{"name": "Seife", "aisle": "drugstore", "storeName": "dm"})
	expect[generated.Store](c, http.MethodPatch, "/v1/stores/"+soap.StoreId.String(), map[string]any{"archived": true}, http.StatusOK)
	added := c.runningLow(map[string]any{"name": "seife"})
	item := added.Restock.ShoppingItem
	if item.Aisle != generated.AisleDrugstore || item.StoreId != nil || item.Source != generated.ItemSourceStaple {
		t.Fatalf("item = %+v", item)
	}
	if added.Staple == nil || added.Staple.StoreName == nil || *added.Staple.StoreName != "dm" || !added.Staple.OnList {
		t.Fatalf("staple = %+v", added.Staple)
	}
}

func TestRunningLowOfAnotherItemOnlyFlagsIt(t *testing.T) {
	c := newAPIClient(t)
	stocked := c.stock(map[string]any{"name": "Brot"}, map[string]any{"name": "Käse"}, map[string]any{"name": "Senf"})
	bread, cheese, mustard := stocked[0], stocked[1], stocked[2]
	seen := len(c.recorder.all())

	if low := c.pantryAction(bread, "running-low", map[string]any{}); !low.RunningLow || low.Restock != nil {
		t.Fatalf("low = %+v", low)
	}
	if used := c.pantryAction(bread, "used-up", nil); used.Restock != nil {
		t.Fatalf("used = %+v", used)
	}
	if n := c.count("shopping_items", "true"); n != 0 {
		t.Fatalf("%d items on the list", n)
	}
	if got := eventTypes(c.recorder.since(&seen)); got != "food.pantry_item.running_low food.pantry_item.used_up" {
		t.Fatalf("events = %s", got)
	}

	// addToList puts it on the list as a manual item.
	low := c.pantryAction(cheese, "running-low", map[string]any{"addToList": true})
	if low.Restock.Status != generated.RestockStatusAdded || low.Restock.StapleId != nil || low.Restock.ShoppingItem.Source != generated.ItemSourceManual ||
		low.Restock.Message != "Added “Käse” to the list: running low" {
		t.Fatalf("low = %+v", low.Restock)
	}
	if got := eventTypes(c.recorder.since(&seen)); got != "food.pantry_item.running_low food.shopping_item.added" {
		t.Fatalf("events = %s", got)
	}

	// Clearing the flag leaves the list alone; clearing it again records nothing.
	cleared := expect[generated.PantryItemResult](c, http.MethodDelete, "/v1/pantry-items/"+cheese.Id.String()+"/running-low", nil, http.StatusOK)
	if cleared.RunningLow || cleared.RunningLowAt != nil {
		t.Fatalf("cleared = %+v", cleared)
	}
	expect[generated.PantryItemResult](c, http.MethodDelete, "/v1/pantry-items/"+cheese.Id.String()+"/running-low", nil, http.StatusOK)
	if got := eventTypes(c.recorder.since(&seen)); got != "food.pantry_item.running_low_cleared" {
		t.Fatalf("events = %s", got)
	}
	if n := c.count("shopping_items", "normalized_name = 'käse'"); n != 1 {
		t.Fatalf("%d Käse on the list", n)
	}

	c.expectError(http.MethodPost, "/v1/pantry-items/"+bread.Id.String()+"/running-low", nil, http.StatusConflict, "not_in_stock")
	c.expectError(http.MethodPost, "/v1/pantry-items/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10/running-low", nil, http.StatusNotFound, "not_found")
	c.expectError(http.MethodDelete, "/v1/pantry-items/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10/running-low", nil, http.StatusNotFound, "not_found")
	c.expectError(http.MethodPost, "/v1/pantry-items/"+mustard.Id.String()+"/running-low", "[]", http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/pantry-items/"+mustard.Id.String()+"/running-low", nil, http.StatusBadRequest, "actor_required", "X-Life-OS-Actor", "")
}

func TestRunningLowByName(t *testing.T) {
	c := newAPIClient(t)
	// Neither a staple nor at home: nothing happens.
	nothing := c.runningLow(map[string]any{"name": "Eier"})
	if nothing.Changed || nothing.Restock != nil || nothing.Staple != nil || len(nothing.PantryItems) != 0 || nothing.NormalizedName != "eier" {
		t.Fatalf("nothing = %+v", nothing)
	}

	// A staple without a pantry row goes on the list.
	eggs := c.staple(map[string]any{"name": "Eier", "amount": "10"})
	added := c.runningLow(map[string]any{"name": "eier"})
	if !added.Changed || added.Restock.Status != generated.RestockStatusAdded || *added.Restock.StapleId != eggs.Id ||
		added.Staple == nil || !added.Staple.OnList || *added.Restock.ShoppingItem.Amount != "10" {
		t.Fatalf("added = %+v", added)
	}
	if again := c.runningLow(map[string]any{"name": "Eier"}); again.Changed || again.Restock.Status != generated.RestockStatusOnList {
		t.Fatalf("again = %+v", again)
	}

	// Another name at home: every in-stock item is flagged, nothing listed.
	c.stock(map[string]any{"name": "Joghurt", "expiresOn": "2026-10-20"}, map[string]any{"name": "joghurt", "expiresOn": "2026-10-13"})
	flagged := c.runningLow(map[string]any{"name": "Joghurt"})
	if !flagged.Changed || flagged.Restock != nil || len(flagged.PantryItems) != 2 || !flagged.PantryItems[0].RunningLow ||
		flagged.PantryItems[0].ExpiresOn.Format("2006-01-02") != "2026-10-13" {
		t.Fatalf("flagged = %+v", flagged)
	}
	if again := c.runningLow(map[string]any{"name": "Joghurt"}); again.Changed {
		t.Fatalf("again = %+v", again)
	}
	listed := c.runningLow(map[string]any{"name": "Joghurt", "addToList": true})
	if !listed.Changed || listed.Restock.Status != generated.RestockStatusAdded || listed.Restock.ShoppingItem.Source != generated.ItemSourceManual {
		t.Fatalf("listed = %+v", listed)
	}
	c.expectError(http.MethodPost, "/v1/staples/running-low", map[string]any{"name": " "}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/staples/running-low", map[string]any{}, http.StatusBadRequest, "invalid_request")
}

func TestMoreAtHomeClearsRunningLow(t *testing.T) {
	c := newAPIClient(t)
	c.staple(map[string]any{"name": "Milch"})
	old := c.stock(map[string]any{"name": "Milch"})[0]
	listed := c.pantryAction(old, "running-low", nil).Restock.ShoppingItem
	seen := len(c.recorder.all())

	// The listed Milch is bought and put away: the old one is no longer low.
	expect[generated.ShoppingItem](c, http.MethodPost, "/v1/shopping-items/"+listed.Id.String()+"/bought", nil, http.StatusOK)
	put := expect[generated.PutAwayResponse](c, http.MethodPost, "/v1/shopping-items/put-away", map[string]any{"items": []any{map[string]any{"id": listed.Id}}}, http.StatusOK)
	if put.Items[0].Source != generated.ItemSourceStaple || put.Items[0].RunningLow {
		t.Fatalf("put away = %+v", put.Items[0])
	}
	events := c.recorder.since(&seen)
	if got := eventTypes(events); got != "food.shopping_item.bought food.shopping_item.put_away food.pantry_item.added food.pantry_item.running_low_cleared" {
		t.Fatalf("events = %s", got)
	}
	if last := events[len(events)-1]; last.Summary != "“Milch” is no longer running low: more came home" || last.Subject.ID != old.Id.String() {
		t.Fatalf("cleared = %+v", last)
	}
	if got := pantryNames(c.pantry("runningLow=true").Items); got != "" {
		t.Fatalf("running low = %s", got)
	}

	// Adding to the pantry directly clears it too.
	c.pantryAction(old, "running-low", nil)
	c.stock(map[string]any{"name": "MILCH"})
	if got := pantryNames(c.pantry("runningLow=true").Items); got != "" {
		t.Fatalf("running low = %s", got)
	}
}

func TestRunningLowReplaysWithItsIdempotencyKey(t *testing.T) {
	c := newAPIClient(t)
	c.staple(map[string]any{"name": "Kaffee"})
	coffee := c.stock(map[string]any{"name": "Kaffee"})[0]
	seen := len(c.recorder.all())
	path := "/v1/pantry-items/" + coffee.Id.String() + "/running-low"
	status, first := c.do(http.MethodPost, path, nil, "Idempotency-Key", "low-1")
	if status != http.StatusOK {
		t.Fatalf("first: %d %s", status, first)
	}
	// Bought meanwhile, so a fresh request would list it again.
	c.do(http.MethodPost, "/v1/shopping-items/"+expect[generated.PantryItemResult](c, http.MethodPost, path, nil, http.StatusOK).Restock.ShoppingItem.Id.String()+"/bought", nil)
	status, second := c.do(http.MethodPost, path, nil, "Idempotency-Key", "low-1")
	if status != http.StatusOK || string(second) != string(first) {
		t.Fatalf("replay: %d %s, want %s", status, second, first)
	}
	if n := c.count("shopping_items", "normalized_name = 'kaffee'"); n != 1 {
		t.Fatalf("%d Kaffee on the list", n)
	}
	if got := eventTypes(c.recorder.since(&seen)); got != "food.pantry_item.running_low food.shopping_item.added food.shopping_item.bought" {
		t.Fatalf("events = %s", got)
	}
	status, by := c.do(http.MethodPost, "/v1/staples/running-low", map[string]any{"name": "Kaffee"}, "Idempotency-Key", "low-2")
	status2, byAgain := c.do(http.MethodPost, "/v1/staples/running-low", map[string]any{"name": "Kaffee"}, "Idempotency-Key", "low-2")
	if status != http.StatusOK || status2 != http.StatusOK || string(by) != string(byAgain) {
		t.Fatalf("by name: %d %s then %d %s", status, by, status2, byAgain)
	}
	if n := c.count("shopping_items", "normalized_name = 'kaffee' AND state = 'needed'"); n != 1 {
		t.Fatalf("%d Kaffee needed", n)
	}
}
