package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/mcp/internal/coreclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// recipeImport saves a recipe from a link through the recipe-import
// integration, which writes it to Food.
type recipeImport struct {
	IdempotencyKey string `json:"idempotencyKey,omitempty" jsonschema:"Passed on to Food through recipe-import, so a retried save creates the recipe once"`
	URL            string `json:"url" jsonschema:"The http or https link of the recipe page or post"`
	Caption        string `json:"caption,omitempty" jsonschema:"Text that came with the link, such as a post's caption with ingredients and steps; read when the page itself has no recipe data"`
	Title          string `json:"title,omitempty" jsonschema:"The recipe's name in the owner's words; replaces the one the page or caption gives, and a page with no recipe data is then saved as a stub with this title (tagged to-review)"`
	SaveLink       bool   `json:"saveLink,omitempty" jsonschema:"The owner asked to save the link itself: a page with no recipe data and no usable caption is then saved as a stub to review (tagged to-review)"`
}

// importPreview is recipe-import's answer to a preview or a text read.
type importPreview struct {
	Method string         `json:"method"`
	Reason *string        `json:"reason"`
	Draft  map[string]any `json:"draft"`
}

// maxTitleNumber is the highest number a recipe's title gets when other
// recipes have its name ("Брауни (2)" up to "Брауни (9)").
const maxTitleNumber = 9

// nameHint is added to the reason when a link gave nothing to save and the
// owner named no recipe.
const nameHint = "Or say what the recipe is called, to keep the link to review."

// addRecipeImportTools registers recipe_import. It uses recipe-import's
// API, the one the Dashboard's import uses; recipe-import saves the recipe
// in Food.
func addRecipeImportTools(s *mcp.Server, hc *http.Client, baseURL string, emitter *activity.Emitter) {
	client := coreclient.Client{BaseURL: baseURL, HTTP: hc}
	mcp.AddTool(s, &mcp.Tool{Name: "recipe_import", Description: "Save a recipe from a link in Food. The link is read first: a page with recipe data (structured) is saved as it is; a page without (preview, such as an Instagram, TikTok, or YouTube post) or one that cannot be read (failed) is saved from the caption pasted with the link when it holds ingredients or steps, else as a stub to review (tagged to-review) when the owner named the recipe (title) or asked to save the link (saveLink); a stub is never titled after the site. Answers saved, method, reason (why nothing was saved), recipe, and recipeId; the same link saved before is saved: true with alreadySaved, and another recipe of that name from another link gets a numbered title (\"Брауни (2)\")"}, func(ctx context.Context, req *mcp.CallToolRequest, in recipeImport) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "recipe_import", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			data, err := importRecipe(ctx, client, trace, in)
			if err != nil {
				return nil, Output{}, err
			}
			return nil, Output{Data: data}, nil
		})
	})
}

// importRecipe reads the link (preview) and saves what it gives: the page's
// recipe, the pasted caption's, or the stub the owner asked for by naming
// the recipe or the link. A link or caption that gives nothing to save
// answers saved: false with the reason.
func importRecipe(ctx context.Context, client coreclient.Client, trace activity.Trace, in recipeImport) (map[string]any, error) {
	link, err := recipeLink(in.URL)
	if err != nil {
		return nil, err
	}
	title, caption := capitalized(in.Title), strings.TrimSpace(in.Caption)
	headers := http.Header{}
	trace.Apply(headers)
	var preview importPreview
	if refused, err := importCall(ctx, client, headers, "/v1/imports/preview", map[string]any{"url": link}, &preview); err != nil || refused != "" {
		return notSaved("failed", refused), err
	}
	reason := text(preview.Reason)
	// overrides is the draft to save; nil saves the page as recipe-import
	// reads it.
	var overrides map[string]any
	switch {
	case preview.Method == "structured":
		if title != "" {
			overrides = withTitle(preview.Draft, title)
		}
	case caption != "":
		var read importPreview
		if refused, err := importCall(ctx, client, headers, "/v1/imports/text", map[string]any{"text": caption, "sourceUrl": link}, &read); err != nil || refused != "" {
			return notSaved(preview.Method, refused), err
		}
		switch draft := captionDraft(read.Draft, preview.Draft, link, title); {
		case !hasRecipe(read.Draft):
			if reason == "" {
				reason = text(read.Reason)
			}
		case text(draft["title"]) == "":
			reason = "The caption names no recipe: say what it is called."
		default:
			overrides = draft
		}
	}
	if overrides == nil && preview.Method != "structured" {
		stub := withTitle(preview.Draft, title)
		if title == "" && siteOnlyTitle(text(stub["title"]), text(stub["siteName"])) {
			// "Instagram" names the site's login wall, not the recipe.
			stub["title"] = ""
		}
		// A recipe the owner named with the link is a request to save it.
		if !(in.SaveLink || title != "") || text(stub["title"]) == "" {
			if reason == "" {
				reason = "The link has no recipe to read: paste the recipe or its caption, or ask to save the link."
			}
			if title == "" && (preview.Method == "preview" || preview.Method == "failed") {
				reason = strings.TrimSpace(reason + " " + nameHint)
			}
			return notSaved(preview.Method, reason), nil
		}
		overrides = stub
	}
	return saveImport(ctx, client, headers, link, strings.TrimSpace(in.IdempotencyKey), preview, overrides)
}

