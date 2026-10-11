package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/mcp/internal/coreclient"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// shoppingItemInput is one thing to buy.
type shoppingItemInput struct {
	Name      string  `json:"name" jsonschema:"The item as the owner wrote it, in their language and without the quantity, such as Milch or яйца"`
	Amount    *string `json:"amount,omitempty" jsonschema:"How much, as free text, such as 6, 2 l, or 500 g"`
	Note      *string `json:"note,omitempty" jsonschema:"A detail about the item"`
	StoreName *string `json:"storeName,omitempty" jsonschema:"The store to buy it at, such as Lidl or dm (case-insensitive); an unknown store is created"`
	Aisle     *string `json:"aisle,omitempty" jsonschema:"produce, bakery, dairy, meat_fish, frozen, pantry, drinks, snacks, household, drugstore, or other; leave out to let the aisle rules place it"`
}

// shoppingAdd puts items on the shopping list. MCP adds them as coming from
// a capture (source capture), named by sourceRef when given.
type shoppingAdd struct {
	IdempotencyKey string              `json:"idempotencyKey,omitempty"`
	SourceRef      string              `json:"sourceRef,omitempty" jsonschema:"The capture the items came from, such as the Inbox item id"`
	Items          []shoppingItemInput `json:"items" jsonschema:"1 to 50 items"`
}

// shoppingQuery lists the shopping list; every field is optional.
type shoppingQuery struct {
	State     string `json:"state,omitempty" jsonschema:"needed or bought"`
	StoreID   string `json:"storeId,omitempty" jsonschema:"Only items tagged with this store (UUID)"`
	StoreName string `json:"storeName,omitempty" jsonschema:"Only items tagged with the store of this name (case-insensitive)"`
	Aisle     string `json:"aisle,omitempty" jsonschema:"Only items in this aisle"`
	Limit     int    `json:"limit,omitempty" jsonschema:"1 to 200 (default 100)"`
	Cursor    string `json:"cursor,omitempty" jsonschema:"The previous result's nextCursor, with the same filters"`
}

// shoppingBought marks shopping items bought.
type shoppingBought struct {
	IdempotencyKey string   `json:"idempotencyKey,omitempty" jsonschema:"Each item's write carries this key followed by a dot and the item id"`
	ItemIDs        []string `json:"itemIds" jsonschema:"Shopping item UUIDs (from food_list_shopping_items), 1 to 50"`
}

// pantryItemInput is one thing the owner has at home.
type pantryItemInput struct {
	Name      string  `json:"name" jsonschema:"The item as the owner wrote it, in their language and without the quantity, such as Joghurt or молоко"`
	Amount    *string `json:"amount,omitempty" jsonschema:"How much, as free text, such as 6, 2 l, or 500 g"`
	Location  *string `json:"location,omitempty" jsonschema:"fridge, freezer, pantry, or other; leave out for the usual place of its kind"`
	ExpiresOn *string `json:"expiresOn,omitempty" jsonschema:"The date it expires, YYYY-MM-DD"`
	OpenedOn  *string `json:"openedOn,omitempty" jsonschema:"The date it was opened, YYYY-MM-DD"`
	Note      *string `json:"note,omitempty" jsonschema:"A detail about the item"`
}

// pantryAdd adds pantry items, as coming from a capture like shoppingAdd.
type pantryAdd struct {
	IdempotencyKey string            `json:"idempotencyKey,omitempty"`
	SourceRef      string            `json:"sourceRef,omitempty" jsonschema:"The capture the items came from, such as the Inbox item id"`
	Items          []pantryItemInput `json:"items" jsonschema:"1 to 50 items"`
}

