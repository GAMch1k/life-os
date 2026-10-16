package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/mcp/internal/coreclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A recipe the owner typed, dictated, or photographed has no page to read:
// Codex builds the draft (title, ingredients, steps) and recipe_save_draft
// saves it in Food, tagged to-review for the owner to look over. It is the
// AI writing a recipe of the owner's to the owning service, as the owner's
// own recipe form does, not an import: recipe-import stays the integration
// for links and other outside sources.

// reviewTag marks a recipe the owner should look over.
const reviewTag = "to-review"

// Food's recipe limits (its CreateRecipeRequest).
const (
	maxDraftTitle       = 200
	maxDraftIngredients = 100
	maxDraftName        = 200
	maxDraftAmount      = 50
	maxDraftSteps       = 20000
	maxDraftTags        = 20
	maxDraftTag         = 40
	maxDraftNote        = 1000
	maxDraftLink        = 2000
	maxFoodKey          = 200
)

// recipeDraft is a recipe Codex built from a capture's text or photo.
type recipeDraft struct {
	IdempotencyKey string            `json:"idempotencyKey,omitempty" jsonschema:"Makes the save safe to retry: a retried capture saves the recipe once"`
	Title          string            `json:"title" jsonschema:"The recipe's name, as the owner or the recipe calls it"`
	Servings       *int              `json:"servings,omitempty" jsonschema:"For how many, 1 to 100, only when stated"`
	Ingredients    []draftIngredient `json:"ingredients,omitempty" jsonschema:"Each ingredient with its amount, in order"`
	Steps          []string          `json:"steps,omitempty" jsonschema:"The steps, one per item, in order"`
	Tags           []string          `json:"tags,omitempty" jsonschema:"A few tags; to-review is always added"`
	SourceURL      string            `json:"sourceUrl,omitempty" jsonschema:"The http or https link the recipe came from, when the capture holds one"`
	Note           string            `json:"note,omitempty" jsonschema:"A short note, such as where the recipe is from"`
}

// draftIngredient is one ingredient of a draft.
type draftIngredient struct {
	Name   string `json:"name" jsonschema:"The ingredient, without the amount"`
	Amount string `json:"amount,omitempty" jsonschema:"How much, as written (200 g, 2 EL, 1 ст. л.)"`
}

// addRecipeDraftTools registers recipe_save_draft, which saves a draft in
// Food (POST /v1/recipes).
func addRecipeDraftTools(s *mcp.Server, hc *http.Client, baseURL string, emitter *activity.Emitter) {
	client := coreclient.Client{BaseURL: baseURL, HTTP: hc}
	mcp.AddTool(s, &mcp.Tool{Name: "recipe_save_draft", Description: "Save a recipe the owner typed, dictated, or photographed, as Codex built it (title, servings, ingredients with amounts, steps, tags, and optionally the sourceUrl it came from and a note), in Food, tagged to-review. No page is read. Answers saved, recipe, recipeId, and reason (why nothing was saved); the same link saved before is saved: true with alreadySaved, and another recipe of that name gets a numbered title (\"Crispy chicken (2)\")"}, func(ctx context.Context, req *mcp.CallToolRequest, in recipeDraft) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "recipe_save_draft", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			body, err := draftBody(in)
			if err != nil {
				return nil, Output{}, err
			}
			data, err := saveDraft(ctx, client, trace, body, strings.TrimSpace(in.IdempotencyKey))
			if err != nil {
				return nil, Output{}, err
			}
			return nil, Output{Data: data}, nil
		})
	})
}

