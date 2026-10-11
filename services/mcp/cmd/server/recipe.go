package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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
	Title          string `json:"title,omitempty" jsonschema:"The recipe's title when the owner names it; replaces the one the page or caption gives"`
	SaveLink       bool   `json:"saveLink,omitempty" jsonschema:"The owner asked to save the link itself: a page with no recipe data and no usable caption is then saved as a stub to review (tagged to-review)"`
}

// importPreview is recipe-import's answer to a preview or a text read.
type importPreview struct {
	Method string         `json:"method"`
	Reason *string        `json:"reason"`
	Draft  map[string]any `json:"draft"`
}

// addRecipeImportTools registers recipe_import. It uses recipe-import's
// API, the one the Dashboard's import uses; recipe-import saves the recipe
// in Food.
func addRecipeImportTools(s *mcp.Server, hc *http.Client, baseURL string, emitter *activity.Emitter) {
	client := coreclient.Client{BaseURL: baseURL, HTTP: hc}
	mcp.AddTool(s, &mcp.Tool{Name: "recipe_import", Description: "Save a recipe from a link in Food. The link is read first: a page with recipe data (structured) is saved as it is; a page without (preview, such as an Instagram, TikTok, or YouTube post) or one that cannot be read (failed) is saved from the caption pasted with the link when it holds ingredients or steps, else as a stub to review when saveLink says the owner asked to save the link. Answers saved, method, reason (why nothing was saved), recipe, and recipeId; a recipe of that title already saved is saved: true with alreadySaved"}, func(ctx context.Context, req *mcp.CallToolRequest, in recipeImport) (*mcp.CallToolResult, Output, error) {
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
// recipe, the pasted caption's, or the stub the owner asked for. A link or
// caption that gives nothing to save answers saved: false with the reason.
func importRecipe(ctx context.Context, client coreclient.Client, trace activity.Trace, in recipeImport) (map[string]any, error) {
	link, err := recipeLink(in.URL)
	if err != nil {
		return nil, err
	}
	title, caption := strings.TrimSpace(in.Title), strings.TrimSpace(in.Caption)
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
		if !in.SaveLink || text(stub["title"]) == "" {
			if reason == "" {
				reason = "The link has no recipe to read: paste the recipe or its caption, or ask to save the link."
			}
			return notSaved(preview.Method, reason), nil
		}
		overrides = stub
	}
	body := map[string]any{"url": link}
	if overrides != nil {
		body["overrides"] = overrides
	}
	save := headers.Clone()
	if in.IdempotencyKey != "" {
		save.Set("Idempotency-Key", in.IdempotencyKey)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	answer, err := client.DoWithHeaders(ctx, http.MethodPost, "/v1/imports", raw, save)
	var status coreclient.StatusError
	if errors.As(err, &status) {
		code, message, recipeID := importError(status.Body)
		switch {
		case status.Status == http.StatusConflict && code == "recipe_exists" && recipeID != "":
			return map[string]any{"saved": true, "alreadySaved": true, "method": preview.Method, "recipeId": recipeID}, nil
		case status.Status == http.StatusUnprocessableEntity || status.Status == http.StatusBadRequest:
			return notSaved(preview.Method, message), nil
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
	return map[string]any{"saved": true, "method": saved.Method, "recipe": saved.Recipe, "recipeId": saved.Recipe["id"]}, nil
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
		_, message, _ := importError(status.Body)
		return message, nil
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

// importError reads recipe-import's error envelope.
func importError(body []byte) (code, message, recipeID string) {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		RecipeID string `json:"recipeId"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Error.Message == "" {
		return "", strings.TrimSpace(string(body)), ""
	}
	return envelope.Error.Code, envelope.Error.Message, envelope.RecipeID
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
