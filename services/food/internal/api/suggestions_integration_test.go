package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
)

const agent = "agent:test-ideas"

func (c apiClient) suggestions(query string) generated.SuggestionList {
	c.t.Helper()
	return expect[generated.SuggestionList](c, http.MethodGet, "/v1/suggestions?"+query, nil, http.StatusOK)
}

func (c apiClient) idea(body map[string]any) generated.Idea {
	c.t.Helper()
	return expect[generated.Idea](c, http.MethodPost, "/v1/ideas", body, http.StatusCreated, "X-Life-OS-Actor", agent)
}

func suggestionTitles(items []generated.Suggestion) string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Recipe.Title)
	}
	return strings.Join(out, ", ")
}

func reasonTexts(item generated.Suggestion) string {
	out := make([]string, 0, len(item.Reasons))
	for _, reason := range item.Reasons {
		out = append(out, reason.Text)
	}
	return strings.Join(out, "; ")
}

func methodNames(methods []generated.CookingMethod) string {
	out := make([]string, 0, len(methods))
	for _, method := range methods {
		out = append(out, string(method))
	}
	return strings.Join(out, ",")
}

func TestPreferencesAreReadAndReplaced(t *testing.T) {
	c := newAPIClient(t)
	seen := 0
	initial := expect[generated.Preferences](c, http.MethodGet, "/v1/preferences", nil, http.StatusOK)
	if methodNames(initial.Methods) != "air_fryer" || len(initial.Dislikes) != 0 || initial.DietaryNotes != nil {
		t.Fatalf("initial = %+v", initial)
	}
	body := map[string]any{"methods": []string{"oven", "air_fryer", "oven"}, "dislikes": []string{"Koriander", " koriander ", "Rosinen"}, "dietaryNotes": "Wenig rotes Fleisch"}
	updated := expect[generated.Preferences](c, http.MethodPut, "/v1/preferences", body, http.StatusOK)
	if methodNames(updated.Methods) != "oven,air_fryer" || !slices.Equal(updated.Dislikes, []string{"Koriander", "Rosinen"}) ||
		*updated.DietaryNotes != "Wenig rotes Fleisch" || updated.UpdatedBy.Kind != generated.Dev {
		t.Fatalf("updated = %+v", updated)
	}
	// The same again changes nothing and records nothing.
	expect[generated.Preferences](c, http.MethodPut, "/v1/preferences", body, http.StatusOK)
	events := c.recorder.since(&seen)
	if eventTypes(events) != "food.preferences.updated" || events[0].Summary != "Cooking preferences updated: methods, dislikes, dietary notes" {
		t.Fatalf("events = %s %q", eventTypes(events), events[0].Summary)
	}
	c.expectError(http.MethodPut, "/v1/preferences", map[string]any{"methods": []string{"deep_fryer"}, "dislikes": []string{}}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPut, "/v1/preferences", map[string]any{"methods": []string{}}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPut, "/v1/preferences", map[string]any{"methods": []string{}, "dislikes": []string{"!!"}}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPut, "/v1/preferences", map[string]any{"methods": []string{}, "dislikes": []string{}}, http.StatusBadRequest, "actor_required", "X-Life-OS-Actor", "")
	cleared := expect[generated.Preferences](c, http.MethodPut, "/v1/preferences", map[string]any{"methods": []string{}, "dislikes": []string{}}, http.StatusOK)
	if len(cleared.Methods) != 0 || len(cleared.Dislikes) != 0 || cleared.DietaryNotes != nil {
		t.Fatalf("cleared = %+v", cleared)
	}
}

func TestRecipeMethodsAreInferredUntilSet(t *testing.T) {
	c := newAPIClient(t)
	wings := c.recipe(map[string]any{"title": "Air Fryer Chicken Wings", "steps": "Toss the wings in oil.\nAir fry at 200 °C for 20 minutes."})
	if methodNames(wings.Methods) != "air_fryer" || !wings.MethodsInferred {
		t.Fatalf("wings = %v %v", wings.Methods, wings.MethodsInferred)
	}
	path := "/v1/recipes/" + wings.Id.String()
	// Set: kept as given (in the methods' order) and no longer inferred.
	set := expect[generated.Recipe](c, http.MethodPatch, path, map[string]any{"methods": []string{"oven", "air_fryer"}}, http.StatusOK)
	if methodNames(set.Methods) != "air_fryer,oven" || set.MethodsInferred {
		t.Fatalf("set = %v %v", set.Methods, set.MethodsInferred)
	}
	if kept := expect[generated.Recipe](c, http.MethodPatch, path, map[string]any{"steps": "Im Topf kochen."}, http.StatusOK); methodNames(kept.Methods) != "air_fryer,oven" {
		t.Fatalf("kept = %v", kept.Methods)
	}
	// Null infers again, now from the new steps too.
	again := expect[generated.Recipe](c, http.MethodPatch, path, map[string]any{"methods": nil}, http.StatusOK)
	if methodNames(again.Methods) != "air_fryer,stovetop" || !again.MethodsInferred {
		t.Fatalf("again = %v %v", again.Methods, again.MethodsInferred)
	}
	none := c.recipe(map[string]any{"title": "Tomatensalat", "methods": []string{}})
	if len(none.Methods) != 0 || none.MethodsInferred {
		t.Fatalf("none = %+v", none)
	}
	russian := c.recipe(map[string]any{"title": "Курица в аэрогриле", "tags": []string{"ужин"}})
	if methodNames(russian.Methods) != "air_fryer" {
		t.Fatalf("russian = %v", russian.Methods)
	}
	c.expectError(http.MethodPost, "/v1/recipes", map[string]any{"title": "X", "methods": []string{"campfire"}}, http.StatusBadRequest, "invalid_request")
}