// draftBody is the draft as Food takes it: the title capitalised; the
// ingredients and steps (at least one of them) trimmed, empty ones left
// out, the steps one per line; to-review first among the tags, tags Food
// would refuse left out; and the link and note when given.
func draftBody(in recipeDraft) (map[string]any, error) {
	title := capitalized(in.Title)
	if title == "" || utf8.RuneCountInString(title) > maxDraftTitle {
		return nil, fmt.Errorf("title must be 1 to %d characters", maxDraftTitle)
	}
	body := map[string]any{"title": title}
	if in.Servings != nil {
		if *in.Servings < 1 || *in.Servings > 100 {
			return nil, errors.New("servings must be between 1 and 100")
		}
		body["servings"] = *in.Servings
	}
	if len(in.Ingredients) > maxDraftIngredients {
		return nil, fmt.Errorf("a recipe has at most %d ingredients", maxDraftIngredients)
	}
	ingredients := make([]map[string]any, 0, len(in.Ingredients))
	for i, ingredient := range in.Ingredients {
		name, amount := oneLine(ingredient.Name), oneLine(ingredient.Amount)
		if name == "" {
			continue
		}
		if utf8.RuneCountInString(name) > maxDraftName || utf8.RuneCountInString(amount) > maxDraftAmount {
			return nil, fmt.Errorf("ingredients[%d]: a name has at most %d characters and an amount at most %d", i, maxDraftName, maxDraftAmount)
		}
		entry := map[string]any{"name": name}
		if amount != "" {
			entry["amount"] = amount
		}
		ingredients = append(ingredients, entry)
	}
	var steps []string
	for _, step := range in.Steps {
		if step = oneLine(step); step != "" {
			steps = append(steps, step)
		}
	}
	if len(ingredients) == 0 && len(steps) == 0 {
		return nil, errors.New("a recipe needs ingredients or steps")
	}
	if len(ingredients) > 0 {
		body["ingredients"] = ingredients
	}
	if joined := strings.Join(steps, "\n"); joined != "" {
		if utf8.RuneCountInString(joined) > maxDraftSteps {
			return nil, fmt.Errorf("the steps have at most %d characters", maxDraftSteps)
		}
		body["steps"] = joined
	}
	tags := []string{reviewTag}
	seen := map[string]bool{reviewTag: true}
	for _, tag := range in.Tags {
		tag = oneLine(tag)
		key := strings.ToLower(tag)
		if tag == "" || seen[key] || utf8.RuneCountInString(tag) > maxDraftTag || strings.IndexFunc(tag, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0 || len(tags) == maxDraftTags {
			continue
		}
		seen[key] = true
		tags = append(tags, tag)
	}
	body["tags"] = tags
	if link := strings.TrimSpace(in.SourceURL); link != "" {
		if _, err := recipeLink(link); err != nil || utf8.RuneCountInString(link) > maxDraftLink {
			return nil, errors.New("sourceUrl must be an http or https link")
		}
		body["sourceUrl"] = link
	}
	if note := strings.TrimSpace(in.Note); note != "" {
		if utf8.RuneCountInString(note) > maxDraftNote {
			return nil, fmt.Errorf("note has at most %d characters", maxDraftNote)
		}
		body["note"] = note
	}
	return body, nil
}

// saveDraft saves the recipe in Food. A title Food has is this recipe saved
// before only when both came from the same link; otherwise this one is
// saved as "<title> (2)", up to (9), each try with its own key, so a
// retried capture finds what it saved. A key this capture used for another
// draft (Codex wrote it differently on a retry) means it saved it already.
func saveDraft(ctx context.Context, client coreclient.Client, trace activity.Trace, body map[string]any, key string) (map[string]any, error) {
	headers := http.Header{}
	trace.Apply(headers)
	base, _ := body["title"].(string)
	link, _ := body["sourceUrl"].(string)
	for number := 1; number <= maxTitleNumber; number++ {
		body["title"] = numberedTitle(base, number)
		save := headers.Clone()
		if key != "" {
			save.Set("Idempotency-Key", numberedKey(key, number, maxFoodKey))
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		answer, err := client.DoWithHeaders(ctx, http.MethodPost, "/v1/recipes", raw, save)
		var status coreclient.StatusError
		if errors.As(err, &status) {
			switch code := errorCode(err); {
			case status.Status == http.StatusConflict && code == "recipe_exists":
				if link == "" {
					continue
				}
				existing, findErr := recipeTitled(ctx, client, headers, numberedTitle(base, number))
				if findErr != nil {
					return nil, findErr
				}
				if existing != nil && sameLink(text(existing["sourceUrl"]), link) {
					return map[string]any{"saved": true, "alreadySaved": true, "recipeId": existing["id"]}, nil
				}
				continue
			case status.Status == http.StatusConflict && code == "idempotency_conflict":
				return map[string]any{"saved": true, "alreadySaved": true}, nil
			case status.Status == http.StatusBadRequest:
				return map[string]any{"saved": false, "reason": importError(status.Body).Message}, nil
			}
		}
		if err != nil {
			return nil, err
		}
		var recipe map[string]any
		if err := json.Unmarshal(answer, &recipe); err != nil {
			return nil, fmt.Errorf("decode Food's recipe: %w", err)
		}
		data := map[string]any{"saved": true, "recipe": recipe, "recipeId": recipe["id"]}
		if number > 1 {
			data["renamedFrom"] = base
		}
		return data, nil
	}
	return map[string]any{"saved": false, "reason": takenReason(base)}, nil
}

// recipeTitled is Food's recipe whose title is title as Food compares
// titles, or nil.
func recipeTitled(ctx context.Context, client coreclient.Client, headers http.Header, title string) (map[string]any, error) {
	_, out, err := read(ctx, client, headers, "/v1/recipes?"+url.Values{"q": {title}, "limit": {"50"}}.Encode())
	if err != nil {
		return nil, err
	}
	items, _ := out.Data["items"].([]any)
	for _, raw := range items {
		recipe, _ := raw.(map[string]any)
		if normalized := text(recipe["normalizedTitle"]); normalized != "" && normalized == foodNormalize(title) {
			return recipe, nil
		}
	}
	return nil, nil
}

// foodNormalize is a name as Food compares names: NFKC, lower case, "ё" as
// "е", single spaces, and no surrounding punctuation.
func foodNormalize(name string) string {
	normalized := strings.ToLower(norm.NFKC.String(name))
	normalized = strings.ReplaceAll(normalized, "ё", "е")
	normalized = strings.Join(strings.Fields(normalized), " ")
	return strings.TrimFunc(normalized, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
}

// oneLine is text on one line, with single spaces.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
