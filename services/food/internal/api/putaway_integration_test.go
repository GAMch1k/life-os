package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
)

// buy adds shopping items and marks them bought, returning them in order.
func (c apiClient) buy(items ...map[string]any) []generated.ShoppingItem {
	c.t.Helper()
	out := make([]generated.ShoppingItem, 0, len(items))
	for _, result := range c.add(items...) {
		out = append(out, expect[generated.ShoppingItem](c, http.MethodPost, "/v1/shopping-items/"+result.Item.Id.String()+"/bought", nil, http.StatusOK))
	}
	return out
}

func TestPutAwayMovesBoughtItemsIntoThePantry(t *testing.T) {
	c := newAPIClient(t)
	bought := c.buy(
		map[string]any{"name": "Vollmilch", "amount": "2 l", "note": "the blue one", "storeName": "Lidl"},
		map[string]any{"name": "Erbsen", "aisle": "frozen", "source": "capture", "sourceRef": "inbox-1"},
		map[string]any{"name": "Brot", "amount": "1"},
	)
	milk, peas, bread := bought[0], bought[1], bought[2]
	c.add(map[string]any{"name": "Eier"})
	seen := len(c.recorder.all())

	got := expect[generated.PutAwayResponse](c, http.MethodPost, "/v1/shopping-items/put-away", map[string]any{"items": []any{
		map[string]any{"id": milk.Id, "expiresOn": "2026-10-14"},
		map[string]any{"id": bread.Id, "location": "other", "amount": ""},
		map[string]any{"id": peas.Id, "amount": "500 g"},
	}}, http.StatusOK)
	if pantryNames(got.Items) != "Vollmilch, Brot, Erbsen" {
		t.Fatalf("put away %s", pantryNames(got.Items))
	}
	// The usual place of the aisle unless the request names one; the
	// shopping item's amount unless the request gives one (empty: none).
	want := []struct {
		location generated.PantryLocation
		amount   string
		expires  string
		from     string
	}{
		{generated.PantryLocationFridge, "2 l", "2026-10-14", milk.Id.String()},
		{generated.PantryLocationOther, "", "", bread.Id.String()},
		{generated.PantryLocationFreezer, "500 g", "", peas.Id.String()},
	}
	for index, item := range got.Items {
		amount, expires := "", ""
		if item.Amount != nil {
			amount = *item.Amount
		}
		if item.ExpiresOn != nil {
			expires = item.ExpiresOn.Format("2006-01-02")
		}
		if item.Location != want[index].location || amount != want[index].amount || expires != want[index].expires ||
			item.FromShoppingItemId == nil || item.FromShoppingItemId.String() != want[index].from || item.State != generated.InStock ||
			item.Note != nil || item.CreatedBy.Kind != generated.Dev {
			t.Errorf("item %d = %+v", index, item)
		}
	}
	// The source comes along: the peas came from a capture.
	if got.Items[2].Source != generated.ItemSourceCapture || got.Items[2].SourceRef == nil || *got.Items[2].SourceRef != "inbox-1" ||
		got.Items[0].Source != generated.ItemSourceManual {
		t.Fatalf("sources = %+v", got.Items)
	}
	// They left the list; the needed item stays.
	if names := names(c.list("").Items); names != "Eier" {
		t.Fatalf("list = %s", names)
	}
	if pantry := pantryNames(c.pantry("state=in_stock").Items); pantry != "Vollmilch, Brot, Erbsen" {
		t.Fatalf("pantry = %s", pantry)
	}
	// Two events per item, in the request's order.
	events := c.recorder.since(&seen)
	if eventTypes(events) != strings.TrimSpace(strings.Repeat(shoppingItemPutAway+" "+pantryItemAdded+" ", 3)) {
		t.Fatalf("events = %s", eventTypes(events))
	}
	putAway, added := events[0], events[1]
	if putAway.Summary != "Put away “Vollmilch” in the fridge" || putAway.Subject.ID != milk.Id.String() || putAway.Subject.Type != "food_shopping_item" ||
		putAway.Data["pantryItemId"] != got.Items[0].Id.String() || putAway.Data["location"] != "fridge" || putAway.Data["storeName"] != "Lidl" {
		t.Fatalf("put away event = %+v", putAway)
	}
	if added.Summary != "Added “Vollmilch” to the pantry (fridge, expires 2026-10-14)" || added.Data["fromShoppingItemId"] != milk.Id.String() {
		t.Fatalf("added event = %+v", added)
	}
	if events[2].Summary != "Put away “Brot”" {
		t.Fatalf("put away elsewhere = %q", events[2].Summary)
	}
	// A shopping item is put away once.
	c.expectError(http.MethodPost, "/v1/shopping-items/put-away", map[string]any{"items": []any{map[string]any{"id": milk.Id}}}, http.StatusNotFound, "not_found")
}

