package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
)

func TestAddPlacesItemsAndMergesOpenOnes(t *testing.T) {
	c := newAPIClient(t)
	results := c.add(
		map[string]any{"name": "Vollmilch", "amount": "1 l"},
		map[string]any{"name": "  Молоко "},
		map[string]any{"name": "Kerzen"},
		map[string]any{"name": "Brot", "aisle": "pantry", "source": "capture", "sourceRef": "inbox-1"},
	)
	if len(results) != 4 {
		t.Fatalf("results = %+v", results)
	}
	milk := results[0].Item
	if results[0].Merged || milk.Name != "Vollmilch" || milk.NormalizedName != "vollmilch" || milk.Aisle != generated.AisleDairy ||
		milk.AisleSetBy != generated.AisleSetByRule || *milk.Amount != "1 l" || milk.State != generated.Needed || milk.BoughtAt != nil ||
		milk.Source != generated.ItemSourceManual || milk.CreatedBy.Kind != generated.Dev || *milk.CreatedBy.Name != "test" {
		t.Fatalf("milk = %+v", milk)
	}
	if results[1].Item.Name != "Молоко" || results[1].Item.Aisle != generated.AisleDairy {
		t.Fatalf("молоко = %+v", results[1].Item)
	}
	if results[2].Item.Aisle != generated.AisleOther {
		t.Fatalf("no rule matches Kerzen: %+v", results[2].Item)
	}
	bread := results[3].Item
	if bread.Aisle != generated.AislePantry || bread.AisleSetBy != generated.AisleSetByManual || bread.Source != generated.ItemSourceCapture || *bread.SourceRef != "inbox-1" {
		t.Fatalf("an explicit aisle wins: %+v", bread)
	}

	// "молоко" is the open "Молоко": the given amount and store replace its
	// own, the name stays as first written, nothing else changes.
	again := c.add(map[string]any{"name": "молоко!", "amount": "2 l", "storeName": "Lidl"})[0]
	if !again.Merged || again.Item.Id != results[1].Item.Id || again.Item.Name != "Молоко" || *again.Item.Amount != "2 l" ||
		again.Item.StoreName == nil || *again.Item.StoreName != "Lidl" || again.Item.Aisle != generated.AisleDairy {
		t.Fatalf("merge = %+v", again)
	}
	// What a merge does not give stays; an explicit aisle replaces the rule's.
	kept := c.add(map[string]any{"name": "МОЛОКО", "aisle": "drinks"})[0].Item
	if *kept.Amount != "2 l" || kept.StoreId == nil || kept.Aisle != generated.AisleDrinks || kept.AisleSetBy != generated.AisleSetByManual {
		t.Fatalf("kept = %+v", kept)
	}
	// Two items of one request with one name merge too, in request order.
	pair := c.add(map[string]any{"name": "Eggs", "amount": "6"}, map[string]any{"name": "eggs", "amount": "10"})
	if pair[0].Merged || !pair[1].Merged || pair[0].Item.Id != pair[1].Item.Id || *pair[1].Item.Amount != "10" {
		t.Fatalf("pair = %+v", pair)
	}
	if n := c.count("shopping_items", "normalized_name = 'молоко'"); n != 1 {
		t.Fatalf("%d rows for молоко", n)
	}
	// A bought item does not take merges: the name is needed again as a new item.
	expect[generated.ShoppingItem](c, http.MethodPost, "/v1/shopping-items/"+milk.Id.String()+"/bought", nil, http.StatusOK)
	fresh := c.add(map[string]any{"name": "Vollmilch"})[0]
	if fresh.Merged || fresh.Item.Id == milk.Id {
		t.Fatalf("fresh = %+v", fresh)
	}

	// Invalid items refuse the whole request.
	for _, body := range []map[string]any{
		{"items": []any{}},
		{"items": []any{map[string]any{"name": "ok"}, map[string]any{"name": "  "}}},
		{"items": []any{map[string]any{"name": "!!!"}}},
		{"items": []any{map[string]any{"name": "x", "aisle": "garden"}}},
		{"items": []any{map[string]any{"name": "x", "source": "email"}}},
		{"items": []any{map[string]any{"name": "x", "storeName": "Lidl", "storeId": milk.Id}}},
	} {
		c.expectError(http.MethodPost, "/v1/shopping-items", body, http.StatusBadRequest, "invalid_request")
	}
	c.expectError(http.MethodPost, "/v1/shopping-items", map[string]any{"items": []any{map[string]any{"name": "x", "storeId": milk.Id}}}, http.StatusBadRequest, "unknown_store")
	if n := c.count("shopping_items", "true"); n != 6 {
		t.Fatalf("%d items after refusals", n)
	}
}

