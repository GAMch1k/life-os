package processor

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Meals are planned and cleared by date and slot; a meal and the missing
// ingredients of a recipe name the recipe only by words.
func TestValidActionChecksMealActions(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		arguments  map[string]any
		valid      bool
	}{
		{"meal", "food_plan_meal", map[string]any{"recipeQuery": "Pasta Bolognese", "date": "2026-10-14", "slot": "dinner"}, true},
		{"meal with everything", "food_plan_meal", map[string]any{"recipeQuery": "паста", "recipeQueryAlternatives": []any{"pasta", "Nudeln"}, "date": "2026-10-14", "slot": "lunch", "servings": 4.0, "note": "for Anna"}, true},
		{"meal with null servings", "food_plan_meal", map[string]any{"recipeQuery": "суп", "date": "2026-10-16", "slot": "lunch", "servings": nil}, true},
		{"meal without words", "food_plan_meal", map[string]any{"date": "2026-10-14", "slot": "dinner"}, false},
		{"meal of 201 characters", "food_plan_meal", map[string]any{"recipeQuery": strings.Repeat("я", 201), "date": "2026-10-14", "slot": "dinner"}, false},
		{"meal on a weekday", "food_plan_meal", map[string]any{"recipeQuery": "Pasta", "date": "Wednesday", "slot": "dinner"}, false},
		{"meal at supper", "food_plan_meal", map[string]any{"recipeQuery": "Pasta", "date": "2026-10-14", "slot": "supper"}, false},
		{"meal for half a person", "food_plan_meal", map[string]any{"recipeQuery": "Pasta", "date": "2026-10-14", "slot": "dinner", "servings": 1.5}, false},
		{"meal for 101", "food_plan_meal", map[string]any{"recipeQuery": "Pasta", "date": "2026-10-14", "slot": "dinner", "servings": 101.0}, false},
		{"meal by recipe id", "food_plan_meal", map[string]any{"recipeQuery": "Pasta", "recipeId": "recipe-1", "date": "2026-10-14", "slot": "dinner"}, false},
		{"meal as text", "food_plan_meal", map[string]any{"text": "Pasta", "date": "2026-10-14", "slot": "dinner"}, false},
		{"clear", "food_clear_meal", map[string]any{"date": "2026-10-17", "slot": "breakfast"}, true},
		{"clear without a date", "food_clear_meal", map[string]any{"slot": "breakfast"}, false},
		{"clear with words", "food_clear_meal", map[string]any{"date": "2026-10-17", "slot": "breakfast", "recipeQuery": "Müsli"}, false},
		{"ingredients of a recipe", "food_add_missing_ingredients", map[string]any{"recipeQuery": "bolognese", "recipeQueryAlternatives": []any{"Bolognese"}}, true},
		{"ingredients of this week", "food_add_missing_ingredients", map[string]any{"week": "this"}, true},
		{"ingredients of next week", "food_add_missing_ingredients", map[string]any{"week": "next"}, true},
		{"ingredients of the weekend", "food_add_missing_ingredients", map[string]any{"from": "2026-10-17", "to": "2026-10-18"}, true},
		{"ingredients of 31 days", "food_add_missing_ingredients", map[string]any{"from": "2026-10-01", "to": "2026-10-31"}, true},
		{"ingredients of 32 days", "food_add_missing_ingredients", map[string]any{"from": "2026-10-01", "to": "2026-11-01"}, false},
		{"ingredients backwards", "food_add_missing_ingredients", map[string]any{"from": "2026-10-18", "to": "2026-10-17"}, false},
		{"ingredients of half a range", "food_add_missing_ingredients", map[string]any{"from": "2026-10-17"}, false},
		{"ingredients of last week", "food_add_missing_ingredients", map[string]any{"week": "last"}, false},
		{"ingredients of a recipe and a week", "food_add_missing_ingredients", map[string]any{"recipeQuery": "bolognese", "week": "this"}, false},
		{"ingredients of nothing", "food_add_missing_ingredients", map[string]any{}, false},
		{"ingredients by recipe id", "food_add_missing_ingredients", map[string]any{"recipeIds": []any{"recipe-1"}}, false},
		{"a dry run", "food_add_missing_ingredients", map[string]any{"week": "this", "dryRun": true}, false},
	} {
		if got := validAction(plannedAction{Tool: tc.tool, Arguments: tc.arguments}); got != tc.valid {
			t.Errorf("%s: validAction = %v, want %v", tc.name, got, tc.valid)
		}
	}
}

func recipe(id, title string, tags ...any) map[string]any {
	return map[string]any{"id": id, "title": title, "normalizedTitle": foodNormalize(title), "tags": tags}
}

