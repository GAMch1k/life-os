package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// shoppingMutation changes one shopping list item: the fields given, and
// Clear names optional fields to remove (sent to Food as null).
type shoppingMutation struct {
	IdempotencyKey string   `json:"idempotencyKey,omitempty"`
	ItemID         string   `json:"itemId" jsonschema:"Shopping item UUID (from food_list_shopping_items, or what food_add_shopping_items returned)"`
	Name           *string  `json:"name,omitempty" jsonschema:"The new name, as the owner wrote it and without the quantity, such as Hafermilch or oat milk"`
	Amount         *string  `json:"amount,omitempty" jsonschema:"How much, as free text, such as 6, 2 l, or 500 g"`
	Note           *string  `json:"note,omitempty" jsonschema:"A detail about the item"`
	StoreName      *string  `json:"storeName,omitempty" jsonschema:"The store to buy it at, one Food has, such as Lidl or dm (case-insensitive)"`
	Aisle          *string  `json:"aisle,omitempty" jsonschema:"produce, bakery, dairy, meat_fish, frozen, pantry, drinks, snacks, household, drugstore, or other"`
	Clear          []string `json:"clear,omitempty" jsonschema:"Fields to remove: amount, note, or store"`
}

// shoppingClearable maps the optional shopping item fields an update may
// remove to the Food field set to null.
var shoppingClearable = map[string]string{"amount": "amount", "note": "note", "store": "storeId"}

// addShoppingUpdateTool registers food_update_shopping_item, Food's PATCH
// of one shopping list item (the same operation the Dashboard's edit uses).
func addShoppingUpdateTool(s *mcp.Server, hc *http.Client, baseURL string, emitter *activity.Emitter) {
	mcp.AddTool(s, &mcp.Tool{Name: "food_update_shopping_item", Description: "Change one shopping list item, by id (from food_list_shopping_items or food_add_shopping_items): rename it (name), or change its amount, note, storeName (a store Food has), or aisle; clear removes the amount, note, or store. A new name that another needed item already has is refused (409 already_on_list). Returns the item"}, func(ctx context.Context, req *mcp.CallToolRequest, in shoppingMutation) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "food_update_shopping_item", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			id, err := uuid.Parse(strings.TrimSpace(in.ItemID))
			if err != nil {
				return nil, Output{}, errors.New("itemId must be a shopping item UUID")
			}
			body, err := shoppingUpdateBody(in)
			if err != nil {
				return nil, Output{}, err
			}
			return patch(ctx, hc, trace, baseURL, "/v1/shopping-items/"+id.String(), body, in.IdempotencyKey)
		})
	})
}

// shoppingUpdateBody is the Food update body for one shopping item,
// checked before Food is called: something changes, a name is not empty,
// text fits Food's limits, the aisle is one of Food's, and nothing is both
// set and cleared.
func shoppingUpdateBody(in shoppingMutation) (map[string]any, error) {
	body := map[string]any{}
	limits := map[string]int{"name": maxShoppingName, "amount": 50, "note": 1000, "storeName": 100}
	for field, value := range map[string]*string{"name": in.Name, "amount": in.Amount, "note": in.Note, "storeName": in.StoreName} {
		if value == nil {
			continue
		}
		text := strings.TrimSpace(*value)
		if text == "" {
			return nil, fmt.Errorf("%s must not be empty; clear removes it", field)
		}
		if utf8.RuneCountInString(text) > limits[field] {
			return nil, fmt.Errorf("%s must be at most %d characters", field, limits[field])
		}
		body[field] = text
	}
	if in.Aisle != nil {
		aisle := strings.TrimSpace(*in.Aisle)
		if !slices.Contains(foodAisles, aisle) {
			return nil, fmt.Errorf("aisle must be one of %s", strings.Join(foodAisles, ", "))
		}
		body["aisle"] = aisle
	}
	for _, field := range in.Clear {
		target, ok := shoppingClearable[field]
		if !ok {
			return nil, fmt.Errorf("cannot clear %q: only amount, note, and store can be cleared", field)
		}
		if _, set := body[field]; set || (field == "store" && in.StoreName != nil) {
			return nil, fmt.Errorf("%s is both set and cleared", field)
		}
		body[target] = nil
	}
	if len(body) == 0 {
		return nil, errors.New("nothing to change: give at least one field to set or clear")
	}
	return body, nil
}

// maxShoppingName is the longest shopping item name Food takes.
const maxShoppingName = 200