func TestConcurrentAddsOfOneNameLeaveOneItem(t *testing.T) {
	c := newAPIClient(t)
	const adders = 8
	var wait sync.WaitGroup
	statuses := make([]int, adders)
	merged := 0
	var mu sync.Mutex
	for index := range adders {
		wait.Go(func() {
			status, raw := c.do(http.MethodPost, "/v1/shopping-items", map[string]any{"items": []any{
				map[string]any{"name": "Butter", "amount": fmt.Sprint(index + 1)},
				map[string]any{"name": "Äpfel"},
			}})
			statuses[index] = status
			if status == http.StatusOK && len(raw) > 0 {
				var response generated.AddShoppingItemsResponse
				if err := json.Unmarshal(raw, &response); err == nil && response.Items[0].Merged {
					mu.Lock()
					merged++
					mu.Unlock()
				}
			}
		})
	}
	wait.Wait()
	for index, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("adder %d: %d", index, status)
		}
	}
	if n := c.count("shopping_items", "normalized_name = 'butter'"); n != 1 {
		t.Fatalf("%d butter rows", n)
	}
	if n := c.count("shopping_items", "normalized_name = 'äpfel'"); n != 1 {
		t.Fatalf("%d äpfel rows", n)
	}
	if merged != adders-1 {
		t.Fatalf("%d of %d adds merged", merged, adders)
	}
}

