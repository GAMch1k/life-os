package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
)

func TestIdempotentReplayAndConflict(t *testing.T) {
	c := newAPIClient(t)
	seen := 0
	body := map[string]any{"items": []any{map[string]any{"name": "Milk", "storeName": "Lidl"}, map[string]any{"name": "Bread"}}}
	status, first := c.do(http.MethodPost, "/v1/shopping-items", body, "Idempotency-Key", "capture-1")
	if status != http.StatusOK {
		t.Fatalf("first: %d %s", status, first)
	}
	events := c.recorder.since(&seen)
	if eventTypes(events) != storeCreated+" "+shoppingItemAdded+" "+shoppingItemAdded {
		t.Fatalf("events = %s", eventTypes(events))
	}
	// A replay returns the original response, byte for byte: nothing merges,
	// nothing is added, nothing is recorded.
	status, again := c.do(http.MethodPost, "/v1/shopping-items", body, "Idempotency-Key", "capture-1")
	if status != http.StatusOK || string(again) != string(first) {
		t.Fatalf("replay: %d %s\nfirst: %s", status, again, first)
	}
	if events := c.recorder.since(&seen); len(events) != 0 {
		t.Fatalf("replay recorded %s", eventTypes(events))
	}
	if n := c.count("shopping_items", "true"); n != 2 {
		t.Fatalf("%d items", n)
	}
	// The same key with another body, or on another path, is refused.
	c.expectError(http.MethodPost, "/v1/shopping-items", map[string]any{"items": []any{map[string]any{"name": "Cheese"}}}, http.StatusConflict, "idempotency_conflict", "Idempotency-Key", "capture-1")
	c.expectError(http.MethodPost, "/v1/stores", map[string]any{"name": "Aldi"}, http.StatusConflict, "idempotency_conflict", "Idempotency-Key", "capture-1")
	if n := c.count("shopping_items", "true"); n != 2 {
		t.Fatalf("%d items after conflicts", n)
	}

	// State changes replay too: the second "bought" answers the first
	// response even after the item changed again.
	var response generated.AddShoppingItemsResponse
	if err := json.Unmarshal(first, &response); err != nil {
		t.Fatal(err)
	}
	milk := response.Items[0].Item.Id.String()
	bought := expect[generated.ShoppingItem](c, http.MethodPost, "/v1/shopping-items/"+milk+"/bought", nil, http.StatusOK, "Idempotency-Key", "buy-milk")
	expect[generated.ShoppingItemResult](c, http.MethodPost, "/v1/shopping-items/"+milk+"/needed", nil, http.StatusOK)
	replayed := expect[generated.ShoppingItem](c, http.MethodPost, "/v1/shopping-items/"+milk+"/bought", nil, http.StatusOK, "Idempotency-Key", "buy-milk")
	if replayed.State != generated.Bought || !replayed.BoughtAt.Equal(*bought.BoughtAt) {
		t.Fatalf("replayed = %+v", replayed)
	}
	if got := expect[generated.ShoppingItem](c, http.MethodGet, "/v1/shopping-items/"+milk, nil, http.StatusOK); got.State != generated.Needed {
		t.Fatalf("a replay changed the item: %+v", got)
	}
	// A 204 replays as a 204; a refusal is not stored, so the key stays free.
	c.expectError(http.MethodDelete, "/v1/aisle-rules/"+milk, nil, http.StatusNotFound, "not_found", "Idempotency-Key", "delete-1")
	if status, _ := c.do(http.MethodDelete, "/v1/shopping-items/"+milk, nil, "Idempotency-Key", "delete-1"); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	if status, raw := c.do(http.MethodDelete, "/v1/shopping-items/"+milk, nil, "Idempotency-Key", "delete-1"); status != http.StatusNoContent || len(raw) != 0 {
		t.Fatalf("delete replay: %d %s", status, raw)
	}
	c.expectError(http.MethodPost, "/v1/stores", map[string]any{"name": "Aldi"}, http.StatusBadRequest, "invalid_request", "Idempotency-Key", strings.Repeat("k", 201))
}

func TestConcurrentRequestsWithOneKeyRunOnce(t *testing.T) {
	c := newAPIClient(t)
	const senders = 6
	var wait sync.WaitGroup
	bodies := make([]string, senders)
	statuses := make([]int, senders)
	for index := range senders {
		wait.Go(func() {
			status, raw := c.do(http.MethodPost, "/v1/pantry-items", map[string]any{"items": []any{map[string]any{"name": "Yogurt"}}}, "Idempotency-Key", "same")
			statuses[index], bodies[index] = status, string(raw)
		})
	}
	wait.Wait()
	for index := range senders {
		if statuses[index] != http.StatusCreated || bodies[index] != bodies[0] {
			t.Fatalf("sender %d: %d %s", index, statuses[index], bodies[index])
		}
	}
	if n := c.count("pantry_items", "true"); n != 1 {
		t.Fatalf("%d pantry items", n)
	}
	if n := len(c.recorder.all()); n != 1 {
		t.Fatalf("%d events", n)
	}
}

func TestExpiredKeysArePruned(t *testing.T) {
	c := newAPIClient(t)
	expect[generated.Store](c, http.MethodPost, "/v1/stores", map[string]any{"name": "Lidl"}, http.StatusCreated, "Idempotency-Key", "old")
	if _, err := c.pool.Exec(t.Context(), "UPDATE idempotency_keys SET created_at = $1", fixedNow.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	c.handler.lastPrune.Store(0)
	expect[generated.Store](c, http.MethodPost, "/v1/stores", map[string]any{"name": "Aldi"}, http.StatusCreated, "Idempotency-Key", "new")
	if n := c.count("idempotency_keys", "true"); n != 1 {
		t.Fatalf("%d keys", n)
	}
	// Once pruned, the old key is free again (and its store exists).
	c.expectError(http.MethodPost, "/v1/stores", map[string]any{"name": "Lidl"}, http.StatusConflict, "store_exists", "Idempotency-Key", "old")
}
