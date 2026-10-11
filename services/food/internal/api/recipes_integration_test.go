package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
)

func (c apiClient) recipe(body map[string]any) generated.Recipe {
	c.t.Helper()
	return expect[generated.Recipe](c, http.MethodPost, "/v1/recipes", body, http.StatusCreated)
}

func (c apiClient) recipes(query string) generated.RecipeList {
	c.t.Helper()
	return expect[generated.RecipeList](c, http.MethodGet, "/v1/recipes?"+query, nil, http.StatusOK)
}

func recipeTitles(items []generated.Recipe) string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Title)
	}
	return strings.Join(out, ", ")
}

func ingredientLine(items []generated.Ingredient) string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		text := item.Name
		if item.Amount != nil {
			text += " " + *item.Amount
		}
		if item.Note != nil {
			text += " (" + *item.Note + ")"
		}
		out = append(out, text)
	}
	return strings.Join(out, ", ")
}

func (c apiClient) plan(date, slot string, body map[string]any) generated.MealPlanEntry {
	c.t.Helper()
	return expect[generated.MealPlanEntry](c, http.MethodPut, "/v1/meal-plan/"+date+"/"+slot, body, http.StatusOK)
}

func (c apiClient) mealPlan(from, to string) []generated.MealPlanEntry {
	c.t.Helper()
	return expect[generated.MealPlanList](c, http.MethodGet, "/v1/meal-plan?from="+from+"&to="+to, nil, http.StatusOK).Items
}

