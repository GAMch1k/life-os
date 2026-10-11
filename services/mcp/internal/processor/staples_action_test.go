package processor

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestValidActionChecksStapleActions(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		arguments  map[string]any
		valid      bool
	}{
		{"running low", "food_mark_running_low", map[string]any{"itemQuery": "мясо", "itemQueryAlternatives": []any{"мяс", "meat", "Fleisch"}}, true},
		{"running low, buy it", "food_mark_running_low", map[string]any{"itemQuery": "milk", "addToList": true}, true},
		{"running low without words", "food_mark_running_low", map[string]any{"itemQueryAlternatives": []any{"Milch"}}, false},
		{"running low by name", "food_mark_running_low", map[string]any{"name": "Milch"}, false},
		{"addToList as text", "food_mark_running_low", map[string]any{"itemQuery": "milk", "addToList": "yes"}, false},
		{"running low of 201 characters", "food_mark_running_low", map[string]any{"itemQuery": strings.Repeat("я", 201)}, false},
		{"running low with an amount", "food_mark_running_low", map[string]any{"itemQuery": "milk", "amount": "2 l"}, false},
		{"new staple", "food_add_staple", map[string]any{"name": "Kaffee"}, true},
		{"new staple with amount and store", "food_add_staple", map[string]any{"name": "Milch", "amount": "2 l", "storeName": "Lidl", "aisle": nil}, false},
		{"new staple with amount and store, no aisle", "food_add_staple", map[string]any{"name": "Milch", "amount": "2 l", "storeName": "Lidl"}, true},
		{"new staple without a name", "food_add_staple", map[string]any{"amount": "2 l"}, false},
		{"new staple with a blank name", "food_add_staple", map[string]any{"name": " "}, false},
		{"new staple with a long amount", "food_add_staple", map[string]any{"name": "Milch", "amount": strings.Repeat("1", 51)}, false},
		{"new staple with a number", "food_add_staple", map[string]any{"name": "Eier", "amount": 10.0}, false},
		{"new staple by words", "food_add_staple", map[string]any{"itemQuery": "milk"}, false},
		{"removal", "food_remove_staple", map[string]any{"itemQuery": "milk", "itemQueryAlternatives": []any{"Milch"}}, true},
		{"removal by id", "food_remove_staple", map[string]any{"itemQuery": "milk", "stapleId": "staple-1"}, false},
		{"removal with addToList", "food_remove_staple", map[string]any{"itemQuery": "milk", "addToList": true}, false},
	} {
		if got := validAction(plannedAction{Tool: tc.tool, Arguments: tc.arguments}); got != tc.valid {
			t.Errorf("%s: validAction = %v, want %v", tc.name, got, tc.valid)
		}
	}
}

const restockAdded = `{"name":"Fleisch","changed":true,"restock":{"status":"added","reason":"running_low","stapleId":"staple-1","shoppingItem":{"id":"shop-9","name":"Fleisch","normalizedName":"fleisch"},"message":"Added “Fleisch” to the list: running low (staple)"}}`

