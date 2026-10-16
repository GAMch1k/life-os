package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Groceries go to the shopping list and the pantry: adds carry items with a
// name, changes name their item only by search terms.
func TestValidActionChecksFoodActions(t *testing.T) {
	item := func(fields ...any) map[string]any {
		out := map[string]any{}
		for i := 0; i < len(fields); i += 2 {
			out[fields[i].(string)] = fields[i+1]
		}
		return out
	}
	items := func(list ...map[string]any) map[string]any {
		raw := make([]any, len(list))
		for i, entry := range list {
			raw[i] = entry
		}
		return map[string]any{"items": raw}
	}
	many := make([]map[string]any, 31)
	for i := range many {
		many[i] = item("name", "Brot")
	}
	for _, tc := range []struct {
		name, tool string
		arguments  map[string]any
		valid      bool
	}{
		{"list add", "food_add_shopping_items", items(item("name", "молоко"), item("name", "яйца")), true},
		{"list add with every field", "food_add_shopping_items", items(item("name", "Eier", "amount", "6", "note", "Bio", "storeName", "Lidl", "aisle", "dairy")), true},
		{"list add with nulls and a blank aisle", "food_add_shopping_items", items(item("name", "milk", "amount", nil, "aisle", "")), true},
		{"thirty items", "food_add_shopping_items", items(many[:30]...), true},
		{"thirty-one items", "food_add_shopping_items", items(many...), false},
		{"no items", "food_add_shopping_items", items(), false},
		{"items not a list", "food_add_shopping_items", map[string]any{"items": "milk"}, false},
		{"blank name", "food_add_shopping_items", items(item("name", "  ")), false},
		{"no name", "food_add_shopping_items", items(item("amount", "6")), false},
		{"name of 201 characters", "food_add_shopping_items", items(item("name", strings.Repeat("я", 201))), false},
		{"name of 200 characters", "food_add_shopping_items", items(item("name", strings.Repeat("я", 200))), true},
		{"unknown aisle", "food_add_shopping_items", items(item("name", "Milch", "aisle", "fridge")), false},
		{"amount as a number", "food_add_shopping_items", items(item("name", "Eier", "amount", 6.0)), false},
		{"store id", "food_add_shopping_items", items(item("name", "Milch", "storeId", "store-1")), false},
		{"source from the plan", "food_add_shopping_items", map[string]any{"items": []any{item("name", "Milch")}, "sourceRef": "x"}, false},
		{"pantry location in a list add", "food_add_shopping_items", items(item("name", "Milch", "location", "fridge")), false},
		{"pantry add", "food_add_pantry_items", items(item("name", "йогурт", "location", "fridge", "expiresOn", "2026-10-16", "amount", "2")), true},
		{"pantry add without expiry", "food_add_pantry_items", items(item("name", "Reis")), true},
		{"relative expiry", "food_add_pantry_items", items(item("name", "Joghurt", "expiresOn", "Freitag")), false},
		{"unknown location", "food_add_pantry_items", items(item("name", "Joghurt", "location", "cellar")), false},
		{"store in a pantry add", "food_add_pantry_items", items(item("name", "Joghurt", "storeName", "Lidl")), false},
		{"mark bought", "food_mark_bought", map[string]any{"itemQuery": "milk", "itemQueryAlternatives": []any{"молок", "Milch"}}, true},
		{"mark bought without terms", "food_mark_bought", map[string]any{"itemQueryAlternatives": []any{"Milch"}}, false},
		{"mark bought by id", "food_mark_bought", map[string]any{"itemQuery": "milk", "itemIds": []any{"shop-1"}}, false},
		{"mark bought with a chosen candidate", "food_mark_bought", map[string]any{"itemQuery": "milk", "taskId": "shop-1"}, false},
		{"used up", "food_update_pantry_item", map[string]any{"itemQuery": "молоко", "state": "used_up"}, true},
		{"thrown away", "food_update_pantry_item", map[string]any{"itemQuery": "Joghurt", "itemQueryAlternatives": []any{"yogurt"}, "state": "thrown_away"}, true},
		{"back in stock", "food_update_pantry_item", map[string]any{"itemQuery": "milk", "state": "in_stock"}, false},
		{"pantry change without a state", "food_update_pantry_item", map[string]any{"itemQuery": "milk"}, false},
		{"pantry edit", "food_update_pantry_item", map[string]any{"itemQuery": "milk", "state": "used_up", "expiresOn": "2026-10-16"}, false},
	} {
		if got := validAction(plannedAction{Tool: tc.tool, Arguments: tc.arguments}); got != tc.valid {
			t.Errorf("%s: validAction = %v, want %v", tc.name, got, tc.valid)
		}
	}
}