// pantryMutation changes one pantry item: its state, or the fields given
// (Clear names optional fields to remove, sent to Food as null).
type pantryMutation struct {
	IdempotencyKey string   `json:"idempotencyKey,omitempty"`
	ItemID         string   `json:"itemId" jsonschema:"Pantry item UUID (from food_list_pantry_items)"`
	State          *string  `json:"state,omitempty" jsonschema:"used_up or thrown_away ends the item (kept as history), in_stock puts it back; not together with field changes"`
	Name           *string  `json:"name,omitempty"`
	Amount         *string  `json:"amount,omitempty"`
	Location       *string  `json:"location,omitempty" jsonschema:"fridge, freezer, pantry, or other"`
	ExpiresOn      *string  `json:"expiresOn,omitempty" jsonschema:"YYYY-MM-DD"`
	OpenedOn       *string  `json:"openedOn,omitempty" jsonschema:"YYYY-MM-DD"`
	Note           *string  `json:"note,omitempty"`
	Clear          []string `json:"clear,omitempty" jsonschema:"Fields to remove: amount, expiresOn, openedOn, or note"`
}

// pantryQuery lists the pantry; every field is optional.
type pantryQuery struct {
	State              string `json:"state,omitempty" jsonschema:"in_stock, used_up, or thrown_away"`
	Location           string `json:"location,omitempty" jsonschema:"fridge, freezer, pantry, or other"`
	ExpiringWithinDays *int   `json:"expiringWithinDays,omitempty" jsonschema:"Only in-stock items that expire within this many days of today in the owner's timezone, 0 to 366; already expired ones are included"`
	Limit              int    `json:"limit,omitempty" jsonschema:"1 to 200 (default 100)"`
	Cursor             string `json:"cursor,omitempty" jsonschema:"The previous result's nextCursor, with the same filters"`
}

// recipeQuery finds recipes; every field is optional.
type recipeQuery struct {
	Q          string `json:"q,omitempty" jsonschema:"Words the recipe's title must all contain, also inside a word (bolo finds Pasta Bolognese)"`
	Tag        string `json:"tag,omitempty" jsonschema:"Only recipes with this tag"`
	Ingredient string `json:"ingredient,omitempty" jsonschema:"Only recipes with an ingredient whose name contains this, such as hack for Hackfleisch"`
	Limit      int    `json:"limit,omitempty" jsonschema:"1 to 200 (default 100)"`
	Cursor     string `json:"cursor,omitempty" jsonschema:"The previous result's nextCursor, with the same filters"`
}

// mealPlanning sets one slot of the meal plan to a recipe or free text.
type mealPlanning struct {
	IdempotencyKey string  `json:"idempotencyKey,omitempty"`
	Date           string  `json:"date" jsonschema:"The owner's local date, YYYY-MM-DD"`
	Slot           string  `json:"slot" jsonschema:"breakfast, lunch, or dinner"`
	RecipeID       string  `json:"recipeId,omitempty" jsonschema:"A recipe UUID (from food_list_recipes); give this or text, not both"`
	Text           string  `json:"text,omitempty" jsonschema:"The meal as free text, when no recipe is meant; give this or recipeId, not both"`
	Servings       *int    `json:"servings,omitempty" jsonschema:"For how many people, 1 to 100"`
	Note           *string `json:"note,omitempty" jsonschema:"A note about the meal"`
}

// mealSlot names one slot of the meal plan.
type mealSlot struct {
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	Date           string `json:"date" jsonschema:"The owner's local date, YYYY-MM-DD"`
	Slot           string `json:"slot" jsonschema:"breakfast, lunch, or dinner"`
}

// mealPlanRange is the dates a meal plan read covers.
type mealPlanRange struct {
	From string `json:"from" jsonschema:"The first date, YYYY-MM-DD"`
	To   string `json:"to" jsonschema:"The last date, YYYY-MM-DD; at most 31 days in all"`
}

// missingIngredients puts the ingredients of recipes that are neither at
// home nor already needed on the shopping list.
type missingIngredients struct {
	IdempotencyKey string   `json:"idempotencyKey,omitempty"`
	RecipeIDs      []string `json:"recipeIds,omitempty" jsonschema:"Recipe UUIDs (from food_list_recipes), 1 to 50; or give from and to"`
	From           string   `json:"from,omitempty" jsonschema:"With to: every recipe planned from this date, YYYY-MM-DD"`
	To             string   `json:"to,omitempty" jsonschema:"With from: through this date, at most 31 days in all"`
	DryRun         bool     `json:"dryRun,omitempty" jsonschema:"Only report what would happen; nothing changes"`
}