func TestAMealWithoutASlotGoesToHumanReview(t *testing.T) {
	tools := newFoodTools("Pasta on Wednesday")
	tools.run(t, `{"actions":[{"tool":"food_plan_meal","arguments":{"recipeQuery":"Pasta","date":"2026-10-14"}}],"confidence":0.9}`)
	if tools.review == nil || tools.review["errorSummary"] != noSlotReason || contains(tools.calls, "food_list_recipes") || contains(tools.calls, "food_plan_meal") {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
}

func TestAMealPlansTheRecipeItsWordsName(t *testing.T) {
	bolognese, vegan := recipe("recipe-1", "Pasta Bolognese"), recipe("recipe-2", "Pasta Bolognese vegan")
	for _, tc := range []struct {
		name    string
		recipes map[string][]map[string]any
		plan    string
		want    map[string]any
	}{
		{
			// Titles are unique: the exact one wins over the others found.
			"an exact title", map[string][]map[string]any{"pasta bolognese!": {bolognese, vegan}},
			`{"tool":"food_plan_meal","arguments":{"recipeQuery":"pasta bolognese!","date":"2026-10-14","slot":"dinner"}}`,
			map[string]any{"date": "2026-10-14", "slot": "dinner", "recipeId": "recipe-1"},
		},
		{
			"the only recipe found", map[string][]map[string]any{"bolognese": {bolognese}},
			`{"tool":"food_plan_meal","arguments":{"recipeQuery":"bolognese","date":"2026-10-14","slot":"dinner","servings":4,"note":"for Anna"}}`,
			map[string]any{"date": "2026-10-14", "slot": "dinner", "recipeId": "recipe-1", "servings": 4.0, "note": "for Anna"},
		},
		{
			// "на ужин в среду паста болоньезе": found by an alternative.
			"an exact title in another language", map[string][]map[string]any{"Pasta Bolognese": {bolognese, vegan}, "bolognese": {bolognese, vegan}},
			`{"tool":"food_plan_meal","arguments":{"recipeQuery":"паста болоньезе","recipeQueryAlternatives":["Pasta Bolognese","bolognese"],"date":"2026-10-14","slot":"dinner"}}`,
			map[string]any{"date": "2026-10-14", "slot": "dinner", "recipeId": "recipe-1"},
		},
		{
			// "В пятницу на обед суп": no recipe, so the words are the meal.
			"no recipe", map[string][]map[string]any{},
			`{"tool":"food_plan_meal","arguments":{"recipeQuery":" суп ","recipeQueryAlternatives":["soup","Suppe"],"date":"2026-10-16","slot":"lunch"}}`,
			map[string]any{"date": "2026-10-16", "slot": "lunch", "text": "суп"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := newFoodTools("a meal")
			tools.recipes = tc.recipes
			tools.run(t, `{"actions":[`+tc.plan+`],"confidence":0.95}`)
			input := tools.inputs["food_plan_meal"][0]
			key, _ := input["idempotencyKey"].(string)
			delete(input, "idempotencyKey")
			if !reflect.DeepEqual(input, tc.want) || !strings.HasPrefix(key, "food-") {
				t.Fatalf("food_plan_meal input = %#v (key %q), want %#v", input, key, tc.want)
			}
			tools.processed(t)
		})
	}
}

func TestSeveralRecipesForAMealGoToHumanReview(t *testing.T) {
	tools := newFoodTools("pasta for dinner on Wednesday")
	tools.recipes = map[string][]map[string]any{"pasta": {recipe("recipe-1", "Pasta Bolognese", "pasta", "italian"), recipe("recipe-3", "Pasta Carbonara")}}
	tools.run(t, `{"actions":[{"tool":"food_plan_meal","arguments":{"recipeQuery":"pasta","date":"2026-10-14","slot":"dinner"}}],"confidence":0.95}`)

	if contains(tools.calls, "food_plan_meal") || contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v", tools.calls)
	}
	if tools.review == nil || tools.review["errorSummary"] != "Several recipes match: choose the one to plan" {
		t.Fatalf("review = %#v", tools.review)
	}
	candidates := tools.review["candidates"].([]map[string]any)
	if len(candidates) != 2 || candidates[0]["id"] != "recipe-1" || candidates[0]["kind"] != "recipe" || candidates[0]["title"] != "Pasta Bolognese · pasta, italian" || candidates[1]["title"] != "Pasta Carbonara" {
		t.Fatalf("candidates = %#v", candidates)
	}
	if proposed := tools.review["proposedAction"].(plannedAction); proposed.Tool != "food_plan_meal" || proposed.Arguments["slot"] != "dinner" {
		t.Fatalf("proposed = %#v", proposed)
	}

	// Approving a recipe plans it without searching again.
	approved := newFoodTools("pasta for dinner on Wednesday")
	approved.approved = map[string]any{"tool": "food_plan_meal", "arguments": map[string]any{"recipeQuery": "pasta", "date": "2026-10-14", "slot": "dinner", "taskId": "recipe-3"}}
	approved.run(t, `{"actions":[],"ambiguous":true,"confidence":0}`)
	if contains(approved.calls, "food_list_recipes") {
		t.Fatalf("searched again: %#v", approved.calls)
	}
	if input := approved.inputs["food_plan_meal"][0]; input["recipeId"] != "recipe-3" || input["text"] != nil || input["recipeQuery"] != nil {
		t.Fatalf("food_plan_meal input = %#v", input)
	}
	approved.processed(t)
}

