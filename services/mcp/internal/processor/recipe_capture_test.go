package processor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// recipeCaptures plan recipe captures the way the plan prompt asks (P15-12),
// from the item's text, or from its photo: an explicit Task wins over a
// link, a dish named with a link saves it under those words, a bare post
// nobody can read goes to the owner, and a recipe without a link (typed,
// dictated, or photographed) is the draft Codex builds.
var recipeCaptures = map[string]string{
	"test stuff, send it to tasks for today https://www.instagram.com/reel/DPtest01/": `{"actions":[{"tool":"task_create","arguments":{"title":"Test stuff","notes":"https://www.instagram.com/reel/DPtest01/","dueAt":"2026-10-16T00:00:00+02:00"}}],"confidence":0.9}`,
	"Remind me to cook this on Friday https://www.tiktok.com/@cook/video/123":         `{"actions":[{"tool":"task_create","arguments":{"title":"Cook this","notes":"https://www.tiktok.com/@cook/video/123","dueAt":"2026-10-16T00:00:00+02:00"}}],"confidence":0.9}`,
	"брауни https://www.instagram.com/reel/DPbrownie/":                                `{"actions":[{"tool":"recipe_import","arguments":{"url":"https://www.instagram.com/reel/DPbrownie/","title":"Брауни","saveLink":true}}],"confidence":0.9}`,
	"https://www.instagram.com/reel/DPbare/":                                          `{"actions":[],"ambiguous":true,"confidence":0.9,"reason":"This post can't be read: say what the recipe is called, or paste its caption."}`,
	crispyChickenCapture:                                                              crispyChickenPlan,
	"Сохрани рецепт сырников: 500 г творога, 2 яйца, 3 ст. л. муки. Смешать, сформировать сырники и обжарить с двух сторон.": `{"actions":[{"tool":"recipe_save_draft","arguments":{"title":"Сырники","ingredients":[{"name":"творог","amount":"500 г"},{"name":"яйца","amount":"2"},{"name":"мука","amount":"3 ст. л."}],"steps":["Смешать.","Сформировать сырники.","Обжарить с двух сторон."],"tags":["завтрак"]}}],"confidence":0.93}`,
	"Save the lasagne recipe": `{"actions":[],"ambiguous":true,"confidence":0.9,"reason":"Paste or dictate the recipe, or share its link."}`,
}

// recipePhoto is a recipe card's photo; letterPhoto a letter's, which keeps
// its rule.
var (
	recipePhoto = "inbox-media:" + strings.Repeat("6", 64)
	letterPhoto = documentRef("letter-finanzamt")
)

type recipeCapturePlanner struct{}

func (recipeCapturePlanner) Plan(ctx context.Context, input string) (string, error) {
	var item struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal([]byte(input), &item)
	if images := imagesFrom(ctx); len(images.Images) == 1 {
		if string(images.Images[0].Data) == "recipe-card" {
			// Transcribed as printed, in its language.
			return `{"actions":[{"tool":"recipe_save_draft","arguments":{"title":"Omas Apfelkuchen","servings":12,"ingredients":[{"name":"Mehl","amount":"300 g"},{"name":"Butter","amount":"200 g"},{"name":"Zucker","amount":"150 g"},{"name":"Äpfel","amount":"1 kg"}],"steps":["Den Ofen auf 180 °C vorheizen.","Mehl, Butter und Zucker verkneten.","Äpfel schälen und auf den Teig legen.","45 Minuten backen."],"tags":["Kuchen"]}}],"confidence":0.9}`, nil
		}
		return documentPlanner{}.Plan(ctx, input)
	}
	if plan, ok := recipeCaptures[item.Content]; ok {
		return plan, nil
	}
	return `{"actions":[],"ambiguous":true,"confidence":0.1,"reason":"clarification required"}`, nil
}

type recipeMedia struct{}

func (recipeMedia) Fetch(ctx context.Context, references []string) (CaptureImages, error) {
	if len(references) == 1 && references[0] == recipePhoto {
		return CaptureImages{Images: []Image{{ContentType: "image/jpeg", Data: []byte("recipe-card")}}}, nil
	}
	return documentMedia{}.Fetch(ctx, references)
}