var (
	// mealSlots are a day's meals, in the order the plan lists them.
	mealSlots = []string{"breakfast", "lunch", "dinner"}
	// foodAisles are the shopping list's sections, in the order it shows them.
	foodAisles = []string{"produce", "bakery", "dairy", "meat_fish", "frozen", "pantry", "drinks", "snacks", "household", "drugstore", "other"}
	// pantryLocations are where pantry items are kept.
	pantryLocations = []string{"fridge", "freezer", "pantry", "other"}
	// pantryStates maps a pantry state to the Food operation that sets it.
	pantryStates = map[string]string{"used_up": "/used-up", "thrown_away": "/thrown-away", "in_stock": "/restock"}
	// clearablePantryFields are the optional pantry fields an update may remove.
	clearablePantryFields = map[string]bool{"amount": true, "expiresOn": true, "openedOn": true, "note": true}
)

const (
	// maxFoodItems is the most items one add or mark-bought call takes, as
	// Food takes in one request; also the most recipes one missing
	// ingredients call takes.
	maxFoodItems = 50
	// planDays is the longest date range the meal plan is read or used for.
	planDays = 31
)

// addFoodTools registers the Food tools. Each is a Food API operation, the
// same one the Dashboard uses; the shopping list's merge rule (one needed
// item per name) and stores given by name are Food's.
func addFoodTools(s *mcp.Server, hc *http.Client, baseURL string, emitter *activity.Emitter) {
	client := coreclient.Client{BaseURL: baseURL, HTTP: hc}
	mcp.AddTool(s, &mcp.Tool{Name: "food_list_shopping_items", Description: "List the shopping list: needed items first, by aisle, then bought items, the most recently bought first. Filters: state (needed or bought), storeId or storeName (one store's items), aisle; limit and cursor page"}, func(ctx context.Context, req *mcp.CallToolRequest, in shoppingQuery) (*mcp.CallToolResult, Output, error) {
		headers := http.Header{}
		traceOf(req).Apply(headers)
		query, known, err := shoppingListQuery(ctx, client, headers, in)
		if err != nil {
			return nil, Output{}, err
		}
		if !known {
			// No store has that name, so no item is tagged with it.
			return nil, Output{Data: map[string]any{"items": []any{}}}, nil
		}
		return read(ctx, client, headers, "/v1/shopping-items?"+query)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "food_add_shopping_items", Description: "Put groceries (food, drinks, household, and drugstore items) on the shopping list, 1 to 50 at once; other purchases are Tasks. Each item is a name as the owner wrote it, with an optional amount, note, storeName (an unknown store is created), and aisle (left out: the aisle rules place it). An item whose name is already needed merges into that item instead of being added twice; the response lists {item, merged} for each, in order"}, func(ctx context.Context, req *mcp.CallToolRequest, in shoppingAdd) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "food_add_shopping_items", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			body, err := shoppingAddBody(in)
			if err != nil {
				return nil, Output{}, err
			}
			return post(ctx, hc, trace, baseURL, "/v1/shopping-items", body, in.IdempotencyKey)
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "food_mark_bought", Description: "Mark shopping list items bought, by id (from food_list_shopping_items); an item already bought stays as it is. Returns the items in the order given"}, func(ctx context.Context, req *mcp.CallToolRequest, in shoppingBought) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "food_mark_bought", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			ids, err := shoppingItemIDs(in.ItemIDs)
			if err != nil {
				return nil, Output{}, err
			}
			items := make([]any, 0, len(ids))
			for _, id := range ids {
				// One write per item, each with its own key, so a retry
				// replays the items already marked and marks the rest.
				key := ""
				if in.IdempotencyKey != "" {
					key = in.IdempotencyKey + "." + id
				}
				_, out, err := post(ctx, hc, trace, baseURL, "/v1/shopping-items/"+id+"/bought", nil, key)
				if err != nil {
					return nil, Output{}, err
				}
				items = append(items, out.Data)
			}
			return nil, Output{Data: map[string]any{"items": items}}, nil
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "food_list_pantry_items", Description: "List the pantry: in-stock items first, the soonest to expire first, then ended (used up or thrown away) items. Filters: state (in_stock, used_up, or thrown_away), location (fridge, freezer, pantry, or other), expiringWithinDays (in-stock items expiring within that many days, already expired ones included); limit and cursor page"}, func(ctx context.Context, req *mcp.CallToolRequest, in pantryQuery) (*mcp.CallToolResult, Output, error) {
		query, err := pantryListQuery(in)
		if err != nil {
			return nil, Output{}, err
		}
		headers := http.Header{}
		traceOf(req).Apply(headers)
		return read(ctx, client, headers, "/v1/pantry-items?"+query)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "food_add_pantry_items", Description: "Add what the owner has at home to the pantry, 1 to 50 at once: each a name as the owner wrote it, with an optional amount, location (fridge, freezer, pantry, or other; left out: the usual place of its kind), expiresOn and openedOn (YYYY-MM-DD), and note. Pantry items never merge: two cartons of milk may expire on different days"}, func(ctx context.Context, req *mcp.CallToolRequest, in pantryAdd) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "food_add_pantry_items", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			body, err := pantryAddBody(in)
			if err != nil {
				return nil, Output{}, err
			}
			return post(ctx, hc, trace, baseURL, "/v1/pantry-items", body, in.IdempotencyKey)
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "food_list_recipes", Description: "Find recipes, by title: q keeps those whose title contains every word of it (also inside a word), tag those with the tag, ingredient those with an ingredient whose name contains it; limit and cursor page. Each recipe has its title, normalizedTitle, servings, ingredients, steps, and tags"}, func(ctx context.Context, req *mcp.CallToolRequest, in recipeQuery) (*mcp.CallToolResult, Output, error) {
		values := url.Values{}
		for name, value := range map[string]string{"q": in.Q, "tag": in.Tag, "ingredient": in.Ingredient} {
			if value = strings.TrimSpace(value); value != "" {
				values.Set(name, value)
			}
		}
		if err := setPage(values, in.Limit, in.Cursor); err != nil {
			return nil, Output{}, err
		}
		headers := http.Header{}
		traceOf(req).Apply(headers)
		return read(ctx, client, headers, "/v1/recipes?"+values.Encode())
	})
	mcp.AddTool(s, &mcp.Tool{Name: "food_list_meal_plan", Description: "List the meal plan from one date through another (YYYY-MM-DD, at most 31 days), by date, then slot (breakfast, lunch, dinner); empty slots are not listed. Each meal is a recipe (recipeId) or free text, with its title"}, func(ctx context.Context, req *mcp.CallToolRequest, in mealPlanRange) (*mcp.CallToolResult, Output, error) {
		from, to, err := planRange(in.From, in.To)
		if err != nil {
			return nil, Output{}, err
		}
		headers := http.Header{}
		traceOf(req).Apply(headers)
		return read(ctx, client, headers, "/v1/meal-plan?"+url.Values{"from": {from}, "to": {to}}.Encode())
	})
	mcp.AddTool(s, &mcp.Tool{Name: "food_plan_meal", Description: "Plan a meal: set a day's breakfast, lunch, or dinner to a recipe (recipeId, from food_list_recipes) or to free text (text), exactly one, with optional servings and note. What the slot held is replaced"}, func(ctx context.Context, req *mcp.CallToolRequest, in mealPlanning) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "food_plan_meal", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			path, err := mealPath(in.Date, in.Slot)
			if err != nil {
				return nil, Output{}, err
			}
			body, err := mealBody(in)
			if err != nil {
				return nil, Output{}, err
			}
			return put(ctx, hc, trace, baseURL, path, body, in.IdempotencyKey)
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "food_clear_meal", Description: "Clear a day's breakfast, lunch, or dinner from the meal plan. A slot with nothing planned is left as it is (cleared: false), not an error"}, func(ctx context.Context, req *mcp.CallToolRequest, in mealSlot) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "food_clear_meal", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			path, err := mealPath(in.Date, in.Slot)
			if err != nil {
				return nil, Output{}, err
			}
			headers := http.Header{}
			trace.Apply(headers)
			if in.IdempotencyKey != "" {
				headers.Set("Idempotency-Key", in.IdempotencyKey)
			}
			cleared := true
			// Food answers 204 with no body, or 404 when nothing is planned.
			if _, err := client.DoWithHeaders(ctx, http.MethodDelete, path, nil, headers); err != nil {
				if !strings.Contains(err.Error(), "service returned 404") || !strings.Contains(err.Error(), "not_found") {
					return nil, Output{}, err
				}
				cleared = false
			}
			return nil, Output{Data: map[string]any{"date": strings.TrimSpace(in.Date), "slot": strings.TrimSpace(in.Slot), "cleared": cleared}}, nil
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "food_add_missing_ingredients", Description: "Put the ingredients of recipes on the shopping list that are neither in stock in the pantry nor already needed: of the recipes given (recipeIds) or of every recipe planned from one date through another (from, to; at most 31 days). One line per ingredient name, the amounts joined. dryRun only reports. Returns each ingredient's status: added, merged, in_pantry, or on_list"}, func(ctx context.Context, req *mcp.CallToolRequest, in missingIngredients) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "food_add_missing_ingredients", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			body, err := missingIngredientsBody(in)
			if err != nil {
				return nil, Output{}, err
			}
			return post(ctx, hc, trace, baseURL, "/v1/shopping-items/from-recipes", body, in.IdempotencyKey)
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "food_update_pantry_item", Description: "Change one pantry item, by id (from food_list_pantry_items): state used_up or thrown_away ends it (it stays as history), in_stock puts it back; or, in a separate call, change its name, amount, location, expiresOn, openedOn, or note, and clear removes amount, expiresOn, openedOn, or note. Returns the item with restock: what its staple put on the shopping list by itself (status added, merged, or on_list, and message), absent or null when nothing did; do not add that item to the list again"}, func(ctx context.Context, req *mcp.CallToolRequest, in pantryMutation) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "food_update_pantry_item", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			id, err := uuid.Parse(strings.TrimSpace(in.ItemID))
			if err != nil {
				return nil, Output{}, errors.New("itemId must be a pantry item UUID")
			}
			path := "/v1/pantry-items/" + id.String()
			body, err := pantryUpdateBody(in)
			if err != nil {
				return nil, Output{}, err
			}
			if in.State != nil {
				return post(ctx, hc, trace, baseURL, path+pantryStates[strings.TrimSpace(*in.State)], nil, in.IdempotencyKey)
			}
			return patch(ctx, hc, trace, baseURL, path, body, in.IdempotencyKey)
		})
	})
	addStapleTools(s, hc, baseURL, emitter)
}