func TestRunningLowFindsTheStapleInAnotherLanguage(t *testing.T) {
	// "мясо заканчивается" while the staple is "Fleisch" and the pantry has
	// minced meat, whose word "fleisch" does not start the name.
	tools := newFoodTools("мясо заканчивается")
	tools.staples = []map[string]any{named("staple-1", "Fleisch"), named("staple-2", "Milch")}
	tools.pantry = []map[string]any{named("pantry-1", "Hackfleisch", "location", "fridge")}
	tools.results = map[string]string{"food_mark_running_low": restockAdded}
	tools.run(t, `{"actions":[{"tool":"food_mark_running_low","arguments":{"itemQuery":"мясо","itemQueryAlternatives":["мяс","meat","Fleisch"]}}],"confidence":0.95}`)

	input := tools.inputs["food_mark_running_low"][0]
	if input["name"] != "Fleisch" || input["addToList"] != false || !strings.HasPrefix(input["idempotencyKey"].(string), "food-") || len(input) != 3 {
		t.Fatalf("food_mark_running_low input = %#v", input)
	}
	if read := tools.reads[0]; read["state"] != "in_stock" {
		t.Fatalf("pantry read = %#v", read)
	}
	if tools.review != nil || contains(tools.calls, "food_add_shopping_items") {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
	if audit := tools.processed(t)[0]; !strings.Contains(string(audit["result"].(json.RawMessage)), "running low (staple)") {
		t.Fatalf("audit = %#v", audit)
	}
}

func TestRunningLowFlagsAPantryItemAndBuysOnlyWhenAsked(t *testing.T) {
	// "Почти закончился рис" with "Reis" in the pantry and no staple of it.
	tools := newFoodTools("Почти закончился рис")
	tools.pantry = []map[string]any{named("pantry-1", "Reis", "location", "pantry"), named("pantry-2", "Reiswaffeln")}
	tools.run(t, `{"actions":[{"tool":"food_mark_running_low","arguments":{"itemQuery":"рис","itemQueryAlternatives":["rice","Reis"]}}],"confidence":0.95}`)
	if input := tools.inputs["food_mark_running_low"][0]; input["name"] != "Reis" || input["addToList"] != false {
		t.Fatalf("input = %#v", input)
	}
	tools.processed(t)

	// Nothing of the name anywhere: the owner's words go to Food, which
	// changes nothing unless the capture says to buy it.
	nothing := newFoodTools("рис заканчивается, купи")
	nothing.run(t, `{"actions":[{"tool":"food_mark_running_low","arguments":{"itemQuery":"рис","itemQueryAlternatives":["rice","Reis"],"addToList":true}}],"confidence":0.95}`)
	if input := nothing.inputs["food_mark_running_low"][0]; input["name"] != "рис" || input["addToList"] != true {
		t.Fatalf("input = %#v", input)
	}
	if audit := nothing.processed(t)[0]; audit["tool"] != "food_mark_running_low" {
		t.Fatalf("audit = %#v", audit)
	}
	flag := newFoodTools("fast keine Milch mehr")
	flag.run(t, `{"actions":[{"tool":"food_mark_running_low","arguments":{"itemQuery":"Milch","itemQueryAlternatives":["milk","молок"]}}],"confidence":0.95}`)
	if input := flag.inputs["food_mark_running_low"][0]; input["name"] != "Milch" || input["addToList"] != false {
		t.Fatalf("input = %#v", input)
	}
	flag.processed(t)
}

func TestSeveralRunningLowNamesGoToTheOwner(t *testing.T) {
	tools := newFoodTools("Milch wird knapp")
	tools.staples = []map[string]any{named("staple-1", "Milch laktosefrei")}
	tools.pantry = []map[string]any{named("pantry-1", "Milch 3,5%", "amount", "1 l", "location", "fridge"), named("pantry-2", "milch 3,5%"), named("pantry-3", "Milchreis")}
	tools.run(t, `{"actions":[{"tool":"food_mark_running_low","arguments":{"itemQuery":"Milch","itemQueryAlternatives":["milk"]}}],"confidence":0.95}`)

	if contains(tools.calls, "food_mark_running_low") || contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v", tools.calls)
	}
	if tools.review == nil || tools.review["errorSummary"] != "Several staples or pantry items match: choose the one running low" {
		t.Fatalf("review = %#v", tools.review)
	}
	// One candidate per name: the staple, the two cartons of one name, and
	// the rice pudding ("milch" starts its word).
	candidates := tools.review["candidates"].([]map[string]any)
	if len(candidates) != 3 || candidates[2]["title"] != "Milchreis" || candidates[0]["id"] != "staple-1" || candidates[0]["kind"] != "staple" || candidates[0]["title"] != "Milch laktosefrei · staple" ||
		candidates[1]["id"] != "pantry-1" || candidates[1]["kind"] != "pantry_item" || candidates[1]["title"] != "Milch 3,5% · 1 l · fridge" {
		t.Fatalf("candidates = %#v", candidates)
	}

	// The owner chose the second carton's name: that name is marked.
	approved := newFoodTools("Milch wird knapp")
	approved.staples, approved.pantry = tools.staples, tools.pantry
	approved.approved = map[string]any{"tool": "food_mark_running_low", "arguments": map[string]any{"itemQuery": "Milch", "itemQueryAlternatives": []any{"milk"}, "taskId": "pantry-2"}}
	approved.run(t, `{"actions":[],"ambiguous":true,"confidence":0}`)
	if input := approved.inputs["food_mark_running_low"][0]; input["name"] != "Milch 3,5%" || approved.review != nil {
		t.Fatalf("input = %#v, review = %#v", input, approved.review)
	}
	approved.processed(t)
}