// foodTools is a fake MCP for Food plans: it serves one capture (with an
// approved action and its capture time and timezone when set), the shopping
// list and pantry in pages, recipe searches and task_search per term, and
// records every write and the Human Review.
type foodTools struct {
	*fakeTools
	itemID, content string
	approved        map[string]any
	createdAt       time.Time
	timeZone        string
	// recipes maps a recipe search's q to the recipes it finds.
	recipes  map[string][]map[string]any
	searches []string
	// imported is recipe_import's answer (default: saved), and drafted
	// recipe_save_draft's.
	imported, drafted string
	shopping          []map[string]any
	pantry            []map[string]any
	staples           []map[string]any
	// results maps a write tool to its answer (default: {}).
	results map[string]string
	// pageSize splits the lists into pages when set.
	pageSize    int
	taskMatches map[string][]string
	reads       []map[string]any
	inputs      map[string][]map[string]any
	review      map[string]any
}

func newFoodTools(content string) *foodTools {
	return &foodTools{fakeTools: &fakeTools{}, itemID: "item-1", content: content, inputs: map[string][]map[string]any{}}
}

func (f *foodTools) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	switch name {
	case "inbox_list":
		f.calls = append(f.calls, name)
		item := inboxItem{ID: f.itemID, Content: f.content, Source: "manual", SourceMetadata: map[string]any{}, CreatedAt: f.createdAt}
		if f.timeZone != "" {
			item.SourceMetadata["timeZone"] = f.timeZone
		}
		if f.approved != nil {
			item.ProcessingMetadata = map[string]any{"approvedAction": f.approved}
		}
		raw, _ := json.Marshal(inboxList{Items: []inboxItem{item}})
		return raw, nil
	case "food_list_shopping_items", "food_list_pantry_items":
		f.calls = append(f.calls, name)
		f.reads = append(f.reads, args)
		list := f.shopping
		if name == "food_list_pantry_items" {
			list = f.pantry
		}
		start, _ := args["cursor"].(string)
		from := 0
		if start != "" {
			from = len(start)
		}
		to := len(list)
		page := map[string]any{}
		if f.pageSize > 0 && from+f.pageSize < to {
			to = from + f.pageSize
			page["nextCursor"] = strings.Repeat("c", to)
		}
		page["items"] = list[from:to]
		raw, _ := json.Marshal(page)
		return raw, nil
	case "food_list_recipes":
		f.calls = append(f.calls, name)
		q, _ := args["q"].(string)
		f.searches = append(f.searches, q)
		if args["limit"] != recipeSearchLimit {
			return nil, fmt.Errorf("recipe search limit = %v", args["limit"])
		}
		items := f.recipes[q]
		if items == nil {
			items = []map[string]any{}
		}
		raw, _ := json.Marshal(map[string]any{"items": items})
		return raw, nil
	case "task_search":
		f.calls = append(f.calls, name)
		query, _ := url.ParseQuery(args["query"].(string))
		items := []map[string]any{}
		for _, id := range f.taskMatches[query.Get("query")] {
			items = append(items, map[string]any{"id": id, "title": "Task " + id})
		}
		raw, _ := json.Marshal(map[string]any{"items": items})
		return raw, nil
	case "food_list_staples":
		f.calls = append(f.calls, name)
		items := f.staples
		if items == nil {
			items = []map[string]any{}
		}
		raw, _ := json.Marshal(map[string]any{"items": items})
		return raw, nil
	case "inbox_set_human_review":
		f.review = args
	case "recipe_import":
		f.calls = append(f.calls, name)
		f.inputs[name] = append(f.inputs[name], args)
		if f.imported == "" {
			return json.RawMessage(`{"saved":true,"method":"structured","recipeId":"recipe-9"}`), nil
		}
		return json.RawMessage(f.imported), nil
	case "recipe_save_draft":
		f.calls = append(f.calls, name)
		f.inputs[name] = append(f.inputs[name], args)
		if f.drafted == "" {
			return json.RawMessage(`{"saved":true,"recipeId":"recipe-10","recipe":{"id":"recipe-10","title":"Crispy chicken"}}`), nil
		}
		return json.RawMessage(f.drafted), nil
	}
	if isFoodWrite(name) || strings.HasPrefix(name, "task_") {
		f.inputs[name] = append(f.inputs[name], args)
		if answer, ok := f.results[name]; ok {
			f.calls = append(f.calls, name)
			return json.RawMessage(answer), nil
		}
	}
	return f.fakeTools.Call(ctx, name, args)
}