// read GETs path from Food and returns its JSON object.
func read(ctx context.Context, client coreclient.Client, headers http.Header, path string) (*mcp.CallToolResult, Output, error) {
	body, err := client.DoWithHeaders(ctx, http.MethodGet, path, nil, headers)
	if err != nil {
		return nil, Output{}, err
	}
	out, err := output(body)
	return nil, out, err
}

// shoppingListQuery is the Food list query for a shopping list request,
// checked so a caller cannot reach another parameter. A store given by name
// is looked up among all stores (archived ones too); known is false when
// none has the name.
func shoppingListQuery(ctx context.Context, client coreclient.Client, headers http.Header, in shoppingQuery) (query string, known bool, err error) {
	values := url.Values{}
	if state := strings.TrimSpace(in.State); state != "" {
		if state != "needed" && state != "bought" {
			return "", false, errors.New("state must be needed or bought")
		}
		values.Set("state", state)
	}
	if aisle := strings.TrimSpace(in.Aisle); aisle != "" {
		if !slices.Contains(foodAisles, aisle) {
			return "", false, fmt.Errorf("aisle must be one of %s", strings.Join(foodAisles, ", "))
		}
		values.Set("aisle", aisle)
	}
	storeID, storeName := strings.TrimSpace(in.StoreID), strings.TrimSpace(in.StoreName)
	switch {
	case storeID != "" && storeName != "":
		return "", false, errors.New("give the store by storeId or by storeName, not both")
	case storeID != "":
		id, err := uuid.Parse(storeID)
		if err != nil {
			return "", false, errors.New("storeId must be a store UUID; give a name as storeName")
		}
		values.Set("storeId", id.String())
	case storeName != "":
		id, err := storeNamed(ctx, client, headers, storeName)
		if err != nil || id == "" {
			return "", false, err
		}
		values.Set("storeId", id)
	}
	if err := setPage(values, in.Limit, in.Cursor); err != nil {
		return "", false, err
	}
	return values.Encode(), true, nil
}