func TestRecipesAreCreatedFoundChangedAndDeleted(t *testing.T) {
	c := newAPIClient(t)
	seen := 0
	pasta := c.recipe(map[string]any{
		"title": "  Pasta Bolognese ", "servings": 4,
		"ingredients": []any{
			map[string]any{"name": "Spaghetti", "amount": "500 g"},
			map[string]any{"name": " Hackfleisch ", "amount": "400 g", "note": "gemischt"},
			map[string]any{"name": "Tomaten", "amount": "1 Dose"},
		},
		"steps": "Zwiebeln anbraten.\r\nHack dazu.\r\nKöcheln lassen.",
		"tags":  []any{"Italian", "quick", "ITALIAN"}, "sourceUrl": "https://example.com/bolognese",
	})
	if pasta.Title != "Pasta Bolognese" || pasta.NormalizedTitle != "pasta bolognese" || pasta.Servings == nil || *pasta.Servings != 4 ||
		ingredientLine(pasta.Ingredients) != "Spaghetti 500 g, Hackfleisch 400 g (gemischt), Tomaten 1 Dose" ||
		pasta.Steps == nil || *pasta.Steps != "Zwiebeln anbraten.\nHack dazu.\nKöcheln lassen." ||
		strings.Join(pasta.Tags, ",") != "italian,quick" || pasta.CreatedBy.Kind != generated.Dev {
		t.Fatalf("created = %+v", pasta)
	}
	c.recipe(map[string]any{"title": "Chili sin carne", "tags": []any{"vegan"}, "ingredients": []any{map[string]any{"name": "Bohnen"}}})
	c.recipe(map[string]any{"title": "Porridge"})
	if events := c.recorder.since(&seen); eventTypes(events) != recipeCreated+" "+recipeCreated+" "+recipeCreated ||
		events[0].Summary != "Recipe “Pasta Bolognese” created (3 ingredients)" || events[0].Subject.Type != "food_recipe" {
		t.Fatalf("events = %+v", events)
	}
	// A title names one recipe, compared normalised.
	c.expectError(http.MethodPost, "/v1/recipes", map[string]any{"title": "PASTA bolognese!"}, http.StatusConflict, "recipe_exists")
	c.expectError(http.MethodPost, "/v1/recipes", map[string]any{"title": "Toast", "sourceUrl": "ftp://example.com"}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/recipes", map[string]any{"title": "Toast", "servings": 0}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/recipes", map[string]any{"title": "Toast", "tags": []any{"!!"}}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/recipes", map[string]any{"title": "Toast", "ingredients": []any{map[string]any{"name": " "}}}, http.StatusBadRequest, "invalid_request")

	// Search: every word of q in the title (also inside a word), a tag, an
	// ingredient's name containing the text; by title, paged.
	if got := recipeTitles(c.recipes("").Items); got != "Chili sin carne, Pasta Bolognese, Porridge" {
		t.Fatalf("all = %s", got)
	}
	for query, want := range map[string]string{
		"q=bolo":                 "Pasta Bolognese",
		"q=Bolognese+PASTA":      "Pasta Bolognese",
		"q=pasta+chili":          "",
		"tag=Italian":            "Pasta Bolognese",
		"tag=vegan":              "Chili sin carne",
		"ingredient=hack":        "Pasta Bolognese",
		"ingredient=bohnen":      "Chili sin carne",
		"q=pasta&ingredient=boh": "",
	} {
		if got := recipeTitles(c.recipes(query).Items); got != want {
			t.Errorf("%s = %q, want %q", query, got, want)
		}
	}
	first := c.recipes("limit=2")
	if recipeTitles(first.Items) != "Chili sin carne, Pasta Bolognese" || first.NextCursor == nil {
		t.Fatalf("first page = %+v", first)
	}
	if rest := c.recipes("limit=2&cursor=" + *first.NextCursor); recipeTitles(rest.Items) != "Porridge" || rest.NextCursor != nil {
		t.Fatalf("second page = %+v", rest)
	}
	c.expectError(http.MethodGet, "/v1/recipes?cursor=nonsense", nil, http.StatusBadRequest, "invalid_cursor")

	// Planned, then renamed: the plan follows the title.
	c.plan("2026-10-12", "dinner", map[string]any{"recipeId": pasta.Id})
	id := pasta.Id.String()
	renamed := expect[generated.Recipe](c, http.MethodPatch, "/v1/recipes/"+id, map[string]any{
		"title": "Spaghetti Bolognese", "servings": nil, "ingredients": []any{map[string]any{"name": "Spaghetti", "amount": "400 g"}},
		"tags": []any{"pasta"}, "note": "Oma's",
	}, http.StatusOK)
	if renamed.Title != "Spaghetti Bolognese" || renamed.Servings != nil || ingredientLine(renamed.Ingredients) != "Spaghetti 400 g" ||
		strings.Join(renamed.Tags, ",") != "pasta" || renamed.Note == nil || *renamed.Steps != *pasta.Steps {
		t.Fatalf("renamed = %+v", renamed)
	}
	if plan := c.mealPlan("2026-10-12", "2026-10-12"); len(plan) != 1 || plan[0].Title != "Spaghetti Bolognese" {
		t.Fatalf("plan after rename = %+v", plan)
	}
	events := c.recorder.since(&seen)
	if last := events[len(events)-1]; last.Type != recipeUpdated || last.Summary != "Recipe “Spaghetti Bolognese” updated: title, servings, ingredients, tags, note" {
		t.Fatalf("update event = %+v", last)
	}
	// The same again changes nothing and records nothing.
	expect[generated.Recipe](c, http.MethodPatch, "/v1/recipes/"+id, map[string]any{"title": "Spaghetti Bolognese", "ingredients": []any{map[string]any{"name": "Spaghetti", "amount": "400 g"}}}, http.StatusOK)
	if events := c.recorder.since(&seen); len(events) != 0 {
		t.Fatalf("a change that changes nothing recorded %s", eventTypes(events))
	}
	c.expectError(http.MethodPatch, "/v1/recipes/"+id, map[string]any{"title": "porridge"}, http.StatusConflict, "recipe_exists")
	c.expectError(http.MethodPatch, "/v1/recipes/"+id, map[string]any{"ingredients": nil}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPatch, "/v1/recipes/"+id, map[string]any{}, http.StatusBadRequest, "invalid_request")

	// Deleted: the plan keeps the meal as plain text.
	if status, raw := c.do(http.MethodDelete, "/v1/recipes/"+id, nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", status, raw)
	}
	if plan := c.mealPlan("2026-10-12", "2026-10-12"); len(plan) != 1 || plan[0].Title != "Spaghetti Bolognese" || plan[0].RecipeId != nil {
		t.Fatalf("plan after delete = %+v", plan)
	}
	c.expectError(http.MethodGet, "/v1/recipes/"+id, nil, http.StatusNotFound, "not_found")
	if n := c.count("recipe_ingredients", "true"); n != 1 {
		t.Fatalf("%d ingredients left", n)
	}
	if events := c.recorder.since(&seen); eventTypes(events) != recipeDeleted || events[0].Summary != "Recipe “Spaghetti Bolognese” deleted" {
		t.Fatalf("delete events = %+v", events)
	}
}

func TestTheMealPlanHoldsOneMealPerSlot(t *testing.T) {
	c := newAPIClient(t)
	soup := c.recipe(map[string]any{"title": "Leek soup", "servings": 2})
	seen := len(c.recorder.all())
	dinner := c.plan("2026-10-12", "dinner", map[string]any{"recipeId": soup.Id, "servings": 3})
	if dinner.Title != "Leek soup" || dinner.RecipeId == nil || *dinner.RecipeId != soup.Id || dinner.Servings == nil || *dinner.Servings != 3 ||
		dinner.Date.Format("2006-01-02") != "2026-10-12" || dinner.Slot != generated.Dinner {
		t.Fatalf("dinner = %+v", dinner)
	}
	c.plan("2026-10-12", "breakfast", map[string]any{"text": " Porridge "})
	c.plan("2026-10-13", "lunch", map[string]any{"text": "Leftovers", "note": "from Monday"})
	// A taken slot is replaced, the entry keeps its id.
	pizza := c.plan("2026-10-12", "dinner", map[string]any{"text": "Pizza"})
	if pizza.Id != dinner.Id || pizza.RecipeId != nil || pizza.Title != "Pizza" || pizza.Servings != nil {
		t.Fatalf("replaced = %+v", pizza)
	}
	events := c.recorder.since(&seen)
	if eventTypes(events) != strings.TrimSpace(strings.Repeat(mealPlanned+" ", 4)) {
		t.Fatalf("events = %s", eventTypes(events))
	}
	if events[0].Summary != "Planned “Leek soup” for dinner on Mon 12 Oct" || events[0].Subject.Type != "food_meal_plan" || events[0].Subject.ID != "2026-10-12:dinner" ||
		events[3].Summary != "Planned “Pizza” for dinner on Mon 12 Oct, replacing “Leek soup”" {
		t.Fatalf("summaries = %q / %q", events[0].Summary, events[3].Summary)
	}
	// The same again changes nothing.
	c.plan("2026-10-12", "dinner", map[string]any{"text": "Pizza"})
	if events := c.recorder.since(&seen); len(events) != 0 {
		t.Fatalf("an unchanged slot recorded %s", eventTypes(events))
	}
	// By day, then breakfast, lunch, dinner.
	var got []string
	for _, entry := range c.mealPlan("2026-10-12", "2026-10-13") {
		got = append(got, entry.Date.Format("02")+" "+string(entry.Slot)+" "+entry.Title)
	}
	if strings.Join(got, ", ") != "12 breakfast Porridge, 12 dinner Pizza, 13 lunch Leftovers" {
		t.Fatalf("plan = %s", strings.Join(got, ", "))
	}
	if len(c.mealPlan("2026-10-14", "2026-11-13")) != 0 {
		t.Fatal("an empty month lists entries")
	}
	c.expectError(http.MethodGet, "/v1/meal-plan?from=2026-10-01&to=2026-11-01", nil, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodGet, "/v1/meal-plan?from=2026-10-13&to=2026-10-12", nil, http.StatusBadRequest, "invalid_request")
	if status, _ := c.do(http.MethodGet, "/v1/meal-plan?from=2026-10-13", nil); status != http.StatusBadRequest {
		t.Fatalf("without to: %d", status)
	}
	c.expectError(http.MethodPut, "/v1/meal-plan/2026-10-12/supper", map[string]any{"text": "x"}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPut, "/v1/meal-plan/2026-10-12/lunch", map[string]any{"text": "x", "recipeId": soup.Id}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPut, "/v1/meal-plan/2026-10-12/lunch", map[string]any{"servings": 2}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPut, "/v1/meal-plan/2026-10-12/lunch", map[string]any{"text": "  "}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPut, "/v1/meal-plan/2026-10-12/lunch", map[string]any{"recipeId": "7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10"}, http.StatusBadRequest, "unknown_recipe")
	c.expectError(http.MethodPut, "/v1/meal-plan/2026-10-12/lunch", map[string]any{"text": "x"}, http.StatusBadRequest, "actor_required", "X-Life-OS-Actor", "")

	// Cleared once; a second clear finds nothing.
	if status, raw := c.do(http.MethodDelete, "/v1/meal-plan/2026-10-12/dinner", nil); status != http.StatusNoContent {
		t.Fatalf("clear: %d %s", status, raw)
	}
	c.expectError(http.MethodDelete, "/v1/meal-plan/2026-10-12/dinner", nil, http.StatusNotFound, "not_found")
	if events := c.recorder.since(&seen); eventTypes(events) != mealCleared || events[0].Summary != "Cleared “Pizza” from dinner on Mon 12 Oct" {
		t.Fatalf("clear events = %+v", events)
	}
}

func ingredientResults(items []generated.IngredientResult) string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		text := item.Name + ":" + string(item.Status)
		if item.Amount != nil {
			text += ":" + *item.Amount
		}
		out = append(out, text)
	}
	return strings.Join(out, ", ")
}

