package processor

import (
	"encoding/json"
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

// linkInCapture reports whether a planned recipe_import's link is one the
// capture holds (Codex may add the scheme a link lacked), so a link Codex
// made up is never fetched.
func linkInCapture(item inboxItem, action plannedAction) bool {
	link := strings.TrimSpace(text(action.Arguments["url"]))
	for _, scheme := range []string{"https://", "http://"} {
		link = strings.TrimPrefix(link, scheme)
	}
	return link != "" && strings.Contains(item.Content, link)
}

// importOutcome is whether recipe_import saved a recipe (or found it saved
// already) and, when not, recipe-import's reason, such as "Instagram posts
// can't be read directly: paste the caption…".
func importOutcome(result json.RawMessage) (reason string, saved bool) {
	var outcome struct {
		Saved  *bool  `json:"saved"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(toolData(result), &outcome) != nil || outcome.Saved == nil || *outcome.Saved {
		return "", true
	}
	if strings.TrimSpace(outcome.Reason) == "" {
		return "The link gave no recipe to save.", false
	}
	return outcome.Reason, false
}