// saveImport saves the recipe (overrides, or the page read again) with
// recipe-import. A recipe of that name saved from the same link is this
// one, saved before; one from another link (or none) is another recipe,
// so this one is saved as "<title> (2)", up to (9), each try with its own
// key, so a retried capture finds each one it saved.
func saveImport(ctx context.Context, client coreclient.Client, headers http.Header, link, key string, preview importPreview, overrides map[string]any) (map[string]any, error) {
	base := text(preview.Draft["title"])
	if overrides != nil {
		base = text(overrides["title"])
	}
	for number := 1; number <= maxTitleNumber; number++ {
		body := map[string]any{"url": link}
		if number > 1 {
			if overrides == nil {
				overrides = withTitle(preview.Draft, "")
			}
			overrides["title"] = numberedTitle(base, number)
		}
		if overrides != nil {
			body["overrides"] = overrides
		}
		save := headers.Clone()
		if key != "" {
			save.Set("Idempotency-Key", numberedKey(key, number, 150))
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		answer, err := client.DoWithHeaders(ctx, http.MethodPost, "/v1/imports", raw, save)
		var status coreclient.StatusError
		if errors.As(err, &status) {
			failure := importError(status.Body)
			switch {
			case status.Status == http.StatusConflict && failure.Code == "recipe_exists" && failure.RecipeID != "":
				if sameLink(failure.SourceURL, link) {
					return map[string]any{"saved": true, "alreadySaved": true, "method": preview.Method, "recipeId": failure.RecipeID}, nil
				}
				continue
			case status.Status == http.StatusConflict && failure.Code == "idempotency_conflict":
				// This capture's key saved a recipe already, from what the
				// link gave then.
				return map[string]any{"saved": true, "alreadySaved": true, "method": preview.Method}, nil
			case status.Status == http.StatusUnprocessableEntity || status.Status == http.StatusBadRequest:
				return notSaved(preview.Method, failure.Message), nil
			}
		}
		if err != nil {
			return nil, err
		}
		var saved struct {
			Method string         `json:"method"`
			Recipe map[string]any `json:"recipe"`
		}
		if err := json.Unmarshal(answer, &saved); err != nil {
			return nil, fmt.Errorf("decode recipe-import answer: %w", err)
		}
		data := map[string]any{"saved": true, "method": saved.Method, "recipe": saved.Recipe, "recipeId": saved.Recipe["id"]}
		if number > 1 {
			data["renamedFrom"] = base
		}
		return data, nil
	}
	return notSaved(preview.Method, takenReason(base)), nil
}

// recipeLink checks that a link is http or https with a host.
func recipeLink(raw string) (string, error) {
	link := strings.TrimSpace(raw)
	parsed, err := url.Parse(link)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("url must be an http or https link")
	}
	return link, nil
}

// importCall POSTs a read (preview or text) to recipe-import and decodes
// its answer into out. A link or text recipe-import refuses (400) is not
// an error: refused is its message.
func importCall(ctx context.Context, client coreclient.Client, headers http.Header, path string, body map[string]any, out *importPreview) (refused string, err error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	answer, err := client.DoWithHeaders(ctx, http.MethodPost, path, raw, headers)
	var status coreclient.StatusError
	if errors.As(err, &status) && status.Status == http.StatusBadRequest {
		return importError(status.Body).Message, nil
	}
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(answer, out); err != nil {
		return "", fmt.Errorf("decode recipe-import answer: %w", err)
	}
	if out.Draft == nil {
		out.Draft = map[string]any{}
	}
	return "", nil
}

// importFailure is recipe-import's error answer; for recipe_exists, the
// recipe of that name and the link it was saved from.
type importFailure struct {
	Code, Message, RecipeID, SourceURL string
}