func TestSuggestionsRankSavedRecipesAndSayWhy(t *testing.T) {
	// fixedNow: today is 11 October 2026 in Berlin.
	c := newAPIClient(t)
	expect[generated.Preferences](c, http.MethodPut, "/v1/preferences", map[string]any{"methods": []string{"air_fryer"}, "dislikes": []string{"Koriander"}}, http.StatusOK)
	c.stock(
		map[string]any{"name": "Hähnchenbrust", "expiresOn": "2026-10-12"},
		map[string]any{"name": "Paprika"},
		map[string]any{"name": "Reis"},
		map[string]any{"name": "Sahne", "expiresOn": "2026-10-13"},
		map[string]any{"name": "Kartoffeln"},
		map[string]any{"name": "Nudeln"},
	)
	pan := c.recipe(map[string]any{"title": "Hähnchen-Reis-Pfanne", "steps": "Das Hähnchen in der Heißluftfritteuse garen.",
		"ingredients": []any{map[string]any{"name": "Hähnchen", "amount": "400 g"}, map[string]any{"name": "Paprika"}, map[string]any{"name": "Reis"},
			map[string]any{"name": "Zwiebel", "amount": "1"}, map[string]any{"name": "Salz"}}})
	c.recipe(map[string]any{"title": "Kartoffelauflauf", "steps": "Im Ofen backen.",
		"ingredients": []any{map[string]any{"name": "Kartoffeln"}, map[string]any{"name": "Sahne"}, map[string]any{"name": "Käse"}}})
	c.recipe(map[string]any{"title": "Linsensuppe", "ingredients": []any{map[string]any{"name": "Linsen"}, map[string]any{"name": "Karotten"}}})
	curry := c.recipe(map[string]any{"title": "Curry", "ingredients": []any{map[string]any{"name": "Reis"}, map[string]any{"name": "Frischer Koriander"}}})
	pesto := c.recipe(map[string]any{"title": "Nudeln mit Pesto", "ingredients": []any{map[string]any{"name": "Nudeln"}, map[string]any{"name": "Pesto"}}})
	// Habits: the pan three times for dinner in the last weeks, the pasta
	// yesterday.
	for _, day := range []string{"2026-09-15", "2026-09-22", "2026-09-29"} {
		c.plan(day, "dinner", map[string]any{"recipeId": pan.Id})
	}
	c.plan("2026-10-10", "lunch", map[string]any{"recipeId": pesto.Id})

	list := c.suggestions("")
	if list.Today.Format("2006-01-02") != "2026-10-11" || list.MealSlot != nil {
		t.Fatalf("today = %s", list.Today)
	}
	if got := suggestionTitles(list.Items); got != "Hähnchen-Reis-Pfanne, Kartoffelauflauf, Nudeln mit Pesto, Linsensuppe" {
		t.Fatalf("order = %s", got)
	}
	top := list.Items[0]
	// Everything at home was added today, so it counts as bought lately.
	want := "You have 3 of its 4 ingredients; Hähnchenbrust expires tomorrow; Air fryer, your preferred way; You had it 3 times in the last 60 days; You bought 3 of its ingredients lately"
	if got := reasonTexts(top); got != want {
		t.Fatalf("reasons = %s", got)
	}
	sum := 0
	for _, reason := range top.Reasons {
		sum += reason.Points
	}
	if top.Score != sum || top.Score != 30+12+10+9+6 {
		t.Fatalf("score = %d, reasons add up to %d", top.Score, sum)
	}
	if len(top.Missing) != 1 || top.Missing[0].Name != "Zwiebel" || *top.Missing[0].Amount != "1" || !slices.Equal(top.Basics, []string{"Salz"}) {
		t.Fatalf("missing = %+v, basics = %v", top.Missing, top.Basics)
	}
	if len(top.Expiring) != 1 || top.Expiring[0].PantryItemName != "Hähnchenbrust" || top.Expiring[0].Ingredient != "Hähnchen" || *top.Expiring[0].ExpiresInDays != 1 {
		t.Fatalf("expiring = %+v", top.Expiring)
	}
	// The title's "Pfanne" is the stove.
	if methodNames(top.Recipe.Methods) != "air_fryer,stovetop" || len(top.Have) != 3 {
		t.Fatalf("recipe = %+v", top.Recipe)
	}
	if got := reasonTexts(list.Items[2]); !strings.Contains(got, "Planned yesterday") || !strings.Contains(got, "You had it once") {
		t.Fatalf("pasta = %s", got)
	}
	if len(list.Excluded) != 1 || list.Excluded[0].Recipe.Id != curry.Id || list.Excluded[0].Disliked != "Frischer Koriander" {
		t.Fatalf("excluded = %+v", list.Excluded)
	}

	// For dinner, the pan fits; the limit cuts the list.
	dinner := c.suggestions("mealSlot=dinner&limit=2")
	if got := suggestionTitles(dinner.Items); got != "Hähnchen-Reis-Pfanne, Kartoffelauflauf" || *dinner.MealSlot != generated.Dinner {
		t.Fatalf("dinner = %s", got)
	}
	if got := reasonTexts(dinner.Items[0]); !strings.HasSuffix(got, "Fits dinner") {
		t.Fatalf("dinner reasons = %s", got)
	}
	// Without the air fryer as a preference, its bonus goes.
	expect[generated.Preferences](c, http.MethodPut, "/v1/preferences", map[string]any{"methods": []string{}, "dislikes": []string{}}, http.StatusOK)
	if got := c.suggestions("limit=1").Items[0]; got.Score != 30+12+9+6 {
		t.Fatalf("without preference = %d %s", got.Score, reasonTexts(got))
	}
	if got := c.suggestions(""); len(got.Excluded) != 0 || len(got.Items) != 5 {
		t.Fatalf("without dislikes = %+v", got.Excluded)
	}
	c.expectError(http.MethodGet, "/v1/suggestions?limit=0", nil, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodGet, "/v1/suggestions?limit=51", nil, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodGet, "/v1/suggestions?mealSlot=brunch", nil, http.StatusBadRequest, "invalid_request")
}