func TestPutAwayRefusesWhatItCannotPutAway(t *testing.T) {
	c := newAPIClient(t)
	bought := c.buy(map[string]any{"name": "Milch"})[0]
	needed := c.add(map[string]any{"name": "Brot"})[0].Item
	put := func(items ...map[string]any) map[string]any {
		list := make([]any, 0, len(items))
		for _, item := range items {
			list = append(list, item)
		}
		return map[string]any{"items": list}
	}
	// A needed item refuses the whole request, naming it.
	status, raw := c.do(http.MethodPost, "/v1/shopping-items/put-away", put(map[string]any{"id": bought.Id}, map[string]any{"id": needed.Id}))
	if status != http.StatusConflict || !strings.Contains(string(raw), `"not_bought"`) || !strings.Contains(string(raw), "items[1]: “Brot” is still needed") {
		t.Fatalf("needed: %d %s", status, raw)
	}
	if n := c.count("pantry_items", "true"); n != 0 {
		t.Fatalf("%d pantry items after a refusal", n)
	}
	if got := names(c.list("").Items); got != "Brot, Milch" {
		t.Fatalf("list after a refusal = %s", got)
	}
	c.expectError(http.MethodPost, "/v1/shopping-items/put-away", put(map[string]any{"id": "7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10"}), http.StatusNotFound, "not_found")
	c.expectError(http.MethodPost, "/v1/shopping-items/put-away", put(map[string]any{"id": bought.Id}, map[string]any{"id": bought.Id}), http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/shopping-items/put-away", put(), http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/shopping-items/put-away", put(map[string]any{"id": bought.Id, "location": "cellar"}), http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/shopping-items/put-away", put(map[string]any{"id": bought.Id, "amount": strings.Repeat("x", 51)}), http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/shopping-items/put-away", put(map[string]any{"id": bought.Id}), http.StatusBadRequest, "actor_required", "X-Life-OS-Actor", "")
	many := make([]map[string]any, 51)
	for index := range many {
		many[index] = map[string]any{"id": bought.Id}
	}
	c.expectError(http.MethodPost, "/v1/shopping-items/put-away", put(many...), http.StatusBadRequest, "invalid_request")
	if n := c.count("pantry_items", "true"); n != 0 {
		t.Fatalf("%d pantry items after refusals", n)
	}
}

func TestPutAwayReplaysWithItsIdempotencyKey(t *testing.T) {
	c := newAPIClient(t)
	bought := c.buy(map[string]any{"name": "Joghurt"}, map[string]any{"name": "Käse"})
	body := map[string]any{"items": []any{map[string]any{"id": bought[0].Id, "expiresOn": "2026-10-15"}}}
	status, first := c.do(http.MethodPost, "/v1/shopping-items/put-away", body, "Idempotency-Key", "put-away-1")
	if status != http.StatusOK {
		t.Fatalf("first: %d %s", status, first)
	}
	seen := len(c.recorder.all())
	// The replay answers the first response, byte for byte, and changes
	// nothing, though the item is no longer on the list.
	status, again := c.do(http.MethodPost, "/v1/shopping-items/put-away", body, "Idempotency-Key", "put-away-1")
	if status != http.StatusOK || string(again) != string(first) {
		t.Fatalf("replay: %d %s\nfirst: %s", status, again, first)
	}
	if events := c.recorder.since(&seen); len(events) != 0 {
		t.Fatalf("replay recorded %s", eventTypes(events))
	}
	if n := c.count("pantry_items", "true"); n != 1 {
		t.Fatalf("%d pantry items after a replay", n)
	}
	c.expectError(http.MethodPost, "/v1/shopping-items/put-away", map[string]any{"items": []any{map[string]any{"id": bought[1].Id}}},
		http.StatusConflict, "idempotency_conflict", "Idempotency-Key", "put-away-1")
	if got := names(c.list("").Items); got != "Käse" {
		t.Fatalf("list = %s", got)
	}
}