// storeNamed is the id of the store called name (compared like Food
// compares names, without case or extra spacing), or "" when there is none.
func storeNamed(ctx context.Context, client coreclient.Client, headers http.Header, name string) (string, error) {
	_, out, err := read(ctx, client, headers, "/v1/stores?includeArchived=true")
	if err != nil {
		return "", err
	}
	stores, _ := out.Data["items"].([]any)
	for _, raw := range stores {
		store, _ := raw.(map[string]any)
		if storeName, _ := store["name"].(string); sameName(storeName, name) {
			id, _ := store["id"].(string)
			return id, nil
		}
	}
	return "", nil
}

// sameName compares two names without case, "ё", or extra spacing.
func sameName(a, b string) bool {
	fold := func(s string) string {
		return strings.ReplaceAll(strings.ToLower(strings.Join(strings.Fields(s), " ")), "ё", "е")
	}
	return fold(a) == fold(b)
}

// setPage adds a list request's limit (1 to 200) and cursor.
func setPage(values url.Values, limit int, cursor string) error {
	if limit != 0 {
		if limit < 1 || limit > 200 {
			return errors.New("limit must be between 1 and 200")
		}
		values.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		values.Set("cursor", cursor)
	}
	return nil
}

// pantryListQuery is the Food list query for a pantry request, checked like
// shoppingListQuery.
func pantryListQuery(in pantryQuery) (string, error) {
	values := url.Values{}
	if state := strings.TrimSpace(in.State); state != "" {
		if _, ok := pantryStates[state]; !ok {
			return "", errors.New("state must be in_stock, used_up, or thrown_away")
		}
		values.Set("state", state)
	}
	if location := strings.TrimSpace(in.Location); location != "" {
		if !slices.Contains(pantryLocations, location) {
			return "", fmt.Errorf("location must be one of %s", strings.Join(pantryLocations, ", "))
		}
		values.Set("location", location)
	}
	if in.ExpiringWithinDays != nil {
		if days := *in.ExpiringWithinDays; days < 0 || days > 366 {
			return "", errors.New("expiringWithinDays must be between 0 and 366")
		}
		values.Set("expiringWithinDays", strconv.Itoa(*in.ExpiringWithinDays))
	}
	if err := setPage(values, in.Limit, in.Cursor); err != nil {
		return "", err
	}
	return values.Encode(), nil
}

