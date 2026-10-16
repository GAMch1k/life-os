package processor

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// A recipe link is saved through recipe_import (MCP, then the recipe-import
// integration, which writes the recipe to Food). Codex names the link, the
// caption that came with it, a title the owner gave, and whether the owner
// asked to save the link itself; recipe-import reads the page.

const (
	// maxRecipeLink, maxRecipeCaption, and maxRecipeTitle are
	// recipe-import's and Food's limits.
	maxRecipeLink    = 2000
	maxRecipeCaption = 20000
	maxRecipeTitle   = 200
)

// validRecipeImport checks a planned recipe_import: an http or https link
// with a host, and optionally a caption, a non-blank title, and saveLink.
func validRecipeImport(arguments map[string]any) bool {
	link, _ := arguments["url"].(string)
	parsed, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || utf8.RuneCountInString(link) > maxRecipeLink {
		return false
	}
	if value, present := arguments["caption"]; present && value != nil {
		caption, ok := value.(string)
		if !ok || utf8.RuneCountInString(caption) > maxRecipeCaption {
			return false
		}
	}
	if value, present := arguments["title"]; present && value != nil {
		title, ok := value.(string)
		if !ok || strings.TrimSpace(title) == "" || utf8.RuneCountInString(title) > maxRecipeTitle {
			return false
		}
	}
	if value, present := arguments["saveLink"]; present && value != nil {
		if _, ok := value.(bool); !ok {
			return false
		}
	}
	return true
}

// linkInCapture reports whether a planned recipe_import's link, or a
// recipe draft's sourceUrl, is one the capture holds (Codex may add the
// scheme a link lacked), so a link Codex made up is never fetched or saved.
// A draft without a link needs none.
func linkInCapture(item inboxItem, action plannedAction) bool {
	field := "url"
	if action.Tool == "recipe_save_draft" {
		field = "sourceUrl"
		if strings.TrimSpace(text(action.Arguments[field])) == "" {
			return true
		}
	}
	link := strings.TrimSpace(text(action.Arguments[field]))
	for _, scheme := range []string{"https://", "http://"} {
		link = strings.TrimPrefix(link, scheme)
	}
	return link != "" && strings.Contains(item.Content, link)
}

// isRecipeSave reports whether a tool saves a recipe: from a link
// (recipe_import) or as Codex built it (recipe_save_draft).
func isRecipeSave(tool string) bool {
	return tool == "recipe_import" || tool == "recipe_save_draft"
}

// importOutcome is whether recipe_import or recipe_save_draft saved a
// recipe (or found it saved already) and, when not, the reason, such as
// "Instagram posts can't be read directly: paste the caption…".
func importOutcome(tool string, result json.RawMessage) (reason string, saved bool) {
	var outcome struct {
		Saved  *bool  `json:"saved"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(toolData(result), &outcome) != nil || outcome.Saved == nil || *outcome.Saved {
		return "", true
	}
	switch {
	case strings.TrimSpace(outcome.Reason) != "":
		return outcome.Reason, false
	case tool == "recipe_save_draft":
		return "The recipe could not be saved.", false
	}
	return "The link gave no recipe to save.", false
}

// A recipe the owner typed, dictated, or photographed is saved as Codex
// built it (recipe_save_draft): title, servings, ingredients with amounts,
// steps, tags, and the link it came from or a note, when there are any.

const (
	// maxDraftIngredients, maxDraftName, maxDraftAmount, maxDraftSteps, and
	// maxDraftStep are Food's and recipe-import's limits; maxDraftTags
	// keeps the AI's tags few (to-review is added).
	maxDraftIngredients = 100
	maxDraftName        = 200
	maxDraftAmount      = 50
	maxDraftSteps       = 200
	maxDraftStep        = 2000
	maxDraftTags        = 10
	maxDraftTag         = 40
	maxDraftNote        = 1000
)

// validRecipeDraft checks a planned recipe_save_draft: a non-blank title;
// ingredients (each a non-blank name and an optional amount) or steps, at
// least one of them; and optionally servings (a whole number from 1 to
// 100), a few tags, an http or https sourceUrl, and a note, all within
// Food's limits.
func validRecipeDraft(arguments map[string]any) bool {
	title, ok := arguments["title"].(string)
	if !ok || strings.TrimSpace(title) == "" || utf8.RuneCountInString(title) > maxRecipeTitle {
		return false
	}
	if value, present := arguments["servings"]; present && value != nil {
		servings, ok := value.(float64)
		if !ok || servings != float64(int(servings)) || servings < 1 || servings > 100 {
			return false
		}
	}
	cooked := false
	if value, present := arguments["ingredients"]; present && value != nil {
		ingredients, ok := value.([]any)
		if !ok || len(ingredients) > maxDraftIngredients {
			return false
		}
		for _, raw := range ingredients {
			ingredient, ok := raw.(map[string]any)
			if !ok {
				return false
			}
			for field, value := range ingredient {
				if field != "name" && field != "amount" {
					return false
				}
				if _, isText := value.(string); value != nil && !isText {
					return false
				}
			}
			name, _ := ingredient["name"].(string)
			amount, _ := ingredient["amount"].(string)
			if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > maxDraftName || utf8.RuneCountInString(amount) > maxDraftAmount {
				return false
			}
			cooked = true
		}
	}
	if value, present := arguments["steps"]; present && value != nil {
		if !validTexts(value, maxDraftSteps, maxDraftStep) {
			return false
		}
		for _, step := range value.([]any) {
			cooked = cooked || strings.TrimSpace(step.(string)) != ""
		}
	}
	if !cooked {
		return false
	}
	if value, present := arguments["tags"]; present && value != nil && !validTexts(value, maxDraftTags, maxDraftTag) {
		return false
	}
	if value, present := arguments["sourceUrl"]; present && value != nil {
		link, ok := value.(string)
		parsed, err := url.Parse(strings.TrimSpace(link))
		if !ok || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || utf8.RuneCountInString(link) > maxRecipeLink {
			return false
		}
	}
	if value, present := arguments["note"]; present && value != nil {
		note, ok := value.(string)
		if !ok || utf8.RuneCountInString(note) > maxDraftNote {
			return false
		}
	}
	return true
}

// validTexts checks a list of at most count texts of at most limit
// characters each.
func validTexts(value any, count, limit int) bool {
	list, ok := value.([]any)
	if !ok || len(list) > count {
		return false
	}
	for _, raw := range list {
		entry, ok := raw.(string)
		if !ok || utf8.RuneCountInString(entry) > limit {
			return false
		}
	}
	return true
}

// draftIdempotencyKey is the key of a capture's numberth recipe draft (0
// for its first). Codex may write the same recipe a little differently
// when a capture is planned again, so the key leaves the draft out: a
// retried capture's draft finds the recipe the first one saved.
func draftIdempotencyKey(item inboxItem, number int) string {
	raw, _ := json.Marshal(map[string]any{"operation": idempotencyKey(item), "tool": "recipe_save_draft", "draft": number})
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("food-%x", sum[:16])
}

// draftNumber is how many recipe drafts a plan has before the action at
// index.
func draftNumber(actions []plannedAction, index int) int {
	number := 0
	for _, action := range actions[:index] {
		if action.Tool == "recipe_save_draft" {
			number++
		}
	}
	return number
}