func (f *foodTools) run(t *testing.T, plan string) {
	t.Helper()
	if _, err := (Runner{Planner: fakePlanner{output: plan}, Tools: f, Model: "test"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// processed is the audit of the item's last attempt, which must be processed.
func (f *foodTools) processed(t *testing.T) []map[string]any {
	t.Helper()
	last := f.records[len(f.records)-1]
	if last["status"] != "processed" || !contains(f.calls, "inbox_resolve") {
		t.Fatalf("not processed: records = %#v, calls = %#v", f.records, f.calls)
	}
	return last["actions"].([]map[string]any)
}

func named(id, name string, fields ...string) map[string]any {
	item := map[string]any{"id": id, "name": name}
	for i := 0; i+1 < len(fields); i += 2 {
		item[fields[i]] = fields[i+1]
	}
	return item
}

const groceriesPlan = `{"actions":[{"tool":"food_add_shopping_items","arguments":{"items":[{"name":"Milch"},{"name":"Eier","amount":"6"}]}}],"confidence":0.95}`

func TestFoodAddsCarryTheirCaptureAndADeterministicKey(t *testing.T) {
	first := newFoodTools("Milch und 6 Eier")
	first.run(t, groceriesPlan)
	added := first.inputs["food_add_shopping_items"][0]
	key, _ := added["idempotencyKey"].(string)
	if added["sourceRef"] != "item-1" || !strings.HasPrefix(key, "food-") {
		t.Fatalf("food_add_shopping_items input = %#v", added)
	}
	if items := added["items"].([]any); len(items) != 2 || items[1].(map[string]any)["amount"] != "6" {
		t.Fatalf("items = %#v", added["items"])
	}
	first.processed(t)

	// The capture retried, planned again: the same write has the same key,
	// so Food replays its first answer instead of adding again.
	again := newFoodTools("Milch und 6 Eier")
	again.run(t, groceriesPlan)
	if got := again.inputs["food_add_shopping_items"][0]["idempotencyKey"]; got != key {
		t.Fatalf("retried key = %v, want %v", got, key)
	}
	// Also when the new plan lists it after another action.
	reordered := newFoodTools("Milch und 6 Eier")
	reordered.run(t, `{"actions":[{"tool":"task_create","arguments":{"title":"Call mom"}},{"tool":"food_add_shopping_items","arguments":{"items":[{"name":"Milch"},{"name":"Eier","amount":"6"}]}}],"confidence":0.95}`)
	if got := reordered.inputs["food_add_shopping_items"][0]["idempotencyKey"]; got != key {
		t.Fatalf("key after reordering = %v, want %v", got, key)
	}
	// Another capture, or another write, has another key.
	other := newFoodTools("Milch und 6 Eier")
	other.itemID = "item-2"
	other.run(t, groceriesPlan)
	if got := other.inputs["food_add_shopping_items"][0]["idempotencyKey"]; got == key {
		t.Fatal("another capture reused the key")
	}
	changed := newFoodTools("Milch und 6 Eier")
	changed.run(t, `{"actions":[{"tool":"food_add_shopping_items","arguments":{"items":[{"name":"Milch"},{"name":"Eier","amount":"10"}]}}],"confidence":0.95}`)
	if got := changed.inputs["food_add_shopping_items"][0]["idempotencyKey"]; got == key {
		t.Fatal("another write reused the key")
	}

	pantry := newFoodTools("йогурт в холодильнике до пятницы")
	pantry.run(t, `{"actions":[{"tool":"food_add_pantry_items","arguments":{"items":[{"name":"йогурт","location":"fridge","expiresOn":"2026-10-16"}]}}],"confidence":0.9}`)
	input := pantry.inputs["food_add_pantry_items"][0]
	if input["sourceRef"] != "item-1" || !strings.HasPrefix(input["idempotencyKey"].(string), "food-") {
		t.Fatalf("food_add_pantry_items input = %#v", input)
	}
	pantry.processed(t)
}

func TestMarkBoughtFindsTheListItemInAnotherLanguage(t *testing.T) {
	// "I bought the milk" while the list says "Молоко".
	tools := newFoodTools("I bought the milk")
	tools.shopping = []map[string]any{named("shop-1", "Молоко"), named("shop-2", "Brot"), named("shop-3", "Steak")}
	tools.run(t, `{"actions":[{"tool":"food_mark_bought","arguments":{"itemQuery":"milk","itemQueryAlternatives":["молок","Milch"]}}],"confidence":0.95}`)

	if read := tools.reads[0]; read["state"] != "needed" || read["limit"] != foodListLimit {
		t.Fatalf("list read = %#v", read)
	}
	input := tools.inputs["food_mark_bought"][0]
	if !reflect.DeepEqual(input["itemIds"], []any{"shop-1"}) || !strings.HasPrefix(input["idempotencyKey"].(string), "food-") {
		t.Fatalf("food_mark_bought input = %#v", input)
	}
	if _, ok := input["itemQuery"]; ok {
		t.Fatalf("search terms reached Food: %#v", input)
	}
	if contains(tools.calls, "task_search") || tools.review != nil {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
	tools.processed(t)
}

func TestMatchFoodItemsPrefersTheExactNameAndWordStarts(t *testing.T) {
	list := []map[string]any{named("a", "Milk"), named("b", "Milk chocolate"), named("c", "Steak"), named("d", "Бананы"), named("e", "Hafermilch"), named("f", "Ёжики")}
	for _, tc := range []struct {
		terms []string
		want  []string
	}{
		{[]string{"milk"}, []string{"a"}},
		{[]string{"MILK!"}, []string{"a"}},
		{[]string{"chocolate"}, []string{"b"}},
		{[]string{"tea"}, nil},
		{[]string{"bananas", "банан"}, []string{"d"}},
		{[]string{"Milch"}, nil},
		{[]string{"ежик"}, []string{"f"}},
		{[]string{"mil"}, []string{"a", "b"}},
	} {
		var got []string
		for _, item := range matchFoodItems(list, tc.terms) {
			got = append(got, item["id"].(string))
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q matched %q, want %q", tc.terms, got, tc.want)
		}
	}
}

func TestABoughtItemNotOnTheListCompletesTheOpenTask(t *testing.T) {
	// "I already bought the chicken" with the Task "Buy chicken for dinner".
	tools := newFoodTools("I already bought the chicken.")
	tools.taskMatches = map[string][]string{"куриц": {"task-1"}}
	tools.run(t, `{"actions":[{"tool":"food_mark_bought","arguments":{"itemQuery":"chicken","itemQueryAlternatives":["куриц","Hähnchen"]}}],"confidence":0.95}`)

	if want := []map[string]any{{"taskId": "task-1"}}; !reflect.DeepEqual(tools.inputs["task_complete"], want) {
		t.Fatalf("task_complete inputs = %#v, calls = %#v", tools.inputs["task_complete"], tools.calls)
	}
	if contains(tools.calls, "food_mark_bought") || tools.review != nil {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
	if action := tools.processed(t)[0]; action["tool"] != "task_complete" {
		t.Fatalf("audit = %#v", action)
	}
}

func TestNothingToMarkBoughtCreatesNothingAndTheRestRuns(t *testing.T) {
	tools := newFoodTools("bought the milk, call mom tomorrow")
	tools.shopping = []map[string]any{named("shop-1", "Brot")}
	tools.run(t, `{"actions":[{"tool":"food_mark_bought","arguments":{"itemQuery":"milk","itemQueryAlternatives":["молок"]}},{"tool":"task_create","arguments":{"title":"Call mom","dueAt":"2026-10-12T00:00:00+02:00"}}],"confidence":0.95}`)

	if contains(tools.calls, "food_mark_bought") || contains(tools.calls, "food_add_shopping_items") || contains(tools.calls, "task_complete") || tools.review != nil {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
	if len(tools.inputs["task_create"]) != 1 {
		t.Fatalf("task_create inputs = %#v", tools.inputs["task_create"])
	}
	audit := tools.processed(t)
	if audit[0]["tool"] != "none" || audit[0]["plannedTool"] != "food_mark_bought" || audit[0]["reason"] != unmatchedFoodReason("food_mark_bought") || audit[1]["tool"] != "task_create" {
		t.Fatalf("audit = %#v", audit)
	}
}

func TestSeveralMatchingListItemsGoToHumanReview(t *testing.T) {
	tools := newFoodTools("Milch gekauft")
	tools.shopping = []map[string]any{named("shop-1", "Milch 3,5%", "amount", "2 l", "storeName", "Lidl"), named("shop-2", "Milch laktosefrei")}
	tools.run(t, `{"actions":[{"tool":"food_mark_bought","arguments":{"itemQuery":"Milch","itemQueryAlternatives":["milk"]}}],"confidence":0.95}`)

	if contains(tools.calls, "food_mark_bought") || contains(tools.calls, "inbox_resolve") || contains(tools.calls, "task_search") {
		t.Fatalf("calls = %#v", tools.calls)
	}
	if tools.review == nil || tools.review["errorSummary"] != "Several shopping list items match: choose the one you bought" {
		t.Fatalf("review = %#v", tools.review)
	}
	candidates := tools.review["candidates"].([]map[string]any)
	if len(candidates) != 2 || candidates[0]["id"] != "shop-1" || candidates[0]["kind"] != "shopping_item" || candidates[0]["title"] != "Milch 3,5% · 2 l · Lidl" || candidates[1]["title"] != "Milch laktosefrei" {
		t.Fatalf("candidates = %#v", candidates)
	}
	if proposed := tools.review["proposedAction"].(plannedAction); proposed.Tool != "food_mark_bought" || proposed.Arguments["itemQuery"] != "Milch" {
		t.Fatalf("proposed = %#v", proposed)
	}
}

func TestAnApprovedFoodChoiceRunsWithoutSearching(t *testing.T) {
	// Inbox records the chosen candidate as taskId.
	tools := newFoodTools("Milch gekauft")
	tools.approved = map[string]any{"tool": "food_mark_bought", "arguments": map[string]any{"itemQuery": "Milch", "itemQueryAlternatives": []any{"milk"}, "taskId": "shop-2"}}
	tools.run(t, `{"actions":[],"ambiguous":true,"confidence":0}`)

	if contains(tools.calls, "food_list_shopping_items") || contains(tools.calls, "task_search") {
		t.Fatalf("searched again: %#v", tools.calls)
	}
	if input := tools.inputs["food_mark_bought"][0]; !reflect.DeepEqual(input["itemIds"], []any{"shop-2"}) {
		t.Fatalf("food_mark_bought input = %#v", input)
	}
	tools.processed(t)

	pantry := newFoodTools("молоко закончилось")
	pantry.approved = map[string]any{"tool": "food_update_pantry_item", "arguments": map[string]any{"itemQuery": "молоко", "state": "used_up", "taskId": "pantry-2"}}
	pantry.run(t, `{"actions":[],"ambiguous":true,"confidence":0}`)
	if input := pantry.inputs["food_update_pantry_item"][0]; input["itemId"] != "pantry-2" || input["state"] != "used_up" || input["itemQuery"] != nil {
		t.Fatalf("food_update_pantry_item input = %#v", input)
	}
}

func TestUsedUpEndsThePantryItemAndBuysMore(t *testing.T) {
	// "молоко закончилось, надо купить"
	plan := `{"actions":[{"tool":"food_update_pantry_item","arguments":{"itemQuery":"молоко","itemQueryAlternatives":["milk","Milch"],"state":"used_up"}},{"tool":"food_add_shopping_items","arguments":{"items":[{"name":"молоко"}]}}],"confidence":0.95}`
	tools := newFoodTools("молоко закончилось, надо купить")
	tools.pantry = []map[string]any{named("pantry-1", "Молоко", "location", "fridge"), named("pantry-2", "Joghurt")}
	tools.run(t, plan)

	if read := tools.reads[0]; read["state"] != "in_stock" {
		t.Fatalf("pantry read = %#v", read)
	}
	input := tools.inputs["food_update_pantry_item"][0]
	if input["itemId"] != "pantry-1" || input["state"] != "used_up" || !strings.HasPrefix(input["idempotencyKey"].(string), "food-") || len(input) != 3 {
		t.Fatalf("food_update_pantry_item input = %#v", input)
	}
	if len(tools.inputs["food_add_shopping_items"]) != 1 {
		t.Fatalf("calls = %#v", tools.calls)
	}
	tools.processed(t)

	// Nothing in the pantry: no Task is searched, and the milk is still added.
	empty := newFoodTools("молоко закончилось, надо купить")
	empty.run(t, plan)
	if contains(empty.calls, "food_update_pantry_item") || contains(empty.calls, "task_search") || len(empty.inputs["food_add_shopping_items"]) != 1 {
		t.Fatalf("calls = %#v", empty.calls)
	}
	if audit := empty.processed(t); audit[0]["tool"] != "none" || audit[0]["reason"] != unmatchedFoodReason("food_update_pantry_item") {
		t.Fatalf("audit = %#v", audit)
	}
}

func TestTheShoppingListIsReadPageByPage(t *testing.T) {
	tools := newFoodTools("Bananen gekauft")
	tools.pageSize = 2
	tools.shopping = []map[string]any{named("shop-1", "Brot"), named("shop-2", "Milch"), named("shop-3", "Käse"), named("shop-4", "Bananen"), named("shop-5", "Bananenmilch")}
	tools.run(t, `{"actions":[{"tool":"food_mark_bought","arguments":{"itemQuery":"Bananen"}}],"confidence":0.95}`)

	if len(tools.reads) != 3 || tools.reads[1]["cursor"] != "cc" || tools.reads[2]["cursor"] != "cccc" {
		t.Fatalf("reads = %#v", tools.reads)
	}
	if input := tools.inputs["food_mark_bought"][0]; !reflect.DeepEqual(input["itemIds"], []any{"shop-4"}) {
		t.Fatalf("food_mark_bought input = %#v", input)
	}
}

func TestALowConfidenceFoodChangeOffersTheMatches(t *testing.T) {
	tools := newFoodTools("the milk is done")
	tools.pantry = []map[string]any{named("pantry-1", "Milch", "expiresOn", "2026-10-16")}
	tools.run(t, `{"actions":[{"tool":"food_update_pantry_item","arguments":{"itemQuery":"Milch","state":"used_up"}}],"confidence":0.6}`)

	if contains(tools.calls, "food_update_pantry_item") || tools.review["errorSummary"] != "ambiguous or low-confidence pantry item selection" {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
	if candidates := tools.review["candidates"].([]map[string]any); len(candidates) != 1 || candidates[0]["title"] != "Milch · expires 2026-10-16" || candidates[0]["kind"] != "pantry_item" {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func TestPlanPromptRoutesGroceriesToTheShoppingList(t *testing.T) {
	for _, want := range []string{
		"|food_add_shopping_items|food_mark_bought|food_update_shopping_item|food_add_pantry_items|food_update_pantry_item",
		"Groceries go on the shopping list; other purchases stay Tasks.",
		`are food_add_shopping_items, never task_create, also when the item gives a date or time (\"buy milk tomorrow\" only puts milk on the list)`,
		`Other purchases (\"buy a flash drive tomorrow\", \"купить зарядку для телефона\") stay task_create, with dueAt when a date is given.`,
		`\"Create a task: buy milk\", \"Создай задачу\"`,
		`\"buy milk and call mom\" is food_add_shopping_items with milk plus task_create \"Call mom\"`,
		"When it is genuinely unclear whether something is a grocery or household item or another purchase, set ambiguous true and actions to [] instead of guessing.",
		`never translate it: \"купить молоко и яйца\" is \"молоко\" and \"яйца\", \"Milch und 6 Eier\" is \"Milch\" and \"Eier\" with amount \"6\"`,
		`storeName (only when the item names the store, as written: \"buy toothpaste at dm\" or \"в Lidl купить хлеб\"`,
		`itemQuery \"milk\", itemQueryAlternatives [\"молок\", \"Milch\"]`,
		"when none does, it completes the one open Task that matches instead",
		`expiresOn (the ISO date it expires, resolved like any other date`,
		`state \"used_up\", or state \"thrown_away\"`,
		"add food_add_shopping_items for it too",
	} {
		want = strings.ReplaceAll(want, `\"`, `"`)
		if !strings.Contains(planPrompt, want) {
			t.Errorf("planPrompt lacks %q", want)
		}
	}
	// A purchase stated as done is no longer the Task example.
	if strings.Contains(planPrompt, `"I already bought the chicken" or "I called the dentist") is task_complete`) {
		t.Error("planPrompt still routes a grocery purchase to task_complete")
	}
}