// shoppingAddBody is the Food add body for shopping items, checked before
// Food is called. Every item comes from a capture.
func shoppingAddBody(in shoppingAdd) (map[string]any, error) {
	if len(in.Items) == 0 || len(in.Items) > maxFoodItems {
		return nil, fmt.Errorf("items must list 1 to %d items", maxFoodItems)
	}
	items := make([]map[string]any, 0, len(in.Items))
	for i, item := range in.Items {
		body, err := capturedItem(i, item.Name, in.SourceRef)
		if err != nil {
			return nil, err
		}
		setText(body, map[string]*string{"amount": item.Amount, "note": item.Note, "storeName": item.StoreName})
		if err := setChoice(body, "aisle", item.Aisle, foodAisles, i); err != nil {
			return nil, err
		}
		items = append(items, body)
	}
	return map[string]any{"items": items}, nil
}

// pantryAddBody is the Food add body for pantry items, checked like
// shoppingAddBody.
func pantryAddBody(in pantryAdd) (map[string]any, error) {
	if len(in.Items) == 0 || len(in.Items) > maxFoodItems {
		return nil, fmt.Errorf("items must list 1 to %d items", maxFoodItems)
	}
	items := make([]map[string]any, 0, len(in.Items))
	for i, item := range in.Items {
		body, err := capturedItem(i, item.Name, in.SourceRef)
		if err != nil {
			return nil, err
		}
		setText(body, map[string]*string{"amount": item.Amount, "note": item.Note})
		if err := setChoice(body, "location", item.Location, pantryLocations, i); err != nil {
			return nil, err
		}
		for field, value := range map[string]*string{"expiresOn": item.ExpiresOn, "openedOn": item.OpenedOn} {
			if value != nil && strings.TrimSpace(*value) != "" {
				day := strings.TrimSpace(*value)
				if _, err := time.Parse(time.DateOnly, day); err != nil {
					return nil, fmt.Errorf("items[%d].%s must be a date such as 2026-10-16", i, field)
				}
				body[field] = day
			}
		}
		items = append(items, body)
	}
	return map[string]any{"items": items}, nil
}

