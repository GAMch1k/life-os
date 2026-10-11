package processor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Groceries go to the shopping list, other purchases stay Tasks (the
// owner's decision of 2026-10-11). Codex plans food_add_shopping_items,
// food_add_pantry_items, food_mark_bought, and food_update_pantry_item; the
// two changes of existing items name them only by search terms, like Task
// and Calendar changes. Staples and running low are in staples.go. Meals are planned with food_plan_meal and cleared
// with food_clear_meal, and food_add_missing_ingredients puts what recipes
// need on the list; a recipe is named only by words (recipeQuery), which
// the processor resolves.

const (
	// maxFoodActionItems bounds the items one planned add may carry.
	maxFoodActionItems = 30
	// maxFoodName is the longest item name, in characters, as Food takes it.
	maxFoodName = 200
	// foodListLimit and foodListPages bound one read of the shopping list or
	// the pantry when the processor looks for an item.
	foodListLimit = 200
	foodListPages = 10
	// foodCandidateLimit bounds the items or recipes a Human Review lists.
	foodCandidateLimit = 10
	// recipeSearchLimit bounds each recipe search.
	recipeSearchLimit = 50
	// maxMealText is the longest free-text meal, in characters, as Food
	// takes it: a meal no recipe matches is planned as its words.
	maxMealText = 200
	// planDays is the longest date range the meal plan is used for.
	planDays = 31
)

var (
	// foodAisles and pantryLocations are Food's enums.
	foodAisles      = []string{"produce", "bakery", "dairy", "meat_fish", "frozen", "pantry", "drinks", "snacks", "household", "drugstore", "other"}
	pantryLocations = []string{"fridge", "freezer", "pantry", "other"}
	// mealSlots are a day's meals.
	mealSlots = []string{"breakfast", "lunch", "dinner"}
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
	return tool == "food_add_shopping_items" || tool == "food_add_pantry_items" || tool == "food_mark_bought" || tool == "food_update_pantry_item" || isMealTool(tool) || isStapleTool(tool)
}

// isMealTool reports whether a tool plans or clears meals, or adds what
// recipes need.
func isMealTool(tool string) bool {
	return tool == "food_plan_meal" || tool == "food_clear_meal" || tool == "food_add_missing_ingredients"
}

// isRecipeAction reports whether an action names a recipe by words, which
// the processor first resolves: every meal planned, and the missing
// ingredients of one recipe.
func isRecipeAction(action plannedAction) bool {
	if action.Tool == "food_plan_meal" {
		return true
	}
	_, named := action.Arguments["recipeQuery"]
	return action.Tool == "food_add_missing_ingredients" && named
}

// validFoodAction checks a planned Food action beyond its field names: an
// add lists 1 to maxFoodActionItems items, each with a name of at most
// maxFoodName characters, text fields as text, an aisle or location from
// Food's enums, and expiresOn an ISO date; a change names its item by
// search terms, and a pantry change ends the item (used up or thrown away).
func validFoodAction(action plannedAction) bool {
	if isMealTool(action.Tool) {
		return validMealAction(action)
	}
	if isStapleTool(action.Tool) {
		return validStapleAction(action)
	}
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

// validMealAction checks a planned meal action: a meal names its recipe by
// a recipeQuery of at most maxMealText characters (planned as text when no
// recipe matches), an ISO date, a slot, and optionally servings (1 to 100)
// and a note; a cleared meal its date and slot; missing ingredients either
// one recipe by words, or a week (this or next), or a range of ISO dates of
// at most planDays days.
func validMealAction(action plannedAction) bool {
	arguments := action.Arguments
	day := func(field string) bool {
		value, _ := arguments[field].(string)
		_, err := time.Parse(time.DateOnly, value)
		return err == nil
	}
	slot, _ := arguments["slot"].(string)
	recipeNamed := validSearchTerms(arguments, "recipeQuery", "recipeQueryAlternatives") && utf8.RuneCountInString(strings.TrimSpace(text(arguments["recipeQuery"]))) <= maxMealText
	switch action.Tool {
	case "food_clear_meal":
		return day("date") && slices.Contains(mealSlots, slot)
	case "food_plan_meal":
		if value, present := arguments["servings"]; present && value != nil {
			servings, ok := value.(float64)
			if !ok || servings != float64(int(servings)) || servings < 1 || servings > 100 {
				return false
			}
		}
		if value, present := arguments["note"]; present && value != nil {
			if _, ok := value.(string); !ok {
				return false
			}
		}
		return recipeNamed && day("date") && slices.Contains(mealSlots, slot)
	}
	_, named := arguments["recipeQuery"]
	week, weekly := arguments["week"]
	_, from := arguments["from"]
	_, to := arguments["to"]
	switch {
	case named && !weekly && !from && !to:
		return recipeNamed
	case weekly && !named && !from && !to:
		return week == "this" || week == "next"
	case from && to && !named && !weekly:
		start, end := text(arguments["from"]), text(arguments["to"])
		first, firstErr := time.Parse(time.DateOnly, start)
		last, lastErr := time.Parse(time.DateOnly, end)
		return firstErr == nil && lastErr == nil && !last.Before(first) && last.Sub(first) < planDays*24*time.Hour
	}
	return false
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
	items, err := r.listFoodItems(ctx, tool, state)
	if err != nil {
		return nil, err
	}
	return matchFoodItems(items, searchTerms(action.Arguments, "itemQuery", "itemQueryAlternatives")), nil
}

// listFoodItems reads the whole shopping list or pantry in a state, page by
// page, within foodListPages pages.
func (r Runner) listFoodItems(ctx context.Context, tool, state string) ([]map[string]any, error) {
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
			return items, nil
		}
		cursor = result.NextCursor
	}
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

// approvedFoodTarget takes the item, recipe, or staple the owner chose in
// Human Review out of an approved Food change, meal, missing ingredients
// request, or running-low or staple change. Inbox records the chosen candidate as taskId,
// its one selection field.
func approvedFoodTarget(approved plan) (plan, string) {
	if len(approved.Actions) != 1 || !(isFoodMutation(approved.Actions[0]) || isRecipeAction(approved.Actions[0]) || isNamedFoodAction(approved.Actions[0])) {
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

// noSlotReason is the Human Review reason for a meal planned or cleared
// without saying which meal of the day it is.
const noSlotReason = "Say which meal it is: breakfast, lunch, or dinner."

// missingSlot reports whether a planned or cleared meal names no slot.
func missingSlot(action plannedAction) bool {
	return (action.Tool == "food_plan_meal" || action.Tool == "food_clear_meal") && strings.TrimSpace(text(action.Arguments["slot"])) == ""
}

// foodNormalize is a name or title as Food compares it (Food's
// domain.Normalize): Unicode NFKC, lower case, "ё" as "е", runs of
// whitespace as one space, and no surrounding whitespace or punctuation.
func foodNormalize(name string) string {
	normalized := strings.ToLower(norm.NFKC.String(name))
	normalized = strings.ReplaceAll(normalized, "ё", "е")
	normalized = strings.Join(strings.Fields(normalized), " ")
	return strings.TrimFunc(normalized, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
}

// findRecipes returns the recipes a meal's words may mean, the way Food
// documents it: a recipe whose normalised title equals one of the terms
// (recipeQuery and its alternatives) is the one, since titles are unique;
// otherwise every recipe a q search with any term finds (its title
// contains all the term's words). The caller plans the one recipe, asks
// the owner about several, and plans the words as text for none.
func (r Runner) findRecipes(ctx context.Context, action plannedAction) ([]map[string]any, error) {
	terms := searchTerms(action.Arguments, "recipeQuery", "recipeQueryAlternatives")
	named := map[string]bool{}
	var found, exact []map[string]any
	seen := map[string]bool{}
	for _, term := range terms {
		named[foodNormalize(term)] = true
		raw, err := r.Tools.Call(ctx, "food_list_recipes", map[string]any{"q": term, "limit": recipeSearchLimit})
		if err != nil {
			return nil, err
		}
		var result struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(toolData(raw), &result); err != nil {
			// A partial union could look like exactly one match.
			return nil, fmt.Errorf("decode recipe search result: %w", err)
		}
		for _, recipe := range result.Items {
			if id, ok := recipe["id"].(string); ok && id != "" && !seen[id] {
				seen[id] = true
				found = append(found, recipe)
			}
		}
	}
	for _, recipe := range found {
		title := text(recipe["normalizedTitle"])
		if title == "" {
			title = foodNormalize(text(recipe["title"]))
		}
		if named[title] {
			exact = append(exact, recipe)
		}
	}
	if len(exact) > 0 {
		return exact, nil
	}
	return found, nil
}

// recipeCandidates is the recipes a Human Review offers to choose from,
// labelled with their tags; the recipe's own title is kept as recipeTitle.
func recipeCandidates(found []map[string]any) []map[string]any {
	if len(found) > foodCandidateLimit {
		found = found[:foodCandidateLimit]
	}
	candidates := make([]map[string]any, 0, len(found))
	for _, recipe := range found {
		label := text(recipe["title"])
		var tags []string
		raw, _ := recipe["tags"].([]any)
		for _, tag := range raw {
			if tag, ok := tag.(string); ok && tag != "" {
				tags = append(tags, tag)
			}
		}
		if len(tags) > 0 {
			label += " · " + strings.Join(tags, ", ")
		}
		candidates = append(candidates, map[string]any{"id": recipe["id"], "kind": "recipe", "title": label, "recipeTitle": recipe["title"]})
	}
	return candidates
}

// recipeReviewReason tells the owner why a meal or its ingredients wait.
func recipeReviewReason(tool string) string {
	if tool == "food_plan_meal" {
		return "Several recipes match: choose the one to plan"
	}
	return "Several recipes match: choose the one whose ingredients to add"
}

// noRecipeReason is the Human Review reason when no recipe matches the
// words of a missing ingredients request: the owner asked for groceries,
// so the capture waits for them instead of doing nothing.
func noRecipeReason(query string) string {
	return fmt.Sprintf("No recipe is called “%s”. Add the recipe on the Food page, or say which items to buy.", strings.TrimSpace(query))
}

// mealArguments is what a meal MCP tool receives: a meal's date and slot
// with the recipe (recipeId) or, when none matched, its words as text, and
// its servings and note; missing ingredients of the recipe, or of the week
// (resolved from at in location) or range. The words only found the recipe.
func mealArguments(tool string, planned map[string]any, recipeID string, at time.Time, location *time.Location) map[string]any {
	switch {
	case tool == "food_clear_meal":
		return map[string]any{"date": planned["date"], "slot": planned["slot"]}
	case tool == "food_plan_meal":
		arguments := map[string]any{"date": planned["date"], "slot": planned["slot"]}
		if recipeID != "" {
			arguments["recipeId"] = recipeID
		} else {
			arguments["text"] = strings.TrimSpace(text(planned["recipeQuery"]))
		}
		for _, field := range []string{"servings", "note"} {
			if value, ok := planned[field]; ok && value != nil {
				arguments[field] = value
			}
		}
		return arguments
	case recipeID != "":
		return map[string]any{"recipeIds": []any{recipeID}}
	case planned["week"] != nil:
		from, to := weekRange(at, location, text(planned["week"]))
		return map[string]any{"from": from, "to": to}
	default:
		return map[string]any{"from": planned["from"], "to": planned["to"]}
	}
}

// weekRange is the Monday through Sunday of the week of at in location
// ("this"), or of the week after ("next").
func weekRange(at time.Time, location *time.Location, week string) (string, string) {
	local := at.In(location)
	monday := time.Date(local.Year(), local.Month(), local.Day()-(int(local.Weekday())+6)%7, 12, 0, 0, 0, location)
	if week == "next" {
		monday = monday.AddDate(0, 0, 7)
	}
	return monday.Format(time.DateOnly), monday.AddDate(0, 0, 6).Format(time.DateOnly)
}
