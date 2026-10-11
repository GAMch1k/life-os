package processor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Groceries go to the shopping list, other purchases stay Tasks (the
// owner's decision of 2026-10-11). Codex plans food_add_shopping_items,
// food_add_pantry_items, food_mark_bought, and food_update_pantry_item; the
// two changes of existing items name them only by search terms, like Task
// and Calendar changes.

const (
	// maxFoodActionItems bounds the items one planned add may carry.
	maxFoodActionItems = 30
	// maxFoodName is the longest item name, in characters, as Food takes it.
	maxFoodName = 200
	// foodListLimit and foodListPages bound one read of the shopping list or
	// the pantry when the processor looks for an item.
	foodListLimit = 200
	foodListPages = 10
	// foodCandidateLimit bounds the items a Human Review lists.
	foodCandidateLimit = 10
)

var (
	// foodAisles and pantryLocations are Food's enums.
	foodAisles      = []string{"produce", "bakery", "dairy", "meat_fish", "frozen", "pantry", "drinks", "snacks", "household", "drugstore", "other"}
	pantryLocations = []string{"fridge", "freezer", "pantry", "other"}
	// foodItemFields are what each item of a planned add may carry; the
	// processor adds where it came from (sourceRef).
	foodItemFields = map[string]map[string]bool{
		"food_add_shopping_items": {"name": true, "amount": true, "note": true, "storeName": true, "aisle": true},
		"food_add_pantry_items":   {"name": true, "amount": true, "location": true, "expiresOn": true},
	}
)

// isFoodMutation reports whether an action changes an existing shopping
// list or pantry item, which the processor first has to identify.
func isFoodMutation(action plannedAction) bool {
	return action.Tool == "food_mark_bought" || action.Tool == "food_update_pantry_item"
}

// isFoodWrite reports whether a tool writes to Food.
func isFoodWrite(tool string) bool {
	return tool == "food_add_shopping_items" || tool == "food_add_pantry_items" || tool == "food_mark_bought" || tool == "food_update_pantry_item"
}

// validFoodAction checks a planned Food action beyond its field names: an
// add lists 1 to maxFoodActionItems items, each with a name of at most
// maxFoodName characters, text fields as text, an aisle or location from
// Food's enums, and expiresOn an ISO date; a change names its item by
// search terms, and a pantry change ends the item (used up or thrown away).
func validFoodAction(action plannedAction) bool {
	switch action.Tool {
	case "food_mark_bought":
		return validSearchTerms(action.Arguments, "itemQuery", "itemQueryAlternatives")
	case "food_update_pantry_item":
		state, _ := action.Arguments["state"].(string)
		return validSearchTerms(action.Arguments, "itemQuery", "itemQueryAlternatives") && (state == "used_up" || state == "thrown_away")
	}
	items, ok := action.Arguments["items"].([]any)
	if !ok || len(items) == 0 || len(items) > maxFoodActionItems {
		return false
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		for field, value := range item {
			if !foodItemFields[action.Tool][field] {
				return false
			}
			if _, isText := value.(string); value != nil && !isText {
				return false
			}
		}
		name, _ := item["name"].(string)
		if strings.TrimSpace(name) == "" || utf8.RuneCountInString(strings.TrimSpace(name)) > maxFoodName {
			return false
		}
		if !validChoice(item["aisle"], foodAisles) || !validChoice(item["location"], pantryLocations) {
			return false
		}
		if day, _ := item["expiresOn"].(string); strings.TrimSpace(day) != "" {
			if _, err := time.Parse(time.DateOnly, strings.TrimSpace(day)); err != nil {
				return false
			}
		}
	}
	return true
}

// validChoice accepts a missing or blank value (MCP leaves it out) or one
// of allowed.
func validChoice(value any, allowed []string) bool {
	choice, _ := value.(string)
	choice = strings.TrimSpace(choice)
	return choice == "" || slices.Contains(allowed, choice)
}

// findFoodItems returns the items a Food change may mean: the needed
// shopping list items for food_mark_bought, the in-stock pantry items for
// food_update_pantry_item, whose names its search terms match (see
// matchFoodItems). Food has no name search, so the processor reads the
// whole list (bounded) and matches here. The caller acts only on exactly
// one.
func (r Runner) findFoodItems(ctx context.Context, action plannedAction) ([]map[string]any, error) {
	tool, state := "food_list_shopping_items", "needed"
	if action.Tool == "food_update_pantry_item" {
		tool, state = "food_list_pantry_items", "in_stock"
	}
	var items []map[string]any
	cursor := ""
	for page := 0; ; page++ {
		if page == foodListPages {
			// A partial list could look like exactly one match.
			return nil, fmt.Errorf("%s: more than %d items", tool, foodListPages*foodListLimit)
		}
		arguments := map[string]any{"state": state, "limit": foodListLimit}
		if cursor != "" {
			arguments["cursor"] = cursor
		}
		raw, err := r.Tools.Call(ctx, tool, arguments)
		if err != nil {
			return nil, err
		}
		var result struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"nextCursor"`
		}
		if err := json.Unmarshal(toolData(raw), &result); err != nil {
			return nil, fmt.Errorf("decode %s result: %w", tool, err)
		}
		items = append(items, result.Items...)
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}
	return matchFoodItems(items, searchTerms(action.Arguments, "itemQuery", "itemQueryAlternatives")), nil
}

// matchFoodItems is the items whose name one of the terms names, compared
// without case, diacritics, or punctuation (comparableTitle): the whole name
// ("Milch" is "milch"), or the start of one of its words, so a stem finds
// inflected forms ("молок" finds "Молоко", "банан" finds "Бананы") while a
// short term inside another word does not ("tea" never finds "Steak").
// Items named exactly by a term win over the others: "milk" means "Milk",
// not "Milk chocolate", when both are on the list.
func matchFoodItems(items []map[string]any, terms []string) []map[string]any {
	var exact, partial []map[string]any
	for _, item := range items {
		name := comparableTitle(text(item["name"]))
		named, starts := false, false
		for _, term := range terms {
			term = comparableTitle(term)
			switch {
			case term == "":
			case name == term:
				named = true
			case strings.Contains(" "+name, " "+term):
				starts = true
			}
		}
		if named {
			exact = append(exact, item)
		} else if starts {
			partial = append(partial, item)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return partial
}

// boughtTask is the open Task a purchase may be instead of a list item:
// "I already bought the chicken" with the Task "Buy chicken for dinner" and
// no chicken on the list completes that Task, with the same search terms.
func boughtTask(action plannedAction) (plannedAction, bool) {
	if action.Tool != "food_mark_bought" {
		return plannedAction{}, false
	}
	arguments := map[string]any{}
	copyField(arguments, "taskQuery", action.Arguments, "itemQuery")
	copyField(arguments, "taskQueryAlternatives", action.Arguments, "itemQueryAlternatives")
	other := plannedAction{Tool: "task_complete", Arguments: arguments}
	return other, validAction(other)
}

// approvedFoodTarget takes the item the owner chose in Human Review out of
// an approved Food change. Inbox records the chosen candidate as taskId,
// its one selection field.
func approvedFoodTarget(approved plan) (plan, string) {
	if len(approved.Actions) != 1 || !isFoodMutation(approved.Actions[0]) {
		return approved, ""
	}
	arguments := approved.Actions[0].Arguments
	target, _ := arguments["taskId"].(string)
	delete(arguments, "taskId")
	return approved, target
}

// foodArguments is what the Food MCP tool receives for one resolved item:
// its id and, for a pantry change, the new state. The search terms only
// identified the item.
func foodArguments(tool string, planned map[string]any, itemID string) map[string]any {
	if tool == "food_mark_bought" {
		return map[string]any{"itemIds": []any{itemID}}
	}
	return map[string]any{"itemId": itemID, "state": planned["state"]}
}

// unmatchedFoodReason is recorded, and the capture's other actions still
// run, when nothing matches a Food change: nothing is created instead.
func unmatchedFoodReason(tool string) string {
	if tool == "food_mark_bought" {
		return "Nothing on the shopping list or among the open Tasks matches, so nothing was marked bought"
	}
	return "Nothing in the pantry matches, so nothing was changed"
}

// foodReviewReason tells the owner, in the Human Review queue, why a Food
// change waits.
func foodReviewReason(tool string) string {
	if tool == "food_mark_bought" {
		return "Several shopping list items match: choose the one you bought"
	}
	return "Several pantry items match: choose the one you mean"
}

// foodCandidates is the items a Human Review offers to choose from. The
// Dashboard shows a candidate's title, so it says how much, and where the
// item is bought or kept; the item's own name is kept as name.
func foodCandidates(tool string, found []map[string]any) []map[string]any {
	if len(found) > foodCandidateLimit {
		found = found[:foodCandidateLimit]
	}
	kind, fields := "shopping_item", []string{"amount", "storeName", "aisle", "state"}
	if tool == "food_update_pantry_item" {
		kind, fields = "pantry_item", []string{"amount", "location", "expiresOn", "state"}
	}
	candidates := make([]map[string]any, 0, len(found))
	for _, item := range found {
		candidate := map[string]any{"id": item["id"], "kind": kind, "name": item["name"]}
		parts := []string{text(item["name"])}
		for _, field := range fields {
			value, ok := item[field]
			if !ok || value == nil {
				continue
			}
			candidate[field] = value
			if label := strings.TrimSpace(text(value)); label != "" && field != "aisle" && field != "state" {
				if field == "expiresOn" {
					label = "expires " + label
				}
				parts = append(parts, label)
			}
		}
		candidate["title"] = strings.Join(parts, " · ")
		candidates = append(candidates, candidate)
	}
	return candidates
}

// foodIdempotencyKey is the key of one Food write for an item: its Inbox
// operation, the tool, and what is sent, but not the action's place in the
// plan. A retried capture is planned again, and the new plan may list the
// same write elsewhere (after an action that failed, or in another order);
// it still repeats the write under the same key, so Food answers with the
// first response instead of writing again. Food's merge rule (one needed
// item per name) is the second safety net, for a plan worded differently.
func foodIdempotencyKey(item inboxItem, tool string, arguments map[string]any) string {
	raw, _ := json.Marshal(map[string]any{"operation": idempotencyKey(item), "tool": tool, "arguments": arguments})
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("food-%x", sum[:16])
}