func TestStoreByNameIsCreatedOnce(t *testing.T) {
	c := newAPIClient(t)
	results := c.add(map[string]any{"name": "Milk", "storeName": "Lidl"}, map[string]any{"name": "Bread", "storeName": " LIDL "})
	if *results[0].Item.StoreId != *results[1].Item.StoreId || *results[1].Item.StoreName != "Lidl" {
		t.Fatalf("results = %+v", results)
	}
	again := c.add(map[string]any{"name": "Cheese", "storeName": "lidl"})[0].Item
	if *again.StoreId != *results[0].Item.StoreId {
		t.Fatalf("again = %+v", again)
	}
	stores := expect[generated.StoreList](c, http.MethodGet, "/v1/stores", nil, http.StatusOK).Items
	if len(stores) != 1 || stores[0].Name != "Lidl" || stores[0].SortOrder != 1 || stores[0].CreatedBy.Kind != generated.Dev {
		t.Fatalf("stores = %+v", stores)
	}
	// A second store goes after the first; the store filter keeps its items.
	rewe := expect[generated.Store](c, http.MethodPost, "/v1/stores", map[string]any{"name": "REWE"}, http.StatusCreated)
	if rewe.SortOrder != 2 {
		t.Fatalf("rewe = %+v", rewe)
	}
	c.expectError(http.MethodPost, "/v1/stores", map[string]any{"name": "rewe"}, http.StatusConflict, "store_exists")
	c.add(map[string]any{"name": "Wine", "storeId": rewe.Id})
	if got := names(c.list("storeId=" + rewe.Id.String()).Items); got != "Wine" {
		t.Fatalf("REWE items = %s", got)
	}
	// An archived store cannot be given by id; by name it is restored.
	archived := expect[generated.Store](c, http.MethodPatch, "/v1/stores/"+rewe.Id.String(), map[string]any{"archived": true, "sortOrder": 0}, http.StatusOK)
	if !archived.Archived || archived.ArchivedAt == nil || archived.SortOrder != 0 {
		t.Fatalf("archived = %+v", archived)
	}
	if got := expect[generated.StoreList](c, http.MethodGet, "/v1/stores", nil, http.StatusOK).Items; len(got) != 1 {
		t.Fatalf("archived store listed: %+v", got)
	}
	if got := expect[generated.StoreList](c, http.MethodGet, "/v1/stores?includeArchived=true", nil, http.StatusOK).Items; len(got) != 2 || got[0].Name != "REWE" {
		t.Fatalf("with archived: %+v", got)
	}
	c.expectError(http.MethodPost, "/v1/shopping-items", map[string]any{"items": []any{map[string]any{"name": "Beer", "storeId": rewe.Id}}}, http.StatusConflict, "store_archived")
	beer := c.add(map[string]any{"name": "Beer", "storeName": "Rewe"})[0].Item
	if *beer.StoreId != rewe.Id {
		t.Fatalf("beer = %+v", beer)
	}
	if got := expect[generated.Store](c, http.MethodGet, "/v1/stores/"+rewe.Id.String(), nil, http.StatusOK); got.Archived {
		t.Fatalf("not restored: %+v", got)
	}
	// Renames keep names unique.
	c.expectError(http.MethodPatch, "/v1/stores/"+rewe.Id.String(), map[string]any{"name": "lidl"}, http.StatusConflict, "store_exists")
	renamed := expect[generated.Store](c, http.MethodPatch, "/v1/stores/"+rewe.Id.String(), map[string]any{"name": "REWE City"}, http.StatusOK)
	if renamed.Name != "REWE City" || *c.list("storeId=" + rewe.Id.String()).Items[0].StoreName != "REWE City" {
		t.Fatalf("renamed = %+v", renamed)
	}
	c.expectError(http.MethodPatch, "/v1/stores/"+rewe.Id.String(), map[string]any{}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPatch, "/v1/stores/"+rewe.Id.String(), map[string]any{"name": nil}, http.StatusBadRequest, "invalid_request")
	if n := c.count("stores", "true"); n != 2 {
		t.Fatalf("%d stores", n)
	}
}

func TestBoughtAndNeededTransitions(t *testing.T) {
	c := newAPIClient(t)
	milk := c.add(map[string]any{"name": "Milk", "amount": "1 l", "note": "lactose-free", "storeName": "Lidl"})[0].Item
	bought := expect[generated.ShoppingItem](c, http.MethodPost, "/v1/shopping-items/"+milk.Id.String()+"/bought", nil, http.StatusOK)
	if bought.State != generated.Bought || bought.BoughtAt == nil {
		t.Fatalf("bought = %+v", bought)
	}
	// Bought again: unchanged.
	if again := expect[generated.ShoppingItem](c, http.MethodPost, "/v1/shopping-items/"+milk.Id.String()+"/bought", nil, http.StatusOK); !again.BoughtAt.Equal(*bought.BoughtAt) {
		t.Fatalf("again = %+v", again)
	}
	// Needed again, with nothing open by that name: the same item reopens.
	reopened := expect[generated.ShoppingItemResult](c, http.MethodPost, "/v1/shopping-items/"+milk.Id.String()+"/needed", nil, http.StatusOK)
	if reopened.Merged || reopened.Item.Id != milk.Id || reopened.Item.State != generated.Needed || reopened.Item.BoughtAt != nil {
		t.Fatalf("reopened = %+v", reopened)
	}
	if again := expect[generated.ShoppingItemResult](c, http.MethodPost, "/v1/shopping-items/"+milk.Id.String()+"/needed", nil, http.StatusOK); again.Merged || again.Item.Id != milk.Id {
		t.Fatalf("needed again = %+v", again)
	}
	// Bought, then milk is added anew; reopening the bought one merges it
	// into the new one, which keeps its own amount and takes the note and
	// store it lacks.
	expect[generated.ShoppingItem](c, http.MethodPost, "/v1/shopping-items/"+milk.Id.String()+"/bought", nil, http.StatusOK)
	newer := c.add(map[string]any{"name": "milk", "amount": "2 l"})[0]
	if newer.Merged || newer.Item.Id == milk.Id {
		t.Fatalf("newer = %+v", newer)
	}
	merged := expect[generated.ShoppingItemResult](c, http.MethodPost, "/v1/shopping-items/"+milk.Id.String()+"/needed", nil, http.StatusOK)
	if !merged.Merged || merged.Item.Id != newer.Item.Id || *merged.Item.Amount != "2 l" || *merged.Item.Note != "lactose-free" ||
		merged.Item.StoreName == nil || *merged.Item.StoreName != "Lidl" {
		t.Fatalf("merged = %+v", merged)
	}
	c.expectError(http.MethodGet, "/v1/shopping-items/"+milk.Id.String(), nil, http.StatusNotFound, "not_found")
	if n := c.count("shopping_items", "true"); n != 1 {
		t.Fatalf("%d items", n)
	}
	c.expectError(http.MethodPost, "/v1/shopping-items/"+milk.Id.String()+"/bought", nil, http.StatusNotFound, "not_found")
	c.expectError(http.MethodPost, "/v1/shopping-items/"+milk.Id.String()+"/needed", nil, http.StatusNotFound, "not_found")
}

