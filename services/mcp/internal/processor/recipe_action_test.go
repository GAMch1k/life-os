package processor

import (
	"strings"
	"testing"
)

// A recipe link is imported by its link, with the caption that came with
// it, a title the owner gave, and whether they asked to save the link.
func TestValidActionChecksRecipeImports(t *testing.T) {
	for _, tc := range []struct {
		name      string
		arguments map[string]any
		valid     bool
	}{
		{"a link", map[string]any{"url": "https://www.chefkoch.de/rezepte/123/gulasch.html"}, true},
		{"an http link", map[string]any{"url": "http://ics-fixture/gulasch.html", "saveLink": true}, true},
		{"a post and its caption", map[string]any{"url": "https://www.instagram.com/reel/DGx7abc/", "caption": "Shakshuka\nZutaten: 4 Eier", "saveLink": false}, true},
		{"a named recipe", map[string]any{"url": "https://www.chefkoch.de/rezepte/1", "title": "Omas Gulasch", "caption": nil}, true},
		{"no link", map[string]any{"caption": "Shakshuka"}, false},
		{"no scheme", map[string]any{"url": "www.chefkoch.de/rezepte/1"}, false},
		{"another scheme", map[string]any{"url": "ftp://example.com/recipe"}, false},
		{"no host", map[string]any{"url": "https:///recipe"}, false},
		{"a long link", map[string]any{"url": "https://example.com/" + strings.Repeat("a", 2000)}, false},
		{"a blank title", map[string]any{"url": "https://www.chefkoch.de/rezepte/1", "title": " "}, false},
		{"saveLink as text", map[string]any{"url": "https://www.chefkoch.de/rezepte/1", "saveLink": "yes"}, false},
		{"a caption as a list", map[string]any{"url": "https://www.chefkoch.de/rezepte/1", "caption": []any{"Eier"}}, false},
		{"overrides from the plan", map[string]any{"url": "https://www.chefkoch.de/rezepte/1", "overrides": map[string]any{"title": "x"}}, false},
	} {
		if got := validAction(plannedAction{Tool: "recipe_import", Arguments: tc.arguments}); got != tc.valid {
			t.Errorf("%s: validAction = %v, want %v", tc.name, got, tc.valid)
		}
	}
}

const chefkochPlan = `{"actions":[{"tool":"recipe_import","arguments":{"url":"https://www.chefkoch.de/rezepte/123/gulasch.html","saveLink":true}}],"confidence":0.95}`

func TestARecipeLinkIsImportedWithADeterministicKey(t *testing.T) {
	key := func(plan string) string {
		tools := newFoodTools("Save this recipe: https://www.chefkoch.de/rezepte/123/gulasch.html")
		tools.run(t, plan)
		tools.processed(t)
		input := tools.inputs["recipe_import"][0]
		if input["url"] != "https://www.chefkoch.de/rezepte/123/gulasch.html" || input["saveLink"] != true || input["sourceRef"] != nil {
			t.Fatalf("recipe_import input = %#v", input)
		}
		return input["idempotencyKey"].(string)
	}
	first := key(chefkochPlan)
	if !strings.HasPrefix(first, "food-") || len(first) > 150 || key(chefkochPlan) != first {
		t.Fatalf("keys %q", first)
	}
	if key(strings.Replace(chefkochPlan, `"saveLink":true`, `"saveLink":true,"title":"Omas Gulasch"`, 1)) == first {
		t.Fatal("another import reused the key")
	}
}

func TestARecipeAlreadySavedIsDone(t *testing.T) {
	tools := newFoodTools("Save this recipe: https://www.chefkoch.de/rezepte/123/gulasch.html")
	tools.imported = `{"saved":true,"alreadySaved":true,"method":"structured","recipeId":"recipe-9"}`
	tools.run(t, chefkochPlan)
	if audit := tools.processed(t); audit[0]["tool"] != "recipe_import" || tools.review != nil {
		t.Fatalf("audit = %#v, review = %#v", audit, tools.review)
	}
}

func TestALinkWithNothingToSaveGoesToHumanReview(t *testing.T) {
	// A bare Instagram link shared from the phone, without the caption.
	tools := newFoodTools("Anna on Instagram\nhttps://www.instagram.com/reel/DGx7abc/")
	tools.imported = `{"saved":false,"method":"preview","reason":"Instagram posts can't be read directly: paste the caption to fill in the ingredients and steps."}`
	tools.run(t, `{"actions":[{"tool":"recipe_import","arguments":{"url":"https://www.instagram.com/reel/DGx7abc/"}},{"tool":"task_create","arguments":{"title":"Call Anna"}}],"confidence":0.9}`)
	if tools.review == nil || tools.review["errorSummary"] != "Instagram posts can't be read directly: paste the caption to fill in the ingredients and steps." || len(tools.review["candidates"].([]map[string]any)) != 0 {
		t.Fatalf("review = %#v", tools.review)
	}
	// As in any mixed capture, what follows does not run.
	if contains(tools.calls, "task_create") || contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v", tools.calls)
	}
	if proposed := tools.review["proposedAction"].(plannedAction); proposed.Tool != "recipe_import" {
		t.Fatalf("proposed = %#v", proposed)
	}
	// Without a reason, a plain one.
	none := newFoodTools("https://example.com/x")
	none.imported = `{"saved":false,"method":"failed"}`
	none.run(t, `{"actions":[{"tool":"recipe_import","arguments":{"url":"https://example.com/x"}}],"confidence":0.9}`)
	if none.review == nil || none.review["errorSummary"] != "The link gave no recipe to save." {
		t.Fatalf("review = %#v", none.review)
	}
}

func TestALinkTheCaptureDoesNotHoldIsNeverFetched(t *testing.T) {
	tools := newFoodTools("Save the gulasch recipe from Chefkoch")
	tools.run(t, chefkochPlan)
	if contains(tools.calls, "recipe_import") || tools.records[len(tools.records)-1]["status"] != "retryable" {
		t.Fatalf("calls = %#v, records = %#v", tools.calls, tools.records)
	}
	// A scheme Codex added is fine.
	added := newFoodTools("сохрани рецепт www.chefkoch.de/rezepte/123/gulasch.html")
	added.run(t, chefkochPlan)
	if !contains(added.calls, "recipe_import") {
		t.Fatalf("calls = %#v", added.calls)
	}
}

func TestPlanPromptImportsRecipeLinks(t *testing.T) {
	for _, want := range []string{
		"|food_add_missing_ingredients|recipe_import",
		`in any language (\"save this recipe https://www.chefkoch.de/rezepte/…\", \"Rezept speichern: https://…\", \"сохрани рецепт https://…\"), is recipe_import, never task_create, calendar_create, or food_add_shopping_items`,
		"a bare instagram.com, tiktok.com, youtube.com, or youtu.be link, such as one shared from the phone",
		`recipe_import may only use url (the http or https link as written; add https:// when it has no scheme), caption`,
		`saveLink (true only when the owner explicitly asks to save it`,
		`(sourceMetadata.via \"share-target\") is the shared title, text, and link, one per line`,
		`A link clearly about something else (a news article, a shop's product page) follows the other rules`,
		`with reason \"What should I do with this link?\"`,
	} {
		want = strings.ReplaceAll(want, `\"`, `"`)
		if !strings.Contains(planPrompt, want) {
			t.Errorf("planPrompt lacks %q", want)
		}
	}
}