// capturedItem starts the Food body of item i: its name, and that it came
// from a capture (sourceRef when known).
func capturedItem(i int, name, sourceRef string) (map[string]any, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("items[%d].name must not be empty", i)
	}
	body := map[string]any{"name": name, "source": "capture"}
	if ref := strings.TrimSpace(sourceRef); ref != "" {
		body["sourceRef"] = ref
	}
	return body, nil
}

// setText adds the given, non-blank text fields, trimmed; Food checks their
// lengths.
func setText(body map[string]any, fields map[string]*string) {
	for field, value := range fields {
		if value != nil && strings.TrimSpace(*value) != "" {
			body[field] = strings.TrimSpace(*value)
		}
	}
}

// setChoice adds an enum field of item i when it is given and allowed.
func setChoice(body map[string]any, field string, value *string, allowed []string, i int) error {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	if !slices.Contains(allowed, strings.TrimSpace(*value)) {
		return fmt.Errorf("items[%d].%s must be one of %s", i, field, strings.Join(allowed, ", "))
	}
	body[field] = strings.TrimSpace(*value)
	return nil
}

// shoppingItemIDs checks that every id is a UUID, so a caller cannot reach
// another path, and keeps each once.
func shoppingItemIDs(raw []string) ([]string, error) {
	if len(raw) == 0 || len(raw) > maxFoodItems {
		return nil, fmt.Errorf("itemIds must list 1 to %d shopping item ids", maxFoodItems)
	}
	ids := make([]string, 0, len(raw))
	for _, value := range raw {
		id, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil {
			return nil, errors.New("itemIds must be shopping item UUIDs (from food_list_shopping_items)")
		}
		if !slices.Contains(ids, id.String()) {
			ids = append(ids, id.String())
		}
	}
	return ids, nil
}

// pantryUpdateBody checks a pantry change and returns the Food PATCH body
// for field changes (nil for a state change): the given fields, plus null
// for each cleared one.
func pantryUpdateBody(in pantryMutation) (map[string]any, error) {
	body := map[string]any{}
	for field, value := range map[string]*string{"name": in.Name, "amount": in.Amount, "note": in.Note} {
		if value != nil {
			body[field] = strings.TrimSpace(*value)
		}
	}
	if name, ok := body["name"]; ok && name == "" {
		return nil, errors.New("name must not be empty")
	}
	if in.Location != nil {
		if !slices.Contains(pantryLocations, strings.TrimSpace(*in.Location)) {
			return nil, fmt.Errorf("location must be one of %s", strings.Join(pantryLocations, ", "))
		}
		body["location"] = strings.TrimSpace(*in.Location)
	}
	for field, value := range map[string]*string{"expiresOn": in.ExpiresOn, "openedOn": in.OpenedOn} {
		if value != nil {
			day := strings.TrimSpace(*value)
			if _, err := time.Parse(time.DateOnly, day); err != nil {
				return nil, fmt.Errorf("%s must be a date such as 2026-10-16; clear removes it", field)
			}
			body[field] = day
		}
	}
	for _, field := range in.Clear {
		if !clearablePantryFields[field] {
			return nil, fmt.Errorf("cannot clear %q: only amount, expiresOn, openedOn, and note can be cleared", field)
		}
		if _, set := body[field]; set {
			return nil, fmt.Errorf("%s is both set and cleared", field)
		}
		body[field] = nil
	}
	if in.State != nil {
		if _, ok := pantryStates[strings.TrimSpace(*in.State)]; !ok {
			return nil, errors.New("state must be used_up, thrown_away, or in_stock")
		}
		if len(body) > 0 {
			return nil, errors.New("change the state and the fields in separate calls")
		}
		return nil, nil
	}
	if len(body) == 0 {
		return nil, errors.New("nothing to change: give a state, or at least one field to set or clear")
	}
	return body, nil
}