func TestClearBought(t *testing.T) {
	c := newAPIClient(t)
	results := c.add(map[string]any{"name": "Milk"}, map[string]any{"name": "Bread"}, map[string]any{"name": "Eggs"})
	for _, result := range results[:2] {
		expect[generated.ShoppingItem](c, http.MethodPost, "/v1/shopping-items/"+result.Item.Id.String()+"/bought", nil, http.StatusOK)
	}
	// Bread was bought yesterday.
	if _, err := c.pool.Exec(t.Context(), "UPDATE shopping_items SET bought_at = now() - interval '1 day' WHERE normalized_name = 'bread'"); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if got := expect[generated.ClearBoughtResult](c, http.MethodPost, "/v1/shopping-items/clear-bought", map[string]any{"boughtBefore": cutoff}, http.StatusOK); got.Deleted != 1 {
		t.Fatalf("cleared %d before %s", got.Deleted, cutoff)
	}
	if got := names(c.list("").Items); got != "Eggs, Milk" {
		t.Fatalf("after clearing bread: %s", got)
	}
	if got := expect[generated.ClearBoughtResult](c, http.MethodPost, "/v1/shopping-items/clear-bought", nil, http.StatusOK); got.Deleted != 1 {
		t.Fatalf("cleared %d", got.Deleted)
	}
	if got := names(c.list("").Items); got != "Eggs" {
		t.Fatalf("after clearing: %s", got)
	}
	if got := expect[generated.ClearBoughtResult](c, http.MethodPost, "/v1/shopping-items/clear-bought", "", http.StatusOK); got.Deleted != 0 {
		t.Fatalf("cleared %d with nothing bought", got.Deleted)
	}
}

