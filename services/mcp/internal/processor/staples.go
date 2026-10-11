package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Staples and running low (P15-08). "мясо заканчивается" is
// food_mark_running_low, "milk is a staple" food_add_staple, and "remove
// milk from staples" food_remove_staple. Running low and removing a staple
// name the item by the owner's words and their alternatives (itemQuery,
// itemQueryAlternatives), which the processor matches against the staples
// and, for running low, the in-stock pantry names, like the other Food
// changes: the exact name wins, else the start of a word, and several names
// go to the owner. Food compares names only by their exact normalised form
// ("Milch" is not "Vollmilch"), so the processor sends it the name it
// matched. A staple marked running low, used up, or thrown away goes back on
// the list by itself (Food's restock); the processor then leaves out the
// same item from the capture's own list add.

const (
	// maxStapleAmount and maxStoreName are Food's limits.
	maxStapleAmount = 50
	maxStoreName    = 100
)

// isStapleTool reports whether a tool is one of the staple operations.
func isStapleTool(tool string) bool {
	return tool == "food_mark_running_low" || tool == "food_add_staple" || tool == "food_remove_staple"
}

// isNamedFoodAction reports whether an action names an existing staple or
// pantry item by words, which the processor first resolves to a name.
func isNamedFoodAction(action plannedAction) bool {
	return action.Tool == "food_mark_running_low" || action.Tool == "food_remove_staple"
}

// validStapleAction checks a planned staple action: running low and a
// removal name the item by search terms (itemQuery at most maxFoodName
// characters, since it may go on the list as written), running low with an
// optional addToList; a new staple has a name and optionally an amount and
// a store, as text within Food's limits.
func validStapleAction(action plannedAction) bool {
	arguments := action.Arguments
	switch action.Tool {
	case "food_mark_running_low", "food_remove_staple":
		if value, present := arguments["addToList"]; present && value != nil {
			if _, ok := value.(bool); !ok {
				return false
			}
		}
		return validSearchTerms(arguments, "itemQuery", "itemQueryAlternatives") && utf8.RuneCountInString(strings.TrimSpace(text(arguments["itemQuery"]))) <= maxFoodName
	case "food_add_staple":
		for field, limit := range map[string]int{"name": maxFoodName, "amount": maxStapleAmount, "storeName": maxStoreName} {
			value, present := arguments[field]
			if !present || value == nil {
				if field == "name" {
					return false
				}
				continue
			}
			written, ok := value.(string)
			if !ok || utf8.RuneCountInString(strings.TrimSpace(written)) > limit || (field == "name" && strings.TrimSpace(written) == "") {
				return false
			}
		}
		return true
	}
	return false
}

// foodName is one name a running-low or staple change may mean: a staple,
// the in-stock pantry items of that name, or both.
type foodName struct {
	// Name is as Food has it: the staple's, else the first pantry item's.
	Name   string
	Staple map[string]any
	Pantry []map[string]any
}

// ID is what a Human Review offers for the name: the staple, else its first
// pantry item.
func (n foodName) ID() string {
	if n.Staple != nil {
		return text(n.Staple["id"])
	}
	return text(n.Pantry[0]["id"])
}

// has reports whether id is the staple's or one of the pantry items'.
func (n foodName) has(id string) bool {
	if n.Staple != nil && text(n.Staple["id"]) == id {
		return true
	}
	return slices.ContainsFunc(n.Pantry, func(item map[string]any) bool { return text(item["id"]) == id })
}

// foodNames is every name a running-low (staples and in-stock pantry items)
// or staple removal (staples only) may mean, one per normalised name.
func (r Runner) foodNames(ctx context.Context, tool string) ([]foodName, error) {
	raw, err := r.Tools.Call(ctx, "food_list_staples", map[string]any{})
	if err != nil {
		return nil, err
	}
	var staples struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(toolData(raw), &staples); err != nil {
		// A partial list could look like exactly one match.
		return nil, fmt.Errorf("decode food_list_staples result: %w", err)
	}
	var names []foodName
	index := map[string]int{}
	for _, staple := range staples.Items {
		key := foodNormalize(text(staple["name"]))
		if _, seen := index[key]; key == "" || seen {
			continue
		}
		index[key] = len(names)
		names = append(names, foodName{Name: text(staple["name"]), Staple: staple})
	}
	if tool != "food_mark_running_low" {
		return names, nil
	}
	pantry, err := r.listFoodItems(ctx, "food_list_pantry_items", "in_stock")
	if err != nil {
		return nil, err
	}
	for _, item := range pantry {
		key := foodNormalize(text(item["name"]))
		if key == "" || text(item["id"]) == "" {
			continue
		}
		if at, seen := index[key]; seen {
			names[at].Pantry = append(names[at].Pantry, item)
			continue
		}
		index[key] = len(names)
		names = append(names, foodName{Name: text(item["name"]), Pantry: []map[string]any{item}})
	}
	return names, nil
}

// matchFoodNames is the names the action's search terms name, as
// matchFoodItems matches items: the exact name wins, else the start of a
// word.
func matchFoodNames(names []foodName, action plannedAction) []foodName {
	items := make([]map[string]any, len(names))
	for i, name := range names {
		items[i] = map[string]any{"name": name.Name, "index": i}
	}
	var found []foodName
	for _, item := range matchFoodItems(items, searchTerms(action.Arguments, "itemQuery", "itemQueryAlternatives")) {
		found = append(found, names[item["index"].(int)])
	}
	return found
}

// chosenFoodName is the name whose staple or pantry item the owner chose in
// Human Review.
func chosenFoodName(names []foodName, id string) []foodName {
	for _, name := range names {
		if name.has(id) {
			return []foodName{name}
		}
	}
	return nil
}