func TestMealWritesCarryDeterministicKeys(t *testing.T) {
	plan := `{"actions":[{"tool":"food_plan_meal","arguments":{"recipeQuery":"суп","date":"2026-10-16","slot":"lunch"}},{"tool":"food_clear_meal","arguments":{"date":"2026-10-17","slot":"breakfast"}}],"confidence":0.95}`
	keys := func(plan string) (string, string) {
		tools := newFoodTools("В пятницу на обед суп, в субботу без завтрака")
		tools.run(t, plan)
		cleared := tools.inputs["food_clear_meal"][0]
		if cleared["date"] != "2026-10-17" || cleared["slot"] != "breakfast" || len(cleared) != 3 {
			t.Fatalf("food_clear_meal input = %#v", cleared)
		}
		return tools.inputs["food_plan_meal"][0]["idempotencyKey"].(string), cleared["idempotencyKey"].(string)
	}
	meal, clear := keys(plan)
	// The capture retried and planned again, here in the other order.
	againMeal, againClear := keys(`{"actions":[{"tool":"food_clear_meal","arguments":{"date":"2026-10-17","slot":"breakfast"}},{"tool":"food_plan_meal","arguments":{"recipeQuery":"суп","date":"2026-10-16","slot":"lunch"}}],"confidence":0.95}`)
	if meal != againMeal || clear != againClear || meal == clear {
		t.Fatalf("keys %q %q, then %q %q", meal, clear, againMeal, againClear)
	}
	if other, _ := keys(strings.Replace(plan, "2026-10-16", "2026-10-23", 1)); other == meal {
		t.Fatal("another meal reused the key")
	}
}

func TestMissingIngredientsOfTheRecipeTheWordsName(t *testing.T) {
	tools := newFoodTools("Add the ingredients for the bolognese to the shopping list.")
	tools.recipes = map[string][]map[string]any{"bolognese": {recipe("recipe-1", "Pasta Bolognese")}}
	tools.run(t, `{"actions":[{"tool":"food_add_missing_ingredients","arguments":{"recipeQuery":"bolognese","recipeQueryAlternatives":["Bolognese","болоньезе"]}}],"confidence":0.95}`)
	input := tools.inputs["food_add_missing_ingredients"][0]
	if !reflect.DeepEqual(input["recipeIds"], []any{"recipe-1"}) || len(input) != 2 || !strings.HasPrefix(input["idempotencyKey"].(string), "food-") {
		t.Fatalf("food_add_missing_ingredients input = %#v", input)
	}
	tools.processed(t)

	// "купи всё для борща" without a borscht recipe: the owner expects
	// groceries, so it waits for them; as in any mixed capture, the actions
	// after it do not run, and nothing is added or created instead.
	none := newFoodTools("купи всё для борща и позвони сантехнику")
	none.run(t, `{"actions":[{"tool":"food_add_missing_ingredients","arguments":{"recipeQuery":" борщ ","recipeQueryAlternatives":["borscht","Borschtsch"]}},{"tool":"task_create","arguments":{"title":"Позвонить сантехнику"}}],"confidence":0.95}`)
	if contains(none.calls, "food_add_missing_ingredients") || contains(none.calls, "food_add_shopping_items") || contains(none.calls, "task_create") || contains(none.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v", none.calls)
	}
	if got, want := none.searches, []string{"борщ", "borscht", "Borschtsch"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("searched %q, want %q", got, want)
	}
	if want := "No recipe is called “борщ”. Add the recipe on the Food page, or say which items to buy."; none.review == nil || none.review["errorSummary"] != want || len(none.review["candidates"].([]map[string]any)) != 0 {
		t.Fatalf("review = %#v", none.review)
	}
	if proposed := none.review["proposedAction"].(plannedAction); proposed.Tool != "food_add_missing_ingredients" {
		t.Fatalf("proposed = %#v", proposed)
	}
	// Planned first, the Task has run already.
	first := newFoodTools("позвони сантехнику и купи всё для борща")
	first.run(t, `{"actions":[{"tool":"task_create","arguments":{"title":"Позвонить сантехнику"}},{"tool":"food_add_missing_ingredients","arguments":{"recipeQuery":"борщ"}}],"confidence":0.95}`)
	if len(first.inputs["task_create"]) != 1 || first.review == nil || contains(first.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v, review = %#v", first.calls, first.review)
	}

	several := newFoodTools("alles für Pasta einkaufen")
	several.recipes = map[string][]map[string]any{"Pasta": {recipe("recipe-1", "Pasta Bolognese"), recipe("recipe-3", "Pasta Carbonara")}}
	several.run(t, `{"actions":[{"tool":"food_add_missing_ingredients","arguments":{"recipeQuery":"Pasta"}}],"confidence":0.95}`)
	if contains(several.calls, "food_add_missing_ingredients") || several.review["errorSummary"] != "Several recipes match: choose the one whose ingredients to add" {
		t.Fatalf("calls = %#v, review = %#v", several.calls, several.review)
	}
}