func TestUpdateShoppingItem(t *testing.T) {
	c := newAPIClient(t)
	results := c.add(map[string]any{"name": "Mlik", "amount": "1 l", "note": "x"}, map[string]any{"name": "Bread"})
	typo := results[0].Item
	if typo.Aisle != generated.AisleOther {
		t.Fatalf("typo = %+v", typo)
	}
	// A rename gets a new normalised name, and the rules place it again.
	fixed := expect[generated.ShoppingItem](c, http.MethodPatch, "/v1/shopping-items/"+typo.Id.String(),
		map[string]any{"name": "Milk", "amount": nil, "note": "  ", "storeName": "Aldi"}, http.StatusOK)
	if fixed.Name != "Milk" || fixed.NormalizedName != "milk" || fixed.Aisle != generated.AisleDairy || fixed.AisleSetBy != generated.AisleSetByRule ||
		fixed.Amount != nil || fixed.Note != nil || *fixed.StoreName != "Aldi" {
		t.Fatalf("fixed = %+v", fixed)
	}
	// A rename onto another needed item's name is refused.
	c.expectError(http.MethodPatch, "/v1/shopping-items/"+typo.Id.String(), map[string]any{"name": "BREAD"}, http.StatusConflict, "already_on_list")
	// An aisle the request names is kept on later renames.
	pinned := expect[generated.ShoppingItem](c, http.MethodPatch, "/v1/shopping-items/"+typo.Id.String(), map[string]any{"aisle": "drinks", "storeId": nil}, http.StatusOK)
	if pinned.Aisle != generated.AisleDrinks || pinned.AisleSetBy != generated.AisleSetByManual || pinned.StoreId != nil {
		t.Fatalf("pinned = %+v", pinned)
	}
	renamed := expect[generated.ShoppingItem](c, http.MethodPatch, "/v1/shopping-items/"+typo.Id.String(), map[string]any{"name": "Oat milk"}, http.StatusOK)
	if renamed.Aisle != generated.AisleDrinks {
		t.Fatalf("renamed = %+v", renamed)
	}
	// A bought item may take a needed item's name.
	expect[generated.ShoppingItem](c, http.MethodPost, "/v1/shopping-items/"+typo.Id.String()+"/bought", nil, http.StatusOK)
	if got := expect[generated.ShoppingItem](c, http.MethodPatch, "/v1/shopping-items/"+typo.Id.String(), map[string]any{"name": "bread"}, http.StatusOK); got.NormalizedName != "bread" {
		t.Fatalf("bought rename = %+v", got)
	}
	for _, body := range []any{map[string]any{}, map[string]any{"name": nil}, map[string]any{"aisle": "garden"}, map[string]any{"storeId": nil, "storeName": "x"}, "[]"} {
		c.expectError(http.MethodPatch, "/v1/shopping-items/"+typo.Id.String(), body, http.StatusBadRequest, "invalid_request")
	}
	c.expectError(http.MethodPatch, "/v1/shopping-items/"+typo.Id.String(), map[string]any{"storeId": typo.Id}, http.StatusBadRequest, "unknown_store")
	if status, _ := c.do(http.MethodDelete, "/v1/shopping-items/"+typo.Id.String(), nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	c.expectError(http.MethodDelete, "/v1/shopping-items/"+typo.Id.String(), nil, http.StatusNotFound, "not_found")
}

func TestShoppingListOrderAndPaging(t *testing.T) {
	c := newAPIClient(t)
	added := c.add(
		map[string]any{"name": "Toilet paper"}, map[string]any{"name": "Zucchini", "aisle": "produce"}, map[string]any{"name": "Apples"},
		map[string]any{"name": "Kerzen"}, map[string]any{"name": "Milk"}, map[string]any{"name": "Bread"}, map[string]any{"name": "Cheese"},
	)
	byName := map[string]generated.ShoppingItem{}
	for _, result := range added {
		byName[result.Item.Name] = result.Item
	}
	for _, name := range []string{"Cheese", "Kerzen"} {
		expect[generated.ShoppingItem](c, http.MethodPost, "/v1/shopping-items/"+byName[name].Id.String()+"/bought", nil, http.StatusOK)
	}
	// Kerzen was bought later than Cheese, so it comes first among bought items.
	if _, err := c.pool.Exec(t.Context(), "UPDATE shopping_items SET bought_at = now() + interval '1 minute' WHERE normalized_name = 'kerzen'"); err != nil {
		t.Fatal(err)
	}
	want := "Apples, Zucchini, Bread, Milk, Toilet paper, Kerzen, Cheese"
	if got := names(c.list("").Items); got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	// Pages of two walk the same order without gaps or repeats.
	var walked []generated.ShoppingItem
	query := "limit=2"
	for pages := 0; ; pages++ {
		page := c.list(query)
		walked = append(walked, page.Items...)
		if page.NextCursor == nil {
			break
		}
		if pages > 5 {
			t.Fatal("too many pages")
		}
		query = "limit=2&cursor=" + *page.NextCursor
	}
	if got := names(walked); got != want {
		t.Fatalf("paged = %s", got)
	}
	if got := names(c.list("state=needed&limit=3").Items); got != "Apples, Zucchini, Bread" {
		t.Fatalf("needed = %s", got)
	}
	if got := names(c.list("state=bought").Items); got != "Kerzen, Cheese" {
		t.Fatalf("bought = %s", got)
	}
	if got := names(c.list("aisle=dairy").Items); got != "Milk, Cheese" {
		t.Fatalf("dairy = %s", got)
	}
	c.expectError(http.MethodGet, "/v1/shopping-items?cursor=nope", nil, http.StatusBadRequest, "invalid_cursor")
	c.expectError(http.MethodGet, "/v1/shopping-items?limit=500", nil, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodGet, "/v1/shopping-items?state=lost", nil, http.StatusBadRequest, "invalid_request")
	got := expect[generated.ShoppingItem](c, http.MethodGet, "/v1/shopping-items/"+byName["Milk"].Id.String(), nil, http.StatusOK)
	if got.Name != "Milk" {
		t.Fatalf("get = %+v", got)
	}
}

func TestAisleRules(t *testing.T) {
	c := newAPIClient(t)
	dairy := expect[generated.AisleRuleList](c, http.MethodGet, "/v1/aisle-rules?aisle=dairy", nil, http.StatusOK).Items
	if len(dairy) < 10 {
		t.Fatalf("dairy rules = %d", len(dairy))
	}
	all := expect[generated.AisleRuleList](c, http.MethodGet, "/v1/aisle-rules", nil, http.StatusOK).Items
	if len(all) < 150 || all[0].CreatedBy.Kind != generated.System || *all[0].CreatedBy.Name != "food" {
		t.Fatalf("%d rules, first %+v", len(all), all[0])
	}
	if c.add(map[string]any{"name": "Kerzen"})[0].Item.Aisle != generated.AisleOther {
		t.Fatal("Kerzen placed before its rule")
	}
	rule := expect[generated.AisleRule](c, http.MethodPost, "/v1/aisle-rules", map[string]any{"keyword": " KERZE ", "aisle": "household", "match": "substring"}, http.StatusCreated)
	if rule.Keyword != "kerze" || rule.Match != generated.Substring || rule.CreatedBy.Kind != generated.Dev {
		t.Fatalf("rule = %+v", rule)
	}
	c.expectError(http.MethodPost, "/v1/aisle-rules", map[string]any{"keyword": "Kerze", "aisle": "other"}, http.StatusConflict, "aisle_rule_exists")
	c.expectError(http.MethodPost, "/v1/aisle-rules", map[string]any{"keyword": "x", "aisle": "garden"}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/aisle-rules", map[string]any{"keyword": "x", "aisle": "other", "match": "fuzzy"}, http.StatusBadRequest, "invalid_request")
	// Items already on the list keep their aisle; new ones follow the rule.
	if got := c.add(map[string]any{"name": "Teelichtkerzen"})[0].Item; got.Aisle != generated.AisleHousehold {
		t.Fatalf("new item = %+v", got)
	}
	if got := c.list("aisle=other").Items; names(got) != "Kerzen" {
		t.Fatalf("other = %s", names(got))
	}
	// A word rule is the default.
	word := expect[generated.AisleRule](c, http.MethodPost, "/v1/aisle-rules", map[string]any{"keyword": "lamp", "aisle": "household"}, http.StatusCreated)
	if word.Match != generated.Word {
		t.Fatalf("word = %+v", word)
	}
	if status, _ := c.do(http.MethodDelete, "/v1/aisle-rules/"+rule.Id.String(), nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	c.expectError(http.MethodDelete, "/v1/aisle-rules/"+rule.Id.String(), nil, http.StatusNotFound, "not_found")
	if got := c.add(map[string]any{"name": "Duftkerzen"})[0].Item; got.Aisle != generated.AisleOther {
		t.Fatalf("after delete = %+v", got)
	}
}