// namedFoodCandidates is the names a Human Review offers: each with its
// staple's id, or its first pantry item's, labelled with where it is kept.
func namedFoodCandidates(found []foodName) []map[string]any {
	if len(found) > foodCandidateLimit {
		found = found[:foodCandidateLimit]
	}
	candidates := make([]map[string]any, 0, len(found))
	for _, name := range found {
		parts := []string{name.Name}
		kind := "pantry_item"
		if name.Staple != nil {
			kind = "staple"
			parts = append(parts, "staple")
		}
		if len(name.Pantry) > 0 {
			for _, field := range []string{"amount", "location"} {
				if label := strings.TrimSpace(text(name.Pantry[0][field])); label != "" {
					parts = append(parts, label)
				}
			}
		}
		candidates = append(candidates, map[string]any{"id": name.ID(), "kind": kind, "name": name.Name, "title": strings.Join(parts, " · ")})
	}
	return candidates
}

// namedFoodReviewReason tells the owner why a running-low or staple change
// waits.
func namedFoodReviewReason(tool string) string {
	if tool == "food_remove_staple" {
		return "Several staples match: choose the one to remove"
	}
	return "Several staples or pantry items match: choose the one running low"
}

// noStapleReason is recorded, and the capture's other actions still run,
// when no staple matches a removal.
const noStapleReason = "No staple matches, so none was removed"

// stapleArguments is what a staple MCP tool receives: for running low the
// name Food knows (or, when none matched, the owner's words) and addToList,
// for a removal the staple's name, for a new staple the planned fields.
func stapleArguments(tool string, planned map[string]any, name string) map[string]any {
	switch tool {
	case "food_mark_running_low":
		addToList, _ := planned["addToList"].(bool)
		return map[string]any{"name": name, "addToList": addToList}
	case "food_remove_staple":
		return map[string]any{"name": name}
	}
	arguments := map[string]any{"name": strings.TrimSpace(text(planned["name"]))}
	for _, field := range []string{"amount", "storeName"} {
		if value := strings.TrimSpace(text(planned[field])); value != "" {
			arguments[field] = value
		}
	}
	return arguments
}

// restocking reports whether an action may make Food put a staple (or, with
// addToList, the item) on the shopping list by itself.
func restocking(tool string) bool {
	return tool == "food_update_pantry_item" || tool == "food_mark_running_low"
}

// restockFirst runs the actions that may restock before the capture's list
// adds, each group in its planned order, so a list add can leave out what a
// staple just put on the list. A plan without both is left as it is.
func restockFirst(p plan) plan {
	adds := slices.ContainsFunc(p.Actions, func(action plannedAction) bool { return action.Tool == "food_add_shopping_items" })
	if !adds || !slices.ContainsFunc(p.Actions, func(action plannedAction) bool { return restocking(action.Tool) }) {
		return p
	}
	ordered := make([]plannedAction, 0, len(p.Actions))
	for _, first := range []bool{true, false} {
		for _, action := range p.Actions {
			if restocking(action.Tool) == first {
				ordered = append(ordered, action)
			}
		}
	}
	p.Actions = ordered
	return p
}

// restocked is what Food put on the list by itself during a capture: the
// item's name and the words that named it.
type restocked struct {
	names []string
	terms []string
}

// restockOf reads what a restocking action's result says went on the list:
// a restock added, merged into an item, or already on the list.
func restockOf(result json.RawMessage, action plannedAction) (restocked, bool) {
	var answer struct {
		Restock *struct {
			Status       string         `json:"status"`
			ShoppingItem map[string]any `json:"shoppingItem"`
		} `json:"restock"`
	}
	if json.Unmarshal(toolData(result), &answer) != nil || answer.Restock == nil {
		return restocked{}, false
	}
	switch answer.Restock.Status {
	case "added", "merged", "on_list":
	default:
		return restocked{}, false
	}
	var names []string
	for _, field := range []string{"name", "normalizedName"} {
		if name := comparableTitle(text(answer.Restock.ShoppingItem[field])); name != "" {
			names = append(names, name)
		}
	}
	return restocked{names: names, terms: searchTerms(action.Arguments, "itemQuery", "itemQueryAlternatives")}, true
}

// alreadyRestocked reports whether a list item the capture adds is one Food
// has just put on the list itself: the restocked item's name, one of the
// words that named it, or a one-word name such a word starts ("молоко" for
// "молок", since the owner may name it in another language than the
// pantry). A longer name keeps its own meaning ("milk chocolate").
func alreadyRestocked(name string, restocks []restocked) bool {
	item := comparableTitle(name)
	if item == "" {
		return false
	}
	for _, restock := range restocks {
		if slices.Contains(restock.names, item) {
			return true
		}
		for _, term := range restock.terms {
			term = comparableTitle(term)
			if term == "" {
				continue
			}
			if item == term || (!strings.Contains(item, " ") && utf8.RuneCountInString(term) >= minTaskQueryAlternative && strings.HasPrefix(item, term)) {
				return true
			}
		}
	}
	return false
}

// withoutRestocked splits a list add's items into those to add and the
// names Food has already put on the list by itself.
func withoutRestocked(items any, restocks []restocked) ([]any, []string) {
	list, _ := items.([]any)
	var kept []any
	var skipped []string
	for _, raw := range list {
		item, _ := raw.(map[string]any)
		if name := text(item["name"]); alreadyRestocked(name, restocks) {
			skipped = append(skipped, strings.TrimSpace(name))
			continue
		}
		kept = append(kept, raw)
	}
	return kept, skipped
}

// restockedReason records the list items a capture did not add itself.
func restockedReason(skipped []string) string {
	return "Already on the shopping list by itself (staple): " + strings.Join(skipped, ", ")
}
