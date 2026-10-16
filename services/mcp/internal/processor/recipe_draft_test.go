package processor

import (
	"strings"
	"testing"
)

// crispyChickenPlan is how Codex plans a dictated recipe: the draft it
// built, never parsed by keywords.
const crispyChickenPlan = `{"actions":[{"tool":"recipe_save_draft","arguments":{"title":"Crispy chicken","servings":2,"ingredients":[{"name":"chicken thighs","amount":"4"},{"name":"flour","amount":"100 g"},{"name":"paprika","amount":"1 tsp"},{"name":"salt"}],"steps":["Coat the chicken in the flour with the paprika and salt.","Air fry at 200 °C for 20 minutes, turning once."],"tags":["chicken","air fryer"]}}],"confidence":0.92}`

const crispyChickenCapture = "Recipe for crispy chicken: 4 chicken thighs, 100 grams of flour, a teaspoon of paprika and salt. Coat the chicken in the flour with the paprika and salt, then air fry it at 200 degrees for 20 minutes, turning once."

func TestValidActionChecksRecipeDrafts(t *testing.T) {
	draft := func(change func(map[string]any)) map[string]any {
		arguments := map[string]any{"title": "Crispy chicken", "ingredients": []any{map[string]any{"name": "chicken thighs", "amount": "4"}}, "steps": []any{"Air fry."}}
		if change != nil {
			change(arguments)
		}
		return arguments
	}
	for _, tc := range []struct {
		name      string
		arguments map[string]any
		valid     bool
	}{
		{"a recipe", draft(nil), true},
		{"with everything", draft(func(a map[string]any) {
			a["servings"], a["tags"], a["sourceUrl"], a["note"] = 2.0, []any{"chicken"}, "https://www.instagram.com/reel/DGx7abc/", "From Anna."
		}), true},
		{"steps only", draft(func(a map[string]any) { delete(a, "ingredients") }), true},
		{"ingredients only", draft(func(a map[string]any) { a["steps"] = nil }), true},
		{"an ingredient without an amount", draft(func(a map[string]any) { a["ingredients"] = []any{map[string]any{"name": "salt", "amount": nil}} }), true},
		{"no title", draft(func(a map[string]any) { delete(a, "title") }), false},
		{"a blank title", draft(func(a map[string]any) { a["title"] = " " }), false},
		{"a long title", draft(func(a map[string]any) { a["title"] = strings.Repeat("a", 201) }), false},
		{"nothing to cook", draft(func(a map[string]any) { a["ingredients"], a["steps"] = []any{}, []any{" "} }), false},
		{"a nameless ingredient", draft(func(a map[string]any) { a["ingredients"] = []any{map[string]any{"amount": "4"}} }), false},
		{"an ingredient's note", draft(func(a map[string]any) { a["ingredients"] = []any{map[string]any{"name": "salt", "note": "fine"}} }), false},
		{"an amount as a number", draft(func(a map[string]any) { a["ingredients"] = []any{map[string]any{"name": "eggs", "amount": 4.0}} }), false},
		{"a long amount", draft(func(a map[string]any) {
			a["ingredients"] = []any{map[string]any{"name": "salt", "amount": strings.Repeat("1", 51)}}
		}), false},
		{"steps as text", draft(func(a map[string]any) { a["steps"] = "Air fry." }), false},
		{"half a serving", draft(func(a map[string]any) { a["servings"] = 1.5 }), false},
		{"no servings", draft(func(a map[string]any) { a["servings"] = 0.0 }), false},
		{"too many tags", draft(func(a map[string]any) { a["tags"] = []any{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} }), false},
		{"a link of no page", draft(func(a map[string]any) { a["sourceUrl"] = "www.instagram.com/reel/x" }), false},
		{"a long note", draft(func(a map[string]any) { a["note"] = strings.Repeat("n", 1001) }), false},
		{"a field of its own", draft(func(a map[string]any) { a["methods"] = []any{"air_fryer"} }), false},
	} {
		if got := validAction(plannedAction{Tool: "recipe_save_draft", Arguments: tc.arguments}); got != tc.valid {
			t.Errorf("%s: validAction = %v, want %v", tc.name, got, tc.valid)
		}
	}
}