// importError reads recipe-import's error envelope.
func importError(body []byte) importFailure {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		RecipeID  string `json:"recipeId"`
		SourceURL string `json:"sourceUrl"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Error.Message == "" {
		return importFailure{Message: strings.TrimSpace(string(body))}
	}
	return importFailure{Code: envelope.Error.Code, Message: envelope.Error.Message, RecipeID: envelope.RecipeID, SourceURL: envelope.SourceURL}
}

// notSaved is the answer when nothing was saved, and why.
func notSaved(method, reason string) map[string]any {
	if strings.TrimSpace(reason) == "" {
		reason = "The link could not be read."
	}
	return map[string]any{"saved": false, "method": method, "reason": reason}
}

// hasRecipe reports whether a draft read from text holds ingredients or
// steps.
func hasRecipe(draft map[string]any) bool {
	ingredients, _ := draft["ingredients"].([]any)
	steps, _ := draft["steps"].([]any)
	return len(ingredients) > 0 || len(steps) > 0
}

// withTitle is a copy of draft with title, when given, as its title.
func withTitle(draft map[string]any, title string) map[string]any {
	out := make(map[string]any, len(draft))
	for field, value := range draft {
		out[field] = value
	}
	if title != "" {
		out["title"] = title
	}
	return out
}

// captionDraft is the draft read from a caption, completed from the page:
// its title when the caption gives none (a title the owner named wins), and
// the page's image and site. The page's description is left out: for a
// post it is the caption again.
func captionDraft(read, page map[string]any, link, title string) map[string]any {
	draft := withTitle(read, title)
	if text(draft["title"]) == "" {
		draft["title"] = page["title"]
	}
	for _, field := range []string{"imageUrl", "siteName"} {
		if text(draft[field]) == "" && text(page[field]) != "" {
			draft[field] = page[field]
		}
	}
	draft["sourceUrl"] = link
	return draft
}

// text is a JSON string, or a string pointer, or "".
func text(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case *string:
		if v != nil {
			return strings.TrimSpace(*v)
		}
	}
	return ""
}

// capitalized is a recipe name the owner wrote, trimmed, with its first
// letter in upper case when its first word is all lower case ("брауни" is
// "Брауни"; "iPad cake" stays).
func capitalized(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	first, size := utf8.DecodeRuneInString(name)
	word, _, _ := strings.Cut(name, " ")
	if size == 0 || !unicode.IsLower(first) || strings.ToLower(word) != word {
		return name
	}
	return string(unicode.ToUpper(first)) + name[size:]
}

// numberedTitle is title with its number when another recipe has the name:
// "Брауни (2)", within Food's 200 characters.
func numberedTitle(title string, number int) string {
	if number <= 1 {
		return title
	}
	suffix := fmt.Sprintf(" (%d)", number)
	runes := []rune(title)
	if limit := 200 - utf8.RuneCountInString(suffix); len(runes) > limit {
		runes = runes[:limit]
	}
	return strings.TrimSpace(string(runes)) + suffix
}

// numberedKey is the Idempotency-Key of the numberth try at a title, at
// most limit characters: the key itself first, then "<key>#<number>".
func numberedKey(key string, number, limit int) string {
	if number <= 1 {
		return key
	}
	suffix := fmt.Sprintf("#%d", number)
	if len(key)+len(suffix) > limit {
		key = key[:limit-len(suffix)]
	}
	return key + suffix
}

// takenReason is why nothing was saved when every numbered title is taken.
func takenReason(title string) string {
	return fmt.Sprintf("Recipes called “%s” and “%s” to “%s” are saved already: rename one, then capture this again.", title, numberedTitle(title, 2), numberedTitle(title, maxTitleNumber))
}

// trackingParams are link query parameters that only say how a link was
// shared, never which page it is.
var trackingParams = map[string]bool{"igsh": true, "igshid": true, "si": true, "fbclid": true, "gclid": true, "feature": true, "is_from_webapp": true, "sender_device": true, "ref": true, "share_id": true}

// sameLink reports whether two links name the same page: the same host
// (without "www." and "m."), path (without a trailing slash), and query
// apart from tracking parameters (utm_*, igsh, si, …). An empty link is
// never the same as another.
func sameLink(a, b string) bool {
	canonical := func(raw string) string {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Host == "" {
			return ""
		}
		host := strings.ToLower(parsed.Hostname())
		host = strings.TrimPrefix(strings.TrimPrefix(host, "www."), "m.")
		query := parsed.Query()
		for name := range query {
			if trackingParams[strings.ToLower(name)] || strings.HasPrefix(strings.ToLower(name), "utm_") {
				query.Del(name)
			}
		}
		return host + strings.TrimRight(parsed.EscapedPath(), "/") + "?" + query.Encode()
	}
	first := canonical(a)
	return first != "" && first == canonical(b)
}

// socialSites are the sites whose login walls give a page no other title.
var socialSites = []string{"Instagram", "TikTok", "YouTube", "Facebook", "Threads", "X", "Twitter", "Pinterest"}

// loginWords are what a login wall's title holds besides the site's name.
var loginWords = regexp.MustCompile(`(?i)\b(log ?in(to)?|sign (in|up)|make your day)\b`)

// siteOnlyTitle reports whether a title names only the site ("Instagram",
// "Login • Instagram"), not the recipe: a stub never gets it.
func siteOnlyTitle(title, site string) bool {
	names := map[string]bool{}
	for _, name := range append([]string{site}, socialSites...) {
		for _, word := range strings.FieldsFunc(strings.ToLower(name), notWordRune) {
			names[word] = true
		}
	}
	for _, word := range strings.FieldsFunc(strings.ToLower(loginWords.ReplaceAllString(title, " ")), notWordRune) {
		if !names[word] {
			return false
		}
	}
	return true
}

func notWordRune(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' }