func TestMissingIngredientsOfAWeekAreItsMondayToSunday(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	for _, tc := range []struct {
		name     string
		captured time.Time
		week     string
		from, to string
	}{
		// Sunday 23:30 in Berlin is still that week, though Monday is near.
		{"sunday night", time.Date(2026, 10, 11, 23, 30, 0, 0, berlin), "this", "2026-10-05", "2026-10-11"},
		// Monday 00:30 in Berlin is Sunday 22:30 UTC: the new week.
		{"monday just after midnight", time.Date(2026, 10, 12, 0, 30, 0, 0, berlin), "this", "2026-10-12", "2026-10-18"},
		{"wednesday", time.Date(2026, 10, 14, 12, 0, 0, 0, berlin), "this", "2026-10-12", "2026-10-18"},
		{"next week", time.Date(2026, 10, 14, 12, 0, 0, 0, berlin), "next", "2026-10-19", "2026-10-25"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := newFoodTools("buy what I need for this week's meals")
			tools.createdAt, tools.timeZone = tc.captured.UTC(), "Europe/Berlin"
			tools.run(t, `{"actions":[{"tool":"food_add_missing_ingredients","arguments":{"week":"`+tc.week+`"}}],"confidence":0.95}`)
			input := tools.inputs["food_add_missing_ingredients"][0]
			if input["from"] != tc.from || input["to"] != tc.to || input["week"] != nil || contains(tools.calls, "food_list_recipes") {
				t.Fatalf("food_add_missing_ingredients input = %#v, want %s to %s", input, tc.from, tc.to)
			}
		})
	}
	// Other days are sent as planned.
	tools := newFoodTools("buy what I need for the weekend")
	tools.run(t, `{"actions":[{"tool":"food_add_missing_ingredients","arguments":{"from":"2026-10-17","to":"2026-10-18"}}],"confidence":0.95}`)
	if input := tools.inputs["food_add_missing_ingredients"][0]; input["from"] != "2026-10-17" || input["to"] != "2026-10-18" {
		t.Fatalf("food_add_missing_ingredients input = %#v", input)
	}
}

func TestFoodNormalizeIsFoodsComparison(t *testing.T) {
	for in, want := range map[string]string{"  Pasta  Bolognese! ": "pasta bolognese", "Ёжики": "ежики", "Ｐａｓｔａ": "pasta", "«Борщ»": "борщ", "Pasta-Bolognese": "pasta-bolognese"} {
		if got := foodNormalize(in); got != want {
			t.Errorf("foodNormalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlanPromptPlansMealsAndTheirIngredients(t *testing.T) {
	for _, want := range []string{
		"|food_plan_meal|food_clear_meal|food_add_missing_ingredients",
		"Meals come before the grocery rule.",
		`(\"Lasagne for dinner on Thursday\", \"на ужин в среду паста\", \"Mittwoch Abendessen: Pasta\") is food_plan_meal, never task_create, calendar_create, or food_add_shopping_items`,
		`slot (breakfast for breakfast, завтрак, or Frühstück; lunch for lunch, обед, or Mittagessen; dinner for dinner, supper, ужин, or Abendessen)`,
		"otherwise plans recipeQuery as free text",
		`with reason \"Say which meal it is: breakfast, lunch, or dinner.\"`,
		`(\"cancel dinner on Friday\", \"убери ужин в пятницу\", \"Samstag kein Frühstück\") is food_clear_meal, never calendar_cancel`,
		`\"купи всё для борща\"`,
		`week (\"this\" for the current week, Monday to Sunday, or \"next\")`,
		`the grocery rule stands: \"buy pasta\" is food_add_shopping_items`,
		"expiresOn, and a meal's date as the owner's local date",
	} {
		want = strings.ReplaceAll(want, `\"`, `"`)
		if !strings.Contains(planPrompt+timeContext(time.Now(), time.Time{}, time.UTC), want) {
			t.Errorf("planPrompt lacks %q", want)
		}
	}
}
