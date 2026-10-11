package main

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const stapleUUID = "5e6f7a8b-9c0d-4e5f-8a6b-7c8d9e0f1a2b"

// stapleFood is a fake Food API with the staple Milch: running low answers
// with Food's restock, a staple already there is 409 staple_exists, and a
// staple removed twice is 404.
func stapleFood(t *testing.T) (*mcp.ClientSession, *[]calendarRequest) {
	t.Helper()
	removed := false
	session, requests := foodSessionWith(t, func(r *http.Request) (int, string) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/staples":
			return http.StatusOK, `{"items":[{"id":"` + stapleUUID + `","name":"Milch","normalizedName":"milch","onList":false}]}`
		case r.URL.Path == "/v1/staples/running-low":
			return http.StatusOK, `{"name":"Milch","normalizedName":"milch","changed":true,"staple":{"id":"` + stapleUUID + `"},"pantryItems":[],"restock":{"status":"added","reason":"running_low","stapleId":"` + stapleUUID + `","shoppingItem":{"id":"` + shoppingUUID + `","name":"Milch","normalizedName":"milch"},"message":"Added “Milch” to the list: running low (staple)"}}`
		case r.Method == http.MethodPost && r.URL.Path == "/v1/staples":
			if strings.Contains(r.Header.Get("Idempotency-Key"), "exists") {
				return http.StatusConflict, `{"error":{"code":"staple_exists","message":"Milch is already a staple"},"request_id":"r"}`
			}
			return http.StatusCreated, `{"id":"` + otherUUID + `","name":"Kaffee","normalizedName":"kaffee"}`
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/staples/"+stapleUUID:
			if removed {
				return http.StatusNotFound, `{"error":{"code":"not_found","message":"Staple not found"},"request_id":"r"}`
			}
			removed = true
			return http.StatusNoContent, ""
		case r.URL.Path == "/v1/pantry-items/"+shoppingUUID+"/used-up":
			return http.StatusOK, `{"id":"` + shoppingUUID + `","name":"Milch","state":"used_up","restock":{"status":"on_list","reason":"used_up","stapleId":"` + stapleUUID + `","shoppingItem":{"name":"Milch"},"message":"“Milch” is already on the list"}}`
		}
		return http.StatusOK, `{"items":[]}`
	})
	return session, requests
}