func TestMissingIngredientsGoOnTheList(t *testing.T) {
	c := newAPIClient(t)
	pasta := c.recipe(map[string]any{"title": "Pasta", "ingredients": []any{
		map[string]any{"name": "Spaghetti", "amount": "500 g"},
		map[string]any{"name": "Hackfleisch", "amount": "400 g"},
		map[string]any{"name": "Salz"},
		map[string]any{"name": "Zwiebeln", "amount": "2"},
	}})
	chili := c.recipe(map[string]any{"title": "Chili", "ingredients": []any{
		map[string]any{"name": "hackfleisch", "amount": "200 g"},
		map[string]any{"name": "Kidneybohnen", "amount": "1 Dose"},
		map[string]any{"name": "Milch"},
	}})
	// Salt is at home, onions are already on the list; milk was used up.
	expect[generated.AddPantryItemsResponse](c, http.MethodPost, "/v1/pantry-items", map[string]any{"items": []any{
		map[string]any{"name": "Salz"}, map[string]any{"name": "Milch"},
	}}, http.StatusCreated)
	milk := c.pantry("").Items
	for _, item := range milk {
		if item.Name == "Milch" {
			expect[generated.PantryItem](c, http.MethodPost, "/v1/pantry-items/"+item.Id.String()+"/used-up", nil, http.StatusOK)
		}
	}
	onions := c.add(map[string]any{"name": "zwiebeln"})[0].Item
	seen := len(c.recorder.all())
	body := map[string]any{"recipeIds": []any{pasta.Id, chili.Id}}

	// A dry run says what would happen and changes nothing.
	dry := expect[generated.FromRecipesResult](c, http.MethodPost, "/v1/shopping-items/from-recipes", map[string]any{"recipeIds": []any{pasta.Id, chili.Id}, "dryRun": true}, http.StatusOK)
	want := "Spaghetti:added:500 g, Hackfleisch:added:400 g + 200 g, Salz:in_pantry, Zwiebeln:on_list:2, Kidneybohnen:added:1 Dose, Milch:added"
	if !dry.DryRun || ingredientResults(dry.Items) != want {
		t.Fatalf("dry run = %s", ingredientResults(dry.Items))
	}
	if refs := dry.Items[1].Recipes; len(refs) != 2 || refs[0].Title != "Pasta" || refs[1].Title != "Chili" {
		t.Fatalf("recipes of the shared ingredient = %+v", refs)
	}
	if dry.Items[2].PantryItem == nil || dry.Items[2].PantryItem.Name != "Salz" || dry.Items[3].ShoppingItem == nil || dry.Items[3].ShoppingItem.Id != onions.Id ||
		dry.Items[0].ShoppingItem != nil {
		t.Fatalf("dry run details = %+v", dry.Items)
	}
	if n := c.count("shopping_items", "true"); n != 1 {
		t.Fatalf("a dry run added items: %d", n)
	}
	if events := c.recorder.since(&seen); len(events) != 0 {
		t.Fatalf("a dry run recorded %s", eventTypes(events))
	}

	// For real: the same answer, with the items added.
	status, first := c.do(http.MethodPost, "/v1/shopping-items/from-recipes", body, "Idempotency-Key", "week-1")
	if status != http.StatusOK {
		t.Fatalf("add: %d %s", status, first)
	}
	got := expect[generated.FromRecipesResult](c, http.MethodPost, "/v1/shopping-items/from-recipes", body, http.StatusOK, "Idempotency-Key", "week-1")
	if got.DryRun || ingredientResults(got.Items) != want {
		t.Fatalf("added = %s", ingredientResults(got.Items))
	}
	minced := got.Items[1].ShoppingItem
	if minced == nil || minced.Source != generated.ItemSourceRecipe || minced.SourceRef == nil || *minced.SourceRef != pasta.Id.String() ||
		minced.Amount == nil || *minced.Amount != "400 g + 200 g" || minced.Aisle != generated.AisleMeatFish || minced.State != generated.Needed {
		t.Fatalf("minced meat = %+v", minced)
	}
	if beans := got.Items[4].ShoppingItem; beans == nil || *beans.SourceRef != chili.Id.String() {
		t.Fatalf("beans = %+v", beans)
	}
	// By aisle: produce, dairy, meat, pantry.
	if names := names(c.list("state=needed").Items); names != "zwiebeln, Milch, Hackfleisch, Kidneybohnen, Spaghetti" {
		t.Fatalf("list = %s", names)
	}
	// The replay recorded nothing more: four items added once.
	if events := c.recorder.since(&seen); eventTypes(events) != strings.TrimSpace(strings.Repeat(shoppingItemAdded+" ", 4)) {
		t.Fatalf("events = %s", eventTypes(events))
	}
	// Again without a key: everything is on the list now.
	again := expect[generated.FromRecipesResult](c, http.MethodPost, "/v1/shopping-items/from-recipes", body, http.StatusOK)
	if ingredientResults(again.Items) != "Spaghetti:on_list:500 g, Hackfleisch:on_list:400 g + 200 g, Salz:in_pantry, Zwiebeln:on_list:2, Kidneybohnen:on_list:1 Dose, Milch:on_list" {
		t.Fatalf("again = %s", ingredientResults(again.Items))
	}

	c.expectError(http.MethodPost, "/v1/shopping-items/from-recipes", map[string]any{}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/shopping-items/from-recipes", map[string]any{"recipeIds": []any{pasta.Id}, "from": "2026-10-12", "to": "2026-10-18"}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/shopping-items/from-recipes", map[string]any{"from": "2026-10-12"}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/shopping-items/from-recipes", map[string]any{"from": "2026-10-01", "to": "2026-11-01"}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/shopping-items/from-recipes", map[string]any{"recipeIds": []any{}}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/shopping-items/from-recipes", map[string]any{"recipeIds": []any{"7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10"}}, http.StatusNotFound, "not_found")
}

func TestMissingIngredientsOfAWeekCountEachPlannedRecipeOnce(t *testing.T) {
	c := newAPIClient(t)
	pasta := c.recipe(map[string]any{"title": "Pasta", "ingredients": []any{map[string]any{"name": "Spaghetti", "amount": "500 g"}}})
	soup := c.recipe(map[string]any{"title": "Soup", "ingredients": []any{map[string]any{"name": "Lauch", "amount": "2"}, map[string]any{"name": "Spaghetti", "amount": "100 g"}}})
	c.plan("2026-10-14", "lunch", map[string]any{"recipeId": soup.Id})
	c.plan("2026-10-12", "dinner", map[string]any{"recipeId": pasta.Id})
	c.plan("2026-10-16", "dinner", map[string]any{"recipeId": pasta.Id})
	c.plan("2026-10-13", "dinner", map[string]any{"text": "Pizza"})
	// Outside the week.
	c.plan("2026-10-19", "dinner", map[string]any{"recipeId": soup.Id})
	got := expect[generated.FromRecipesResult](c, http.MethodPost, "/v1/shopping-items/from-recipes", map[string]any{"from": "2026-10-12", "to": "2026-10-18", "dryRun": true}, http.StatusOK)
	if ingredientResults(got.Items) != "Spaghetti:added:500 g + 100 g, Lauch:added:2" {
		t.Fatalf("week = %s", ingredientResults(got.Items))
	}
	if empty := expect[generated.FromRecipesResult](c, http.MethodPost, "/v1/shopping-items/from-recipes", map[string]any{"from": "2026-11-01", "to": "2026-11-07"}, http.StatusOK); len(empty.Items) != 0 {
		t.Fatalf("an empty week = %+v", empty)
	}
	if n := c.count("shopping_items", "true"); n != 0 {
		t.Fatalf("%d items", n)
	}
}
