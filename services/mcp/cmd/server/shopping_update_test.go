package main

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestShoppingItemUpdateRenamesOrEditsOneItem(t *testing.T) {
	session, requests := foodSessionWith(t, func(r *http.Request) (int, string) {
		if strings.Contains(r.URL.Path, otherUUID) {
			return http.StatusConflict, `{"error":{"code":"already_on_list","message":"another needed item has that name"}}`
		}
		return http.StatusOK, `{"id":"` + shoppingUUID + `","name":"Hafermilch","state":"needed"}`
	})
	result := callTool(t, session, "food_update_shopping_item", map[string]any{"idempotencyKey": "food-7", "itemId": " " + shoppingUUID + " ", "name": " Hafermilch "})
	if result.IsError {
		t.Fatalf("rename failed: %s", resultText(result))
	}
	if !strings.Contains(resultText(result), "Hafermilch") {
		t.Fatalf("result = %s, want the item", resultText(result))
	}
	if result := callTool(t, session, "food_update_shopping_item", map[string]any{"itemId": shoppingUUID, "amount": "2 l", "storeName": "Lidl", "aisle": "dairy", "clear": []any{"note"}}); result.IsError {
		t.Fatalf("edit failed: %s", resultText(result))
	}
	if result := callTool(t, session, "food_update_shopping_item", map[string]any{"itemId": shoppingUUID, "clear": []any{"store", "amount"}}); result.IsError {
		t.Fatalf("clear failed: %s", resultText(result))
	}
	rename, edit, clear := (*requests)[0], (*requests)[1], (*requests)[2]
	if rename.Method != http.MethodPatch || rename.Path != "/v1/shopping-items/"+shoppingUUID || rename.Idempotency != "food-7" || !reflect.DeepEqual(rename.Body, map[string]any{"name": "Hafermilch"}) {
		t.Fatalf("rename = %+v %s", rename, rename.RawBody)
	}
	if rename.Actor != "agent:mcp" {
		t.Errorf("actor = %q, want the MCP agent", rename.Actor)
	}
	if !reflect.DeepEqual(edit.Body, map[string]any{"amount": "2 l", "storeName": "Lidl", "aisle": "dairy", "note": nil}) {
		t.Fatalf("edit = %s", edit.RawBody)
	}
	if !reflect.DeepEqual(clear.Body, map[string]any{"storeId": nil, "amount": nil}) {
		t.Fatalf("clear = %s", clear.RawBody)
	}
	// Food's refusal reaches the caller with its code.
	if result := callTool(t, session, "food_update_shopping_item", map[string]any{"itemId": otherUUID, "name": "Milch"}); !result.IsError || !strings.Contains(resultText(result), "already_on_list") {
		t.Fatalf("a refused rename = %s, want already_on_list", resultText(result))
	}
	for name, arguments := range map[string]map[string]any{
		"bad id":            {"itemId": "Milch", "name": "Hafermilch"},
		"nothing":           {"itemId": shoppingUUID},
		"empty name":        {"itemId": shoppingUUID, "name": " "},
		"long name":         {"itemId": shoppingUUID, "name": strings.Repeat("x", 201)},
		"unknown aisle":     {"itemId": shoppingUUID, "aisle": "fridge"},
		"clear name":        {"itemId": shoppingUUID, "clear": []any{"name"}},
		"store set+cleared": {"itemId": shoppingUUID, "storeName": "dm", "clear": []any{"store"}},
		"note set+cleared":  {"itemId": shoppingUUID, "note": "oat", "clear": []any{"note"}},
	} {
		if result := callTool(t, session, "food_update_shopping_item", arguments); !result.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(*requests) != 4 {
		t.Fatalf("refused updates reached Food: %d requests", len(*requests))
	}
}