// planRange checks a meal plan date range: ISO dates, to not before from,
// at most planDays days in all.
func planRange(from, to string) (string, string, error) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	start, startErr := time.Parse(time.DateOnly, from)
	end, endErr := time.Parse(time.DateOnly, to)
	switch {
	case startErr != nil || endErr != nil:
		return "", "", errors.New("from and to must be dates such as 2026-10-12")
	case end.Before(start):
		return "", "", errors.New("to must not be before from")
	case end.Sub(start) >= planDays*24*time.Hour:
		return "", "", fmt.Errorf("the range must be at most %d days", planDays)
	}
	return from, to, nil
}

// mealPath is the Food path of one meal plan slot. The date must be a date
// and the slot a slot, so a caller cannot reach another path.
func mealPath(date, slot string) (string, error) {
	date, slot = strings.TrimSpace(date), strings.TrimSpace(slot)
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		return "", errors.New("date must be a date such as 2026-10-14")
	}
	if !slices.Contains(mealSlots, slot) {
		return "", errors.New("slot must be breakfast, lunch, or dinner")
	}
	return "/v1/meal-plan/" + date + "/" + slot, nil
}

// mealBody is the Food body that sets a slot: a recipe or free text,
// exactly one, with optional servings and note.
func mealBody(in mealPlanning) (map[string]any, error) {
	recipe, text := strings.TrimSpace(in.RecipeID), strings.TrimSpace(in.Text)
	if (recipe == "") == (text == "") {
		return nil, errors.New("give recipeId or text, exactly one")
	}
	body := map[string]any{}
	if recipe != "" {
		id, err := uuid.Parse(recipe)
		if err != nil {
			return nil, errors.New("recipeId must be a recipe UUID (from food_list_recipes); give a meal without one as text")
		}
		body["recipeId"] = id.String()
	} else {
		body["text"] = text
	}
	if in.Servings != nil {
		if *in.Servings < 1 || *in.Servings > 100 {
			return nil, errors.New("servings must be between 1 and 100")
		}
		body["servings"] = *in.Servings
	}
	setText(body, map[string]*string{"note": in.Note})
	return body, nil
}

// missingIngredientsBody is the Food body for missing ingredients: recipe
// ids (each once) or a date range, not both.
func missingIngredientsBody(in missingIngredients) (map[string]any, error) {
	ranged := strings.TrimSpace(in.From) != "" || strings.TrimSpace(in.To) != ""
	if (len(in.RecipeIDs) > 0) == ranged {
		return nil, errors.New("give recipeIds, or from and to, not both")
	}
	body := map[string]any{}
	if ranged {
		from, to, err := planRange(in.From, in.To)
		if err != nil {
			return nil, err
		}
		body["from"], body["to"] = from, to
	} else {
		if len(in.RecipeIDs) > maxFoodItems {
			return nil, fmt.Errorf("recipeIds must list 1 to %d recipes", maxFoodItems)
		}
		ids := make([]string, 0, len(in.RecipeIDs))
		for _, value := range in.RecipeIDs {
			id, err := uuid.Parse(strings.TrimSpace(value))
			if err != nil {
				return nil, errors.New("recipeIds must be recipe UUIDs (from food_list_recipes)")
			}
			if !slices.Contains(ids, id.String()) {
				ids = append(ids, id.String())
			}
		}
		body["recipeIds"] = ids
	}
	if in.DryRun {
		body["dryRun"] = true
	}
	return body, nil
}