func TestRecipeCapturesRouteByTheOwnersWords(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		images        []string
		tool, review  string
	}{
		{"an explicit Task wins over a reel", "test stuff, send it to tasks for today https://www.instagram.com/reel/DPtest01/", nil, "task_create", ""},
		{"a reminder to cook a post", "Remind me to cook this on Friday https://www.tiktok.com/@cook/video/123", nil, "task_create", ""},
		{"a dish named with a reel", "брауни https://www.instagram.com/reel/DPbrownie/", nil, "recipe_import", ""},
		{"a bare reel without words", "https://www.instagram.com/reel/DPbare/", nil, "", "This post can't be read: say what the recipe is called, or paste its caption."},
		{"a dictated recipe", crispyChickenCapture, nil, "recipe_save_draft", ""},
		{"a typed Russian recipe", "Сохрани рецепт сырников: 500 г творога, 2 яйца, 3 ст. л. муки. Смешать, сформировать сырники и обжарить с двух сторон.", nil, "recipe_save_draft", ""},
		{"a recipe asked for but not given", "Save the lasagne recipe", nil, "", "Paste or dictate the recipe, or share its link."},
		{"a recipe photo", "Photo", []string{recipePhoto}, "recipe_save_draft", ""},
		{"a letter keeps its rule", "Photo", []string{letterPhoto}, "task_create", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := &corpusTools{item: inboxItem{ID: "item-" + tc.name, Content: tc.content, Source: "web", MediaReferences: tc.images, CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)}}
			if _, err := (Runner{Planner: recipeCapturePlanner{}, Tools: tools, Model: "gpt-5.6-luna", Media: recipeMedia{}}).Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, other := range []string{"task_create", "recipe_import", "recipe_save_draft", "food_add_shopping_items", "food_plan_meal"} {
				if (other == tc.tool) != contains(tools.calls, other) {
					t.Fatalf("%s called: %v; calls = %#v", other, contains(tools.calls, other), tools.calls)
				}
			}
			if tc.review != "" {
				if contains(tools.calls, "inbox_resolve") || tools.inputs["inbox_set_human_review"]["errorSummary"] != tc.review {
					t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.inputs["inbox_set_human_review"])
				}
				return
			}
			if !contains(tools.calls, "inbox_resolve") || tools.records[len(tools.records)-1]["status"] != "processed" {
				t.Fatalf("calls = %#v, records = %#v", tools.calls, tools.records)
			}
			input := tools.inputs[tc.tool]
			switch tc.name {
			case "an explicit Task wins over a reel":
				// The link goes into the Task's notes.
				if input["title"] != "Test stuff" || input["notes"] != "https://www.instagram.com/reel/DPtest01/" || !strings.HasPrefix(input["idempotencyKey"].(string), "action-") {
					t.Fatalf("task_create input = %#v", input)
				}
			case "a dish named with a reel":
				if input["title"] != "Брауни" || input["saveLink"] != true || input["url"] != "https://www.instagram.com/reel/DPbrownie/" {
					t.Fatalf("recipe_import input = %#v", input)
				}
			case "a recipe photo":
				ingredients, _ := input["ingredients"].([]any)
				if input["title"] != "Omas Apfelkuchen" || len(ingredients) != 4 || len(input["steps"].([]any)) != 4 || input["sourceUrl"] != nil || !strings.HasPrefix(input["idempotencyKey"].(string), "food-") {
					t.Fatalf("recipe_save_draft input = %#v", input)
				}
			case "a dictated recipe", "a typed Russian recipe":
				if input["sourceUrl"] != nil || !strings.HasPrefix(input["idempotencyKey"].(string), "food-") {
					t.Fatalf("recipe_save_draft input = %#v", input)
				}
			}
		})
	}
}

func TestPlanPromptRoutesRecipesByTheOwnersWords(t *testing.T) {
	for _, want := range []string{
		"|recipe_import|recipe_save_draft|",
		// An explicit Task wins over the recipe-link rules.
		`An item that explicitly asks for a Task or a reminder (\"send it to tasks\", \"add a task\", \"remind me to cook this\", \"в задачи\", \"напомни\", \"als Aufgabe\", \"erinnere mich\") is task_create also when its link is a recipe or a post`,
		"so there is no recipe_import for that link",
		"the Task's title is the owner's words about it",
		"its notes hold the link as written",
		// A dish named with a link saves it.
		`A dish or recipe the owner names alongside a post or another link that is not a recipe site's page`,
		"recipe_import with that name as title and saveLink true, also for a post the server cannot read, which is then kept as a stub to review under the owner's words",
		"(unless the item also explicitly asks to save the recipe: then both)",
		`with reason \"This post can't be read: say what the recipe is called, or paste its caption.\"`,
		`never take a title from the site's name (\"Instagram\", \"TikTok\", \"YouTube\") or from a phone share's title line`,
		`\"брауни <link>\" is \"Брауни\"`,
		// Recipes without a link: Codex builds the draft.
		"Recipes without a link.",
		"is recipe_save_draft, never task_create, food_add_shopping_items, or food_plan_meal: you build the recipe from the words",
		"recipe_save_draft may only use title",
		"Use only what the item gives: never add ingredients, amounts, or steps of your own.",
		"A recipe pasted with its link (a post's caption) stays recipe_import with that caption.",
		`with reason \"Paste or dictate the recipe, or share its link.\"`,
		// A recipe photo is transcribed; letters and receipts keep their rule.
		"Photos of recipes. A photo of a recipe",
		"here as the one exception to never copying a document's text: transcribe the recipe into the draft",
		"Letters and receipts keep their rules above.",
	} {
		want = strings.ReplaceAll(want, `\"`, `"`)
		if !strings.Contains(planPrompt, want) {
			t.Errorf("planPrompt lacks %q", want)
		}
	}
}