func TestIdeasAreSuggestedSavedAndDismissed(t *testing.T) {
	c := newAPIClient(t)
	seen := 0
	c.expectError(http.MethodPost, "/v1/ideas", map[string]any{"title": "Wings", "reason": "r"}, http.StatusForbidden, "actor_not_allowed", "X-Life-OS-Actor", "user")
	wings := c.idea(map[string]any{"title": "Air fryer wings", "reason": "Uses the Hähnchen that expires tomorrow, in the air fryer",
		"ingredients": []any{map[string]any{"name": "Chicken wings", "amount": "1 kg"}, map[string]any{"name": "Paprika", "note": "smoked"}},
		"steps":       "Season.\nAir fry for 20 minutes.", "tags": []string{"Snack"}, "servings": 2})
	if wings.State != generated.New || wings.Source != generated.Ai || methodNames(wings.Methods) != "air_fryer" || wings.RecipeId != nil ||
		len(wings.Ingredients) != 2 || *wings.Ingredients[1].Note != "smoked" || wings.CreatedBy.Kind != generated.Agent || wings.Tags[0] != "snack" {
		t.Fatalf("wings = %+v", wings)
	}
	c.expectError(http.MethodPost, "/v1/ideas", map[string]any{"title": "AIR FRYER WINGS!", "reason": "again"}, http.StatusConflict, "idea_exists", "X-Life-OS-Actor", agent)
	c.recipe(map[string]any{"title": "Borschtsch"})
	c.expectError(http.MethodPost, "/v1/ideas", map[string]any{"title": "borschtsch", "reason": "r"}, http.StatusConflict, "recipe_exists", "X-Life-OS-Actor", agent)
	c.expectError(http.MethodPost, "/v1/ideas", map[string]any{"title": "Soup", "reason": "  "}, http.StatusBadRequest, "invalid_request", "X-Life-OS-Actor", agent)
	c.expectError(http.MethodPost, "/v1/ideas", map[string]any{"title": "Soup", "reason": "r", "source": "tv"}, http.StatusBadRequest, "invalid_request", "X-Life-OS-Actor", agent)
	c.expectError(http.MethodPost, "/v1/ideas", map[string]any{"title": "Soup", "reason": "r", "sourceUrl": "ftp://x"}, http.StatusBadRequest, "invalid_request", "X-Life-OS-Actor", agent)
	found := c.idea(map[string]any{"title": "Shakshuka", "reason": "Found for your eggs", "source": "web", "sourceUrl": "https://example.com/shakshuka",
		"methods": []string{"stovetop"}})
	if found.Source != generated.Web || *found.SourceUrl != "https://example.com/shakshuka" {
		t.Fatalf("found = %+v", found)
	}
	if got := expect[generated.IdeaList](c, http.MethodGet, "/v1/ideas", nil, http.StatusOK).Items; len(got) != 2 || got[0].Id != found.Id {
		t.Fatalf("new ideas = %+v", got)
	}

	// Saved: a recipe with the idea's content, the reason as its note.
	path := "/v1/ideas/" + wings.Id.String()
	saved := expect[generated.SaveIdeaResult](c, http.MethodPost, path+"/save", nil, http.StatusCreated)
	recipe := saved.Recipe
	if saved.Idea.State != generated.Saved || *saved.Idea.RecipeId != recipe.Id || recipe.Title != "Air fryer wings" || *recipe.Servings != 2 ||
		len(recipe.Ingredients) != 2 || *recipe.Ingredients[0].Amount != "1 kg" || *recipe.Note != wings.Reason || !recipe.MethodsInferred ||
		methodNames(recipe.Methods) != "air_fryer" || *recipe.Steps != "Season.\nAir fry for 20 minutes." {
		t.Fatalf("saved = %+v", saved)
	}
	again := expect[generated.SaveIdeaResult](c, http.MethodPost, path+"/save", nil, http.StatusOK)
	if again.Recipe.Id != recipe.Id {
		t.Fatalf("saved again = %+v", again)
	}
	c.expectError(http.MethodPost, path+"/dismiss", nil, http.StatusConflict, "idea_saved")
	// A web idea keeps its methods and link.
	shakshuka := expect[generated.SaveIdeaResult](c, http.MethodPost, "/v1/ideas/"+found.Id.String()+"/save", nil, http.StatusCreated).Recipe
	if methodNames(shakshuka.Methods) != "stovetop" || shakshuka.MethodsInferred || *shakshuka.SourceUrl != "https://example.com/shakshuka" {
		t.Fatalf("shakshuka = %+v", shakshuka)
	}

	// Dismissed: gone from the new ones; dismissing again records nothing.
	soup := c.idea(map[string]any{"title": "Soup", "reason": "Warm"})
	dismissed := expect[generated.Idea](c, http.MethodPost, "/v1/ideas/"+soup.Id.String()+"/dismiss", nil, http.StatusOK)
	expect[generated.Idea](c, http.MethodPost, "/v1/ideas/"+soup.Id.String()+"/dismiss", nil, http.StatusOK)
	if dismissed.State != generated.Dismissed {
		t.Fatalf("dismissed = %+v", dismissed)
	}
	if got := expect[generated.IdeaList](c, http.MethodGet, "/v1/ideas", nil, http.StatusOK).Items; len(got) != 0 {
		t.Fatalf("new ideas = %+v", got)
	}
	if got := expect[generated.IdeaList](c, http.MethodGet, "/v1/ideas?state=dismissed", nil, http.StatusOK).Items; len(got) != 1 {
		t.Fatalf("dismissed ideas = %+v", got)
	}
	// The title is free for a new idea again; saving it once a recipe has
	// the title is refused.
	soupAgain := c.idea(map[string]any{"title": "Soup", "reason": "Still warm"})
	c.recipe(map[string]any{"title": "SOUP"})
	c.expectError(http.MethodPost, "/v1/ideas/"+soupAgain.Id.String()+"/save", nil, http.StatusConflict, "recipe_exists")

	events := c.recorder.since(&seen)
	want := "food.idea.created food.recipe.created food.idea.created food.recipe.created food.idea.saved food.recipe.created food.idea.saved food.idea.created food.idea.dismissed food.idea.created food.recipe.created"
	if got := eventTypes(events); got != want {
		t.Fatalf("events = %s", got)
	}
	if events[0].Summary != "Recipe idea “Air fryer wings”: Uses the Hähnchen that expires tomorrow, in the air fryer" ||
		events[4].Summary != "Saved the idea “Air fryer wings” as a recipe" || events[8].Summary != "Dismissed the idea “Soup”" {
		t.Fatalf("summaries = %q %q %q", events[0].Summary, events[4].Summary, events[8].Summary)
	}
	c.expectError(http.MethodGet, "/v1/ideas/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10", nil, http.StatusNotFound, "not_found")
	c.expectError(http.MethodPost, "/v1/ideas/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10/save", nil, http.StatusNotFound, "not_found")
	c.expectError(http.MethodGet, "/v1/ideas?state=old", nil, http.StatusBadRequest, "invalid_request")
}