func TestADictatedRecipeIsSavedAsCodexBuiltIt(t *testing.T) {
	tools := newFoodTools(crispyChickenCapture)
	tools.run(t, crispyChickenPlan)
	audit := tools.processed(t)
	input := tools.inputs["recipe_save_draft"][0]
	if audit[0]["tool"] != "recipe_save_draft" || input["title"] != "Crispy chicken" || len(input["ingredients"].([]any)) != 4 || len(input["steps"].([]any)) != 2 || input["sourceUrl"] != nil {
		t.Fatalf("audit = %#v, input = %#v", audit, input)
	}
	key, _ := input["idempotencyKey"].(string)
	if !strings.HasPrefix(key, "food-") || key != draftIdempotencyKey(inboxItem{ID: "item-1", Content: crispyChickenCapture}, 0) {
		t.Fatalf("key = %q", key)
	}
	// Planned again (a retry), Codex writes the recipe a little differently:
	// the same key, so Food answers with the recipe saved the first time.
	again := newFoodTools(crispyChickenCapture)
	again.run(t, strings.Replace(crispyChickenPlan, `"Crispy chicken"`, `"Crispy air fryer chicken"`, 1))
	if again.inputs["recipe_save_draft"][0]["idempotencyKey"] != key {
		t.Fatal("a re-planned draft got another key")
	}
	// Another capture, or a second draft in the plan, has another.
	if draftIdempotencyKey(inboxItem{ID: "item-2", Content: crispyChickenCapture}, 0) == key || draftIdempotencyKey(inboxItem{ID: "item-1", Content: crispyChickenCapture}, 1) == key {
		t.Fatal("draft keys are shared")
	}
	two := newFoodTools(crispyChickenCapture)
	two.run(t, strings.Replace(crispyChickenPlan, `}],"confidence"`, `},{"tool":"recipe_save_draft","arguments":{"title":"Garlic dip","ingredients":[{"name":"yoghurt"}]}}],"confidence"`, 1))
	if inputs := two.inputs["recipe_save_draft"]; len(inputs) != 2 || inputs[0]["idempotencyKey"] == inputs[1]["idempotencyKey"] {
		t.Fatalf("inputs = %#v", inputs)
	}
}

func TestADraftNotSavedGoesToHumanReview(t *testing.T) {
	tools := newFoodTools(crispyChickenCapture)
	tools.drafted = `{"saved":false,"reason":"Recipes called “Crispy chicken” and “Crispy chicken (2)” to “Crispy chicken (9)” are saved already: rename one, then capture this again."}`
	tools.run(t, crispyChickenPlan)
	if tools.review == nil || tools.review["errorSummary"] != "Recipes called “Crispy chicken” and “Crispy chicken (2)” to “Crispy chicken (9)” are saved already: rename one, then capture this again." || contains(tools.calls, "inbox_resolve") {
		t.Fatalf("review = %#v, calls = %#v", tools.review, tools.calls)
	}
	// Without a reason, a plain one.
	plain := newFoodTools(crispyChickenCapture)
	plain.drafted = `{"saved":false}`
	plain.run(t, crispyChickenPlan)
	if plain.review == nil || plain.review["errorSummary"] != "The recipe could not be saved." {
		t.Fatalf("review = %#v", plain.review)
	}
	// Found saved already: done.
	done := newFoodTools(crispyChickenCapture)
	done.drafted = `{"saved":true,"alreadySaved":true,"recipeId":"recipe-10"}`
	done.run(t, crispyChickenPlan)
	done.processed(t)
}

func TestADraftsLinkMustBeInTheCapture(t *testing.T) {
	withLink := strings.Replace(crispyChickenPlan, `"tags":`, `"sourceUrl":"https://www.instagram.com/reel/DGx7abc/","tags":`, 1)
	made := newFoodTools(crispyChickenCapture)
	made.run(t, withLink)
	if contains(made.calls, "recipe_save_draft") || made.records[len(made.records)-1]["status"] != "retryable" {
		t.Fatalf("calls = %#v, records = %#v", made.calls, made.records)
	}
	held := newFoodTools(crispyChickenCapture + " www.instagram.com/reel/DGx7abc/")
	held.run(t, withLink)
	if held.processed(t); held.inputs["recipe_save_draft"][0]["sourceUrl"] != "https://www.instagram.com/reel/DGx7abc/" {
		t.Fatalf("input = %#v", held.inputs["recipe_save_draft"][0])
	}
}