func TestRunningLowIsSaidByNameWithItsKey(t *testing.T) {
	session, requests := stapleFood(t)
	result := callTool(t, session, "food_mark_running_low", map[string]any{"idempotencyKey": "food-8", "name": " Milch "})
	if result.IsError {
		t.Fatalf("running low failed: %s", resultText(result))
	}
	got := (*requests)[0]
	if got.Method != http.MethodPost || got.Path != "/v1/staples/running-low" || got.Idempotency != "food-8" || got.Actor != "agent:mcp" ||
		!reflect.DeepEqual(got.Body, map[string]any{"name": "Milch", "addToList": false}) {
		t.Fatalf("Food saw %+v %s", got, got.RawBody)
	}
	// Food's restock reaches the caller.
	if !strings.Contains(resultText(result), `"status":"added"`) || !strings.Contains(resultText(result), "running low (staple)") {
		t.Fatalf("result = %s", resultText(result))
	}
	if result := callTool(t, session, "food_mark_running_low", map[string]any{"name": "рис", "addToList": true}); result.IsError {
		t.Fatalf("running low with addToList failed: %s", resultText(result))
	}
	if body := (*requests)[1].Body; !reflect.DeepEqual(body, map[string]any{"name": "рис", "addToList": true}) {
		t.Fatalf("body = %#v", body)
	}
	for name, arguments := range map[string]map[string]any{
		"blank": {"name": "  "}, "too long": {"name": strings.Repeat("я", 201)},
	} {
		if result := callTool(t, session, "food_mark_running_low", arguments); !result.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(*requests) != 2 {
		t.Fatalf("refused calls reached Food: %d", len(*requests))
	}
}

func TestAStapleIsAddedOnceAndAnExistingOneIsReturned(t *testing.T) {
	session, requests := stapleFood(t)
	result := callTool(t, session, "food_add_staple", map[string]any{"idempotencyKey": "food-9", "name": " Kaffee ", "amount": "500 g", "storeName": "Lidl"})
	if result.IsError || !strings.Contains(resultText(result), `"created":true`) || !strings.Contains(resultText(result), `"name":"Kaffee"`) {
		t.Fatalf("add = %s", resultText(result))
	}
	if got := (*requests)[0]; got.Path != "/v1/staples" || got.Idempotency != "food-9" || !reflect.DeepEqual(got.Body, map[string]any{"name": "Kaffee", "amount": "500 g", "storeName": "Lidl"}) {
		t.Fatalf("Food saw %+v %s", got, got.RawBody)
	}
	// Already a staple: not an error, and the existing staple comes back.
	result = callTool(t, session, "food_add_staple", map[string]any{"idempotencyKey": "food-exists", "name": "milch"})
	if result.IsError || !strings.Contains(resultText(result), `"created":false`) || !strings.Contains(resultText(result), stapleUUID) {
		t.Fatalf("existing = %s", resultText(result))
	}
	for name, arguments := range map[string]map[string]any{
		"blank":       {"name": ""},
		"long store":  {"name": "Kaffee", "storeName": strings.Repeat("s", 101)},
		"long amount": {"name": "Kaffee", "amount": strings.Repeat("1", 51)},
	} {
		if result := callTool(t, session, "food_add_staple", arguments); !result.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(*requests) != 3 {
		t.Fatalf("Food saw %d requests, want the add, the conflict, and the staples read", len(*requests))
	}
}

func TestAStapleIsRemovedByItsNormalisedName(t *testing.T) {
	session, requests := stapleFood(t)
	result := callTool(t, session, "food_remove_staple", map[string]any{"idempotencyKey": "food-10", "name": " MILCH "})
	if result.IsError || !strings.Contains(resultText(result), `"removed":true`) {
		t.Fatalf("remove = %s", resultText(result))
	}
	read, remove := (*requests)[0], (*requests)[1]
	if read.Method != http.MethodGet || read.Path != "/v1/staples" || remove.Method != http.MethodDelete || remove.Path != "/v1/staples/"+stapleUUID || remove.Idempotency != "food-10" {
		t.Fatalf("Food saw %+v then %+v", read, remove)
	}
	// Gone meanwhile, or no staple of the name: removed false, no error.
	if result := callTool(t, session, "food_remove_staple", map[string]any{"name": "Milch"}); result.IsError || !strings.Contains(resultText(result), `"removed":false`) {
		t.Fatalf("second remove = %s", resultText(result))
	}
	if result := callTool(t, session, "food_remove_staple", map[string]any{"name": "Vollmilch"}); result.IsError || !strings.Contains(resultText(result), `"removed":false`) {
		t.Fatalf("unknown remove = %s", resultText(result))
	}
	if len(*requests) != 5 {
		t.Fatalf("Food saw %d requests: %+v", len(*requests), *requests)
	}
}

func TestStaplesAreListedAndAUsedUpItemReportsItsRestock(t *testing.T) {
	session, requests := stapleFood(t)
	if result := callTool(t, session, "food_list_staples", map[string]any{}); result.IsError || !strings.Contains(resultText(result), `"normalizedName":"milch"`) {
		t.Fatalf("list = %s", resultText(result))
	}
	if got := (*requests)[0]; got.Method != http.MethodGet || got.Path != "/v1/staples" || got.Query != "" {
		t.Fatalf("Food saw %+v", got)
	}
	result := callTool(t, session, "food_update_pantry_item", map[string]any{"itemId": shoppingUUID, "state": "used_up"})
	if result.IsError || !strings.Contains(resultText(result), `"restock":{`) || !strings.Contains(resultText(result), `"status":"on_list"`) {
		t.Fatalf("used up = %s", resultText(result))
	}
}