func TestAStapleIsAddedAndRemovedByItsName(t *testing.T) {
	added := newFoodTools("Kaffee sollte ich immer zu Hause haben")
	added.run(t, `{"actions":[{"tool":"food_add_staple","arguments":{"name":" Kaffee ","amount":"500 g","storeName":"Lidl"}}],"confidence":0.95}`)
	input := added.inputs["food_add_staple"][0]
	key, _ := input["idempotencyKey"].(string)
	delete(input, "idempotencyKey")
	if !reflect.DeepEqual(input, map[string]any{"name": "Kaffee", "amount": "500 g", "storeName": "Lidl"}) || !strings.HasPrefix(key, "food-") {
		t.Fatalf("food_add_staple input = %#v (key %q)", input, key)
	}
	if contains(added.calls, "food_list_staples") || contains(added.calls, "food_add_shopping_items") {
		t.Fatalf("calls = %#v", added.calls)
	}
	added.processed(t)

	// "remove milk from staples" with the staple "Milch".
	removed := newFoodTools("remove milk from staples")
	removed.staples = []map[string]any{named("staple-1", "Milch"), named("staple-2", "Kaffee")}
	removed.run(t, `{"actions":[{"tool":"food_remove_staple","arguments":{"itemQuery":"milk","itemQueryAlternatives":["Milch","молок"]}}],"confidence":0.95}`)
	if input := removed.inputs["food_remove_staple"][0]; input["name"] != "Milch" || len(input) != 2 {
		t.Fatalf("food_remove_staple input = %#v", input)
	}
	// Only staples are read: a pantry item is no staple to remove.
	if contains(removed.calls, "food_list_pantry_items") {
		t.Fatalf("calls = %#v", removed.calls)
	}
	removed.processed(t)

	// No such staple: nothing removed, and the capture's other actions run.
	none := newFoodTools("Milch ist kein Grundvorrat mehr, call mom")
	none.staples = []map[string]any{named("staple-2", "Kaffee")}
	none.run(t, `{"actions":[{"tool":"food_remove_staple","arguments":{"itemQuery":"Milch"}},{"tool":"task_create","arguments":{"title":"Call mom"}}],"confidence":0.95}`)
	if contains(none.calls, "food_remove_staple") || len(none.inputs["task_create"]) != 1 {
		t.Fatalf("calls = %#v", none.calls)
	}
	if audit := none.processed(t); audit[0]["tool"] != "none" || audit[0]["reason"] != noStapleReason {
		t.Fatalf("audit = %#v", audit)
	}

	// Two staples match: the owner chooses.
	several := newFoodTools("remove milk from staples")
	several.staples = []map[string]any{named("staple-1", "Milch 3,5%"), named("staple-3", "Milch laktosefrei")}
	several.run(t, `{"actions":[{"tool":"food_remove_staple","arguments":{"itemQuery":"Milch"}}],"confidence":0.95}`)
	if several.review == nil || several.review["errorSummary"] != "Several staples match: choose the one to remove" || len(several.review["candidates"].([]map[string]any)) != 2 {
		t.Fatalf("review = %#v", several.review)
	}
}

const usedUpRestock = `{"id":"pantry-1","name":"Milch","state":"used_up","restock":{"status":"added","reason":"used_up","stapleId":"staple-1","shoppingItem":{"id":"shop-9","name":"Milch","normalizedName":"milch"},"message":"Added “Milch” to the list: used up (staple)"}}`

func TestAListAddLeavesOutWhatAStapleAlreadyPutOnTheList(t *testing.T) {
	// "молоко закончилось, надо купить, и хлеб": the pantry's "Milch" is a
	// staple, so Food puts it back on the list; the capture adds only bread,
	// even though the plan lists the add first.
	plan := `{"actions":[{"tool":"food_add_shopping_items","arguments":{"items":[{"name":"молоко"},{"name":"хлеб"}]}},{"tool":"food_update_pantry_item","arguments":{"itemQuery":"молок","itemQueryAlternatives":["milk","Milch"],"state":"used_up"}}],"confidence":0.95}`
	tools := newFoodTools("молоко закончилось, надо купить, и хлеб")
	tools.pantry = []map[string]any{named("pantry-1", "Milch")}
	tools.results = map[string]string{"food_update_pantry_item": usedUpRestock}
	tools.run(t, plan)

	added := tools.inputs["food_add_shopping_items"]
	if len(added) != 1 || !reflect.DeepEqual(added[0]["items"], []any{map[string]any{"name": "хлеб"}}) {
		t.Fatalf("food_add_shopping_items inputs = %#v", added)
	}
	audit := tools.processed(t)
	if audit[0]["tool"] != "food_update_pantry_item" || audit[1]["tool"] != "food_add_shopping_items" || !reflect.DeepEqual(audit[1]["notAdded"], []string{"молоко"}) {
		t.Fatalf("audit = %#v", audit)
	}

	// Only the milk: the add is left out entirely.
	only := newFoodTools("молоко закончилось, надо купить")
	only.pantry = tools.pantry
	only.results = tools.results
	only.run(t, `{"actions":[{"tool":"food_update_pantry_item","arguments":{"itemQuery":"молок","itemQueryAlternatives":["milk","Milch"],"state":"used_up"}},{"tool":"food_add_shopping_items","arguments":{"items":[{"name":"молоко"}]}}],"confidence":0.95}`)
	if contains(only.calls, "food_add_shopping_items") {
		t.Fatalf("calls = %#v", only.calls)
	}
	if audit := only.processed(t); audit[1]["tool"] != "none" || audit[1]["reason"] != "Already on the shopping list by itself (staple): молоко" {
		t.Fatalf("audit = %#v", audit)
	}

	// Not a staple (no restock): the milk is added as planned.
	plain := newFoodTools("молоко закончилось, надо купить")
	plain.pantry = tools.pantry
	plain.run(t, `{"actions":[{"tool":"food_update_pantry_item","arguments":{"itemQuery":"молок","itemQueryAlternatives":["milk","Milch"],"state":"used_up"}},{"tool":"food_add_shopping_items","arguments":{"items":[{"name":"молоко"}]}}],"confidence":0.95}`)
	if added := plain.inputs["food_add_shopping_items"]; len(added) != 1 || !reflect.DeepEqual(added[0]["items"], []any{map[string]any{"name": "молоко"}}) {
		t.Fatalf("food_add_shopping_items inputs = %#v", added)
	}

	// A running-low staple likewise, though the plan should have used
	// addToList.
	low := newFoodTools("мясо заканчивается, купи мясо")
	low.staples = []map[string]any{named("staple-1", "Fleisch")}
	low.results = map[string]string{"food_mark_running_low": restockAdded}
	low.run(t, `{"actions":[{"tool":"food_add_shopping_items","arguments":{"items":[{"name":"мясо"}]}},{"tool":"food_mark_running_low","arguments":{"itemQuery":"мясо","itemQueryAlternatives":["мяс","Fleisch"],"addToList":true}}],"confidence":0.95}`)
	if contains(low.calls, "food_add_shopping_items") || low.inputs["food_mark_running_low"][0]["name"] != "Fleisch" {
		t.Fatalf("calls = %#v, inputs = %#v", low.calls, low.inputs)
	}
}

