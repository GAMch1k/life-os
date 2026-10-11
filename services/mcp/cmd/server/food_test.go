package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GAMch1k/life-os/pkg/activity"
)

const (
	shoppingUUID = "1a2b3c4d-5e6f-4a1b-8c2d-3e4f5a6b7c8d"
	otherUUID    = "2b3c4d5e-6f7a-4b2c-9d3e-4f5a6b7c8d9e"
	storeUUID    = "3c4d5e6f-7a8b-4c3d-8e4f-5a6b7c8d9e0f"
)

// foodSession connects an MCP client to a server with only the Food tools,
// backed by a fake Food API that records every request and answers the
// store list with Lidl, other reads with an empty list, and writes with an
// item.
func foodSession(t *testing.T) (*mcp.ClientSession, *[]calendarRequest) {
	t.Helper()
	return foodSessionWith(t, func(r *http.Request) (int, string) {
		switch {
		case r.URL.Path == "/v1/stores":
			return http.StatusOK, `{"items":[{"id":"` + storeUUID + `","name":"Lidl"}]}`
		case r.Method == http.MethodGet:
			return http.StatusOK, `{"items":[]}`
		default:
			return http.StatusOK, `{"id":"` + strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/shopping-items/"), "/bought") + `","state":"bought"}`
		}
	})
}

// foodSessionWith is foodSession with the fake Food API's answers chosen by
// respond.
func foodSessionWith(t *testing.T, respond func(*http.Request) (int, string)) (*mcp.ClientSession, *[]calendarRequest) {
	t.Helper()
	requests := &[]calendarRequest{}
	food := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		request := calendarRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Actor: r.Header.Get(activity.ActorHeader), Idempotency: r.Header.Get("Idempotency-Key"), RawBody: string(raw)}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &request.Body)
		}
		*requests = append(*requests, request)
		status, body := respond(r)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(food.Close)
	server := mcp.NewServer(&mcp.Implementation{Name: "life-os-test", Version: "0"}, nil)
	addFoodTools(server, food.Client(), food.URL, activity.NewEmitter("", "mcp", nil))
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, requests
}

func TestShoppingItemsAreAddedAsCapturedWithTheirKey(t *testing.T) {
	session, requests := foodSession(t)
	// "Milch und 6 Eier bei Lidl" from Inbox item inbox-1.
	result := callTool(t, session, "food_add_shopping_items", map[string]any{
		"idempotencyKey": "food-1", "sourceRef": "inbox-1",
		"items": []any{
			map[string]any{"name": " Milch ", "storeName": "Lidl"},
			map[string]any{"name": "Eier", "amount": "6", "aisle": "dairy", "note": " "},
		},
	})
	if result.IsError {
		t.Fatalf("add failed: %s", resultText(result))
	}
	got := (*requests)[0]
	if got.Method != http.MethodPost || got.Path != "/v1/shopping-items" || got.Idempotency != "food-1" || got.Actor != "agent:mcp" {
		t.Fatalf("Food saw %+v", got)
	}
	want := map[string]any{"items": []any{
		map[string]any{"name": "Milch", "storeName": "Lidl", "source": "capture", "sourceRef": "inbox-1"},
		map[string]any{"name": "Eier", "amount": "6", "aisle": "dairy", "source": "capture", "sourceRef": "inbox-1"},
	}}
	if !reflect.DeepEqual(got.Body, want) {
		t.Fatalf("body = %s", got.RawBody)
	}
	for name, arguments := range map[string]map[string]any{
		"no items":      {"items": []any{}},
		"blank name":    {"items": []any{map[string]any{"name": "  "}}},
		"unknown aisle": {"items": []any{map[string]any{"name": "Milch", "aisle": "fridge"}}},
		"store id":      {"items": []any{map[string]any{"name": "Milch", "storeId": storeUUID}}},
		"too many":      {"items": manyItems(51)},
	} {
		if result := callTool(t, session, "food_add_shopping_items", arguments); !result.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(*requests) != 1 {
		t.Fatalf("refused adds reached Food: %d requests", len(*requests))
	}
}

func manyItems(n int) []any {
	items := make([]any, n)
	for i := range items {
		items[i] = map[string]any{"name": "item"}
	}
	return items
}

func TestMarkBoughtWritesEachItemWithItsOwnKey(t *testing.T) {
	session, requests := foodSession(t)
	result := callTool(t, session, "food_mark_bought", map[string]any{"idempotencyKey": "food-2", "itemIds": []any{shoppingUUID, otherUUID, strings.ToUpper(shoppingUUID)}})
	if result.IsError {
		t.Fatalf("mark failed: %s", resultText(result))
	}
	// The repeated id is marked once.
	if len(*requests) != 2 {
		t.Fatalf("Food saw %+v", *requests)
	}
	for i, id := range []string{shoppingUUID, otherUUID} {
		got := (*requests)[i]
		if got.Method != http.MethodPost || got.Path != "/v1/shopping-items/"+id+"/bought" || got.Idempotency != "food-2."+id || got.RawBody != "" {
			t.Fatalf("request %d = %+v", i, got)
		}
	}
	if items, _ := result.StructuredContent.(map[string]any)["data"].(map[string]any)["items"].([]any); len(items) != 2 {
		t.Fatalf("result = %#v", result.StructuredContent)
	}
	for name, arguments := range map[string]map[string]any{
		"no ids":    {"itemIds": []any{}},
		"a name":    {"itemIds": []any{"Milch"}},
		"a path":    {"itemIds": []any{"../stores"}},
		"no itemId": {},
	} {
		if result := callTool(t, session, "food_mark_bought", arguments); !result.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(*requests) != 2 {
		t.Fatalf("refused marks reached Food: %d requests", len(*requests))
	}
}

func TestShoppingListFiltersByStateAndStoreName(t *testing.T) {
	session, requests := foodSession(t)
	if result := callTool(t, session, "food_list_shopping_items", map[string]any{"state": "needed", "storeName": " lidl ", "limit": 200}); result.IsError {
		t.Fatalf("list failed: %s", resultText(result))
	}
	stores, list := (*requests)[0], (*requests)[1]
	if stores.Path != "/v1/stores" || stores.Query != "includeArchived=true" {
		t.Fatalf("stores read = %+v", stores)
	}
	query, _ := url.ParseQuery(list.Query)
	if list.Path != "/v1/shopping-items" || query.Get("state") != "needed" || query.Get("storeId") != storeUUID || query.Get("limit") != "200" {
		t.Fatalf("list = %+v", list)
	}
	// No store has the name, so no item has the store: Food is not asked.
	result := callTool(t, session, "food_list_shopping_items", map[string]any{"storeName": "Aldi"})
	if result.IsError || len(*requests) != 3 || !strings.Contains(resultText(result), `"items":[]`) {
		t.Fatalf("unknown store: %s, requests %+v", resultText(result), *requests)
	}
	for name, arguments := range map[string]map[string]any{
		"state":      {"state": "open"},
		"aisle":      {"aisle": "fridge"},
		"both":       {"storeId": storeUUID, "storeName": "Lidl"},
		"store name": {"storeId": "Lidl"},
		"limit":      {"limit": 500},
	} {
		if result := callTool(t, session, "food_list_shopping_items", arguments); !result.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(*requests) != 3 {
		t.Fatalf("refused lists reached Food: %d", len(*requests))
	}
}

func TestPantryItemsAreAddedWithTheirExpiry(t *testing.T) {
	session, requests := foodSession(t)
	result := callTool(t, session, "food_add_pantry_items", map[string]any{
		"idempotencyKey": "food-3", "sourceRef": "inbox-2",
		"items": []any{map[string]any{"name": "йогурт", "location": "fridge", "expiresOn": "2026-10-16", "amount": "2"}},
	})
	if result.IsError {
		t.Fatalf("add failed: %s", resultText(result))
	}
	got := (*requests)[0]
	want := map[string]any{"items": []any{map[string]any{"name": "йогурт", "location": "fridge", "expiresOn": "2026-10-16", "amount": "2", "source": "capture", "sourceRef": "inbox-2"}}}
	if got.Path != "/v1/pantry-items" || got.Idempotency != "food-3" || !reflect.DeepEqual(got.Body, want) {
		t.Fatalf("Food saw %+v %s", got, got.RawBody)
	}
	for name, arguments := range map[string]map[string]any{
		"bad date":     {"items": []any{map[string]any{"name": "Joghurt", "expiresOn": "Freitag"}}},
		"bad location": {"items": []any{map[string]any{"name": "Joghurt", "location": "Kühlschrank"}}},
		"blank name":   {"items": []any{map[string]any{"name": ""}}},
	} {
		if result := callTool(t, session, "food_add_pantry_items", arguments); !result.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(*requests) != 1 {
		t.Fatalf("refused adds reached Food: %d requests", len(*requests))
	}
}

func TestPantryUpdateEndsAnItemOrEditsItsFields(t *testing.T) {
	session, requests := foodSession(t)
	if result := callTool(t, session, "food_update_pantry_item", map[string]any{"idempotencyKey": "food-4", "itemId": shoppingUUID, "state": "used_up"}); result.IsError {
		t.Fatalf("used up failed: %s", resultText(result))
	}
	if result := callTool(t, session, "food_update_pantry_item", map[string]any{"itemId": shoppingUUID, "state": "thrown_away"}); result.IsError {
		t.Fatalf("thrown away failed: %s", resultText(result))
	}
	if result := callTool(t, session, "food_update_pantry_item", map[string]any{"itemId": shoppingUUID, "expiresOn": "2026-10-18", "location": "freezer", "clear": []any{"note"}}); result.IsError {
		t.Fatalf("edit failed: %s", resultText(result))
	}
	usedUp, thrown, edit := (*requests)[0], (*requests)[1], (*requests)[2]
	if usedUp.Method != http.MethodPost || usedUp.Path != "/v1/pantry-items/"+shoppingUUID+"/used-up" || usedUp.Idempotency != "food-4" {
		t.Fatalf("used up = %+v", usedUp)
	}
	if thrown.Path != "/v1/pantry-items/"+shoppingUUID+"/thrown-away" {
		t.Fatalf("thrown away = %+v", thrown)
	}
	if edit.Method != http.MethodPatch || edit.Path != "/v1/pantry-items/"+shoppingUUID || !reflect.DeepEqual(edit.Body, map[string]any{"expiresOn": "2026-10-18", "location": "freezer", "note": nil}) {
		t.Fatalf("edit = %+v %s", edit, edit.RawBody)
	}
	for name, arguments := range map[string]map[string]any{
		"bad id":          {"itemId": "Joghurt", "state": "used_up"},
		"unknown state":   {"itemId": shoppingUUID, "state": "eaten"},
		"state and field": {"itemId": shoppingUUID, "state": "used_up", "amount": "1"},
		"nothing":         {"itemId": shoppingUUID},
		"set and cleared": {"itemId": shoppingUUID, "note": "open", "clear": []any{"note"}},
		"clear location":  {"itemId": shoppingUUID, "clear": []any{"location"}},
		"bad date":        {"itemId": shoppingUUID, "expiresOn": "soon"},
	} {
		if result := callTool(t, session, "food_update_pantry_item", arguments); !result.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(*requests) != 3 {
		t.Fatalf("refused updates reached Food: %d requests", len(*requests))
	}
}

func TestPantryListBuildsAFilteredQuery(t *testing.T) {
	session, requests := foodSession(t)
	if result := callTool(t, session, "food_list_pantry_items", map[string]any{"state": "in_stock", "location": "fridge", "expiringWithinDays": 0}); result.IsError {
		t.Fatalf("list failed: %s", resultText(result))
	}
	query, _ := url.ParseQuery((*requests)[0].Query)
	if (*requests)[0].Path != "/v1/pantry-items" || query.Get("state") != "in_stock" || query.Get("location") != "fridge" || query.Get("expiringWithinDays") != "0" {
		t.Fatalf("Food saw %+v", (*requests)[0])
	}
	for name, arguments := range map[string]map[string]any{
		"state": {"state": "needed"}, "location": {"location": "cellar"}, "days": {"expiringWithinDays": 400},
	} {
		if result := callTool(t, session, "food_list_pantry_items", arguments); !result.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(*requests) != 1 {
		t.Fatalf("refused lists reached Food: %d", len(*requests))
	}
}

const recipeUUID = "4d5e6f7a-8b9c-4d4e-9f5a-6b7c8d9e0f1a"

func TestRecipesAreFoundByTitleTagOrIngredient(t *testing.T) {
	session, requests := foodSession(t)
	if result := callTool(t, session, "food_list_recipes", map[string]any{"q": " bolognese ", "ingredient": "hack", "limit": 20}); result.IsError {
		t.Fatalf("list failed: %s", resultText(result))
	}
	got := (*requests)[0]
	query, _ := url.ParseQuery(got.Query)
	if got.Path != "/v1/recipes" || query.Get("q") != "bolognese" || query.Get("ingredient") != "hack" || query.Get("limit") != "20" || query.Has("tag") {
		t.Fatalf("Food saw %+v", got)
	}
	if result := callTool(t, session, "food_list_recipes", map[string]any{"limit": 201}); !result.IsError || len(*requests) != 1 {
		t.Fatalf("a limit of 201 was accepted: %s", resultText(result))
	}
}

func TestPlanMealSetsTheSlotToARecipeOrText(t *testing.T) {
	session, requests := foodSession(t)
	if result := callTool(t, session, "food_plan_meal", map[string]any{"idempotencyKey": "food-5", "date": "2026-10-14", "slot": "dinner", "recipeId": recipeUUID, "servings": 4}); result.IsError {
		t.Fatalf("plan failed: %s", resultText(result))
	}
	if result := callTool(t, session, "food_plan_meal", map[string]any{"date": "2026-10-16", "slot": "lunch", "text": " суп ", "note": "with bread"}); result.IsError {
		t.Fatalf("plan failed: %s", resultText(result))
	}
	recipe, text := (*requests)[0], (*requests)[1]
	if recipe.Method != http.MethodPut || recipe.Path != "/v1/meal-plan/2026-10-14/dinner" || recipe.Idempotency != "food-5" || recipe.Actor != "agent:mcp" ||
		!reflect.DeepEqual(recipe.Body, map[string]any{"recipeId": recipeUUID, "servings": 4.0}) {
		t.Fatalf("recipe plan = %+v %s", recipe, recipe.RawBody)
	}
	if text.Path != "/v1/meal-plan/2026-10-16/lunch" || !reflect.DeepEqual(text.Body, map[string]any{"text": "суп", "note": "with bread"}) {
		t.Fatalf("text plan = %+v %s", text, text.RawBody)
	}
	for name, arguments := range map[string]map[string]any{
		"both":         {"date": "2026-10-14", "slot": "dinner", "recipeId": recipeUUID, "text": "Pasta"},
		"neither":      {"date": "2026-10-14", "slot": "dinner"},
		"recipe title": {"date": "2026-10-14", "slot": "dinner", "recipeId": "Pasta Bolognese"},
		"bad slot":     {"date": "2026-10-14", "slot": "supper", "text": "Pasta"},
		"bad date":     {"date": "Wednesday", "slot": "dinner", "text": "Pasta"},
		"a path":       {"date": "2026-10-14/../x", "slot": "dinner", "text": "Pasta"},
		"servings":     {"date": "2026-10-14", "slot": "dinner", "text": "Pasta", "servings": 0},
	} {
		if result := callTool(t, session, "food_plan_meal", arguments); !result.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(*requests) != 2 {
		t.Fatalf("refused plans reached Food: %d requests", len(*requests))
	}
}

func TestClearingAnEmptySlotIsNotAnError(t *testing.T) {
	session, requests := foodSessionWith(t, func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "/lunch") {
			return http.StatusNotFound, `{"error":{"code":"not_found","message":"Nothing is planned there"},"request_id":"r"}`
		}
		if strings.HasSuffix(r.URL.Path, "/breakfast") {
			return http.StatusBadGateway, `{"error":{"code":"unavailable","message":"down"},"request_id":"r"}`
		}
		return http.StatusNoContent, ""
	})
	result := callTool(t, session, "food_clear_meal", map[string]any{"idempotencyKey": "food-6", "date": "2026-10-17", "slot": "dinner"})
	if result.IsError || !strings.Contains(resultText(result), `"cleared":true`) {
		t.Fatalf("clear = %s", resultText(result))
	}
	if got := (*requests)[0]; got.Method != http.MethodDelete || got.Path != "/v1/meal-plan/2026-10-17/dinner" || got.Idempotency != "food-6" {
		t.Fatalf("Food saw %+v", got)
	}
	if result := callTool(t, session, "food_clear_meal", map[string]any{"date": "2026-10-17", "slot": "lunch"}); result.IsError || !strings.Contains(resultText(result), `"cleared":false`) {
		t.Fatalf("empty slot = %s", resultText(result))
	}
	if result := callTool(t, session, "food_clear_meal", map[string]any{"date": "2026-10-17", "slot": "breakfast"}); !result.IsError {
		t.Fatalf("an unavailable Food was a success: %s", resultText(result))
	}
	if result := callTool(t, session, "food_clear_meal", map[string]any{"date": "2026-10-17", "slot": "snack"}); !result.IsError || len(*requests) != 3 {
		t.Fatalf("a bad slot was accepted: %s", resultText(result))
	}
}

func TestMealPlanReadsAtMost31Days(t *testing.T) {
	session, requests := foodSession(t)
	if result := callTool(t, session, "food_list_meal_plan", map[string]any{"from": "2026-10-01", "to": "2026-10-31"}); result.IsError {
		t.Fatalf("list failed: %s", resultText(result))
	}
	if got := (*requests)[0]; got.Path != "/v1/meal-plan" || got.Query != "from=2026-10-01&to=2026-10-31" {
		t.Fatalf("Food saw %+v", got)
	}
	for name, arguments := range map[string]map[string]any{
		"32 days": {"from": "2026-10-01", "to": "2026-11-01"}, "backwards": {"from": "2026-10-12", "to": "2026-10-11"}, "words": {"from": "today", "to": "Sunday"},
	} {
		if result := callTool(t, session, "food_list_meal_plan", arguments); !result.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(*requests) != 1 {
		t.Fatalf("refused reads reached Food: %d", len(*requests))
	}
}

func TestMissingIngredientsComeFromRecipesOrAWeek(t *testing.T) {
	session, requests := foodSession(t)
	if result := callTool(t, session, "food_add_missing_ingredients", map[string]any{"idempotencyKey": "food-7", "recipeIds": []any{recipeUUID, strings.ToUpper(recipeUUID)}}); result.IsError {
		t.Fatalf("add failed: %s", resultText(result))
	}
	if result := callTool(t, session, "food_add_missing_ingredients", map[string]any{"from": "2026-10-12", "to": "2026-10-18", "dryRun": true}); result.IsError {
		t.Fatalf("dry run failed: %s", resultText(result))
	}
	recipes, week := (*requests)[0], (*requests)[1]
	if recipes.Method != http.MethodPost || recipes.Path != "/v1/shopping-items/from-recipes" || recipes.Idempotency != "food-7" || !reflect.DeepEqual(recipes.Body, map[string]any{"recipeIds": []any{recipeUUID}}) {
		t.Fatalf("recipes = %+v %s", recipes, recipes.RawBody)
	}
	if !reflect.DeepEqual(week.Body, map[string]any{"from": "2026-10-12", "to": "2026-10-18", "dryRun": true}) {
		t.Fatalf("week = %s", week.RawBody)
	}
	for name, arguments := range map[string]map[string]any{
		"neither":     {},
		"both":        {"recipeIds": []any{recipeUUID}, "from": "2026-10-12", "to": "2026-10-18"},
		"half range":  {"from": "2026-10-12"},
		"long range":  {"from": "2026-10-01", "to": "2026-11-15"},
		"recipe name": {"recipeIds": []any{"Pasta Bolognese"}},
	} {
		if result := callTool(t, session, "food_add_missing_ingredients", arguments); !result.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(*requests) != 2 {
		t.Fatalf("refused calls reached Food: %d", len(*requests))
	}
}