func TestAlreadyRestockedMatchesTheItemNotAnotherOne(t *testing.T) {
	restocks := []restocked{{names: []string{"milch"}, terms: []string{"молок", "milk", "Milch"}}}
	for name, want := range map[string]bool{
		"Milch":          true,
		"milk":           true,
		"молоко":         true,
		"Молока":         true,
		"milk chocolate": false,
		"Vollmilch":      false,
		"хлеб":           false,
		"":               false,
	} {
		if got := alreadyRestocked(name, restocks); got != want {
			t.Errorf("alreadyRestocked(%q) = %v, want %v", name, got, want)
		}
	}
	if alreadyRestocked("Milch", nil) {
		t.Error("restocked without a restock")
	}
}

func TestRestockFirstOnlyReordersAPlanWithBoth(t *testing.T) {
	add := plannedAction{Tool: "food_add_shopping_items"}
	used := plannedAction{Tool: "food_update_pantry_item"}
	task := plannedAction{Tool: "task_create"}
	low := plannedAction{Tool: "food_mark_running_low"}
	tools := func(p plan) []string {
		var names []string
		for _, action := range p.Actions {
			names = append(names, action.Tool)
		}
		return names
	}
	if got := tools(restockFirst(plan{Actions: []plannedAction{task, add, used, low}})); !reflect.DeepEqual(got, []string{"food_update_pantry_item", "food_mark_running_low", "task_create", "food_add_shopping_items"}) {
		t.Fatalf("reordered = %v", got)
	}
	if got := tools(restockFirst(plan{Actions: []plannedAction{task, used}})); !reflect.DeepEqual(got, []string{"task_create", "food_update_pantry_item"}) {
		t.Fatalf("a plan without an add = %v", got)
	}
}

func TestPlanPromptRoutesStaplesAndRunningLow(t *testing.T) {
	for _, want := range []string{
		`|recipe_import|food_mark_running_low|food_add_staple|food_remove_staple\"`,
		`Something running low but not finished yet (\"almost out of milk\", \"мясо заканчивается\", \"почти закончился рис\", \"fast keine Milch mehr\", \"Milch wird knapp\") is food_mark_running_low, never food_update_pantry_item or task_create.`,
		`itemQuery \"мясо\", itemQueryAlternatives [\"мяс\", \"meat\", \"Fleisch\"]`,
		`addToList (true only when the item also says to buy it or put it on the list`,
		"never add food_add_shopping_items for something running low: buying it is addToList.",
		`(\"milk is a staple\", \"молоко всегда должно быть дома\", \"Milch immer da haben\", \"Kaffee sollte ich immer zu Hause haben\") is food_add_staple`,
		"it puts nothing on the list now.",
		`(\"remove milk from staples\", \"молоко больше не нужно держать дома\", \"Milch muss nicht mehr immer da sein\") is food_remove_staple`,
		"when it is a staple, Food has already put it back on the list and the processor leaves it out",
	} {
		want = strings.ReplaceAll(want, `\"`, `"`)
		if !strings.Contains(planPrompt, want) {
			t.Errorf("planPrompt lacks %q", want)
		}
	}
}
