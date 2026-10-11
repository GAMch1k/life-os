// Package api serves the recipe-import API: previews of a link or pasted
// text, and saving a recipe in Food.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/recipe-import/internal/api/generated"
	"github.com/GAMch1k/life-os/services/recipe-import/internal/fetch"
	"github.com/GAMch1k/life-os/services/recipe-import/internal/food"
	"github.com/GAMch1k/life-os/services/recipe-import/internal/recipe"
)

// Limits of what Food takes (its CreateRecipeRequest).
const (
	textLimit       = 20000
	urlLimit        = 2000
	titleLimit      = 200
	nameLimit       = 200
	amountLimit     = 50
	stepsLimit      = 20000
	noteLimit       = 1000
	ingredientLimit = 100
	tagsLimit       = 20
	tagLimit        = 40
	keyLimit        = 150
)

// importedType records a saved import in Activity; Food records the recipe
// itself (food.recipe.created), caused by it.
const importedType = "food.recipe.imported"

// Fetcher downloads pages (*fetch.Fetcher).
type Fetcher interface {
	CheckURL(raw string) (*url.URL, error)
	Fetch(ctx context.Context, raw string) (fetch.Page, error)
}

// Food saves recipes (*food.Client).
type Food interface {
	CreateRecipe(ctx context.Context, recipe food.NewRecipe, key string) (generated.Recipe, error)
	FindRecipe(ctx context.Context, title string) (*generated.Recipe, error)
}

// Handler serves the recipe-import API.
type Handler struct {
	fetcher  Fetcher
	food     Food
	activity *activity.Emitter
	logger   *slog.Logger
}

var _ generated.ServerInterface = (*Handler)(nil)

// NewHandler serves the API with fetcher and food; emitter (Activity) may be
// nil.
func NewHandler(fetcher Fetcher, food Food, emitter *activity.Emitter, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{fetcher: fetcher, food: food, activity: emitter, logger: logger}
}

// Register mounts the API on router. Every write must say who makes it in
// X-Life-OS-Actor.
func Register(router fiber.Router, handler *Handler) {
	router.Use(activity.RequireActor())
	generated.RegisterHandlers(router, handler)
}

func invalid(message string) error {
	return httpserver.NewError(fiber.StatusBadRequest, "invalid_request", message)
}

func decode(c fiber.Ctx, target any) error {
	if err := json.Unmarshal(c.Body(), target); err != nil {
		return invalid("the body must be a JSON object with the documented fields")
	}
	return nil
}

// link checks a link: an http or https URL to a public address.
func (h *Handler) link(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" || utf8.RuneCountInString(raw) > urlLimit {
		return nil, invalid("url must be an http or https link of at most 2000 characters")
	}
	parsed, err := h.fetcher.CheckURL(raw)
	if err != nil {
		return nil, invalid(err.Error())
	}
	return parsed, nil
}

// read fetches and parses the page at link.
func (h *Handler) read(ctx context.Context, link *url.URL) recipe.Result {
	page, err := h.fetcher.Fetch(ctx, link.String())
	if err != nil {
		var plain *fetch.Error
		message := "The page could not be downloaded."
		if errors.As(err, &plain) {
			message = plain.Message
		}
		return recipe.FromFetchError(link, message)
	}
	result := recipe.Parse(page.URL, page.Body)
	// The link the owner gave, not where it redirected (a login page).
	result.Draft.SourceURL = link.String()
	return result
}

func (h *Handler) PreviewImport(c fiber.Ctx) error {
	var body generated.PreviewRequest
	if err := decode(c, &body); err != nil {
		return err
	}
	link, err := h.link(body.Url)
	if err != nil {
		return err
	}
	return c.JSON(preview(h.read(c.Context(), link)))
}

func (h *Handler) ParseImportText(c fiber.Ctx) error {
	var body generated.TextRequest
	if err := decode(c, &body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Text) == "" || utf8.RuneCountInString(body.Text) > textLimit {
		return invalid("text must be 1 to 20000 characters")
	}
	source := ""
	if body.SourceUrl != nil && strings.TrimSpace(*body.SourceUrl) != "" {
		link, err := h.link(*body.SourceUrl)
		if err != nil {
			return err
		}
		source = link.String()
	}
	return c.JSON(preview(recipe.ParseText(body.Text, source)))
}

func (h *Handler) CreateImport(c fiber.Ctx, params generated.CreateImportParams) error {
	var body generated.ImportRequest
	if err := decode(c, &body); err != nil {
		return err
	}
	key := ""
	if params.IdempotencyKey != nil {
		key = *params.IdempotencyKey
		if utf8.RuneCountInString(key) > keyLimit {
			return invalid("Idempotency-Key must be at most 150 characters")
		}
	}
	link, err := h.link(body.Url)
	if err != nil {
		return err
	}
	ctx := c.Context()
	method := generated.Edited
	var draft recipe.Draft
	if body.Overrides != nil {
		if draft, err = fromOverrides(*body.Overrides); err != nil {
			return err
		}
	} else {
		result := h.read(ctx, link)
		if result.Method == recipe.Failed {
			return httpserver.NewError(fiber.StatusUnprocessableEntity, "import_failed", result.Reason)
		}
		method, draft = generated.ImportMethod(result.Method), result.Draft
	}
	draft.SourceURL = link.String()
	newRecipe := forFood(draft)
	if newRecipe.Title == "" {
		return invalid("the recipe needs a title")
	}

	// The import's own event causes Food's: its id is known before the
	// save, and a retried save (same key) names the same event.
	eventID := uuid.New()
	if key != "" {
		eventID = activity.EventID(importedType, link.String(), key)
	}
	trace := activity.FromRequest(c)
	actor := trace.Actor
	trace.CausationID = &eventID
	saved, err := h.food.CreateRecipe(activity.WithTrace(ctx, trace), newRecipe, key)
	var answer *food.StatusError
	switch {
	case food.Is(err, http.StatusConflict, "recipe_exists"):
		return h.exists(c, newRecipe.Title)
	case food.Is(err, http.StatusConflict, "idempotency_conflict"):
		return httpserver.NewError(fiber.StatusConflict, "idempotency_conflict", "The Idempotency-Key was already used with a different request")
	case food.Is(err, http.StatusBadRequest, "") && errors.As(err, &answer):
		return invalid(answer.Message)
	case err != nil:
		h.logger.Warn("recipe not saved in Food", "error", err)
		return httpserver.WrapError(err, fiber.StatusBadGateway, "food_unavailable", "Food could not save the recipe right now. Try again in a moment.")
	}
	site := recipe.SiteName(link)
	h.activity.Emit(ctx, activity.Event{
		ID:            eventID,
		Type:          importedType,
		Subject:       activity.Subject{Type: "food_recipe", ID: saved.Id.String()},
		CorrelationID: trace.CorrelationID,
		Actor:         actor,
		Outcome:       activity.Succeeded,
		Summary:       activity.Preview("Imported the recipe “"+saved.Title+"” from "+site, 500),
		Data: map[string]any{"title": activity.Preview(saved.Title, 200), "method": string(method), "site": site,
			"sourceUrl": link.String(), "ingredients": len(saved.Ingredients)},
	})
	return c.Status(fiber.StatusCreated).JSON(generated.ImportResult{Method: method, Recipe: saved})
}

// exists answers a save whose title Food already has, with that recipe's id
// when it can be found.
func (h *Handler) exists(c fiber.Ctx, title string) error {
	answer := generated.RecipeExistsError{RequestId: httpserver.RequestID(c)}
	answer.Error.Code = "recipe_exists"
	answer.Error.Message = "A recipe called “" + title + "” is already saved."
	if existing, err := h.food.FindRecipe(c.Context(), title); err == nil && existing != nil {
		answer.RecipeId = &existing.Id
	}
	return c.Status(fiber.StatusConflict).JSON(answer)
}

// preview is a parse result as the API answers it.
func preview(result recipe.Result) generated.ImportPreview {
	draft := result.Draft
	out := generated.ImportPreview{Method: generated.ImportMethod(result.Method), Draft: generated.RecipeDraft{
		Title: draft.Title, Ingredients: make([]generated.DraftIngredient, 0, len(draft.Ingredients)),
		Steps: append([]string{}, draft.Steps...), Tags: append([]string{}, draft.Tags...),
		SourceUrl: optional(draft.SourceURL), ImageUrl: optional(draft.ImageURL),
		Description: optional(draft.Description), SiteName: optional(draft.SiteName),
	}}
	if result.Reason != "" {
		out.Reason = &result.Reason
	}
	if draft.Servings > 0 {
		out.Draft.Servings = &draft.Servings
	}
	for _, item := range draft.Ingredients {
		out.Draft.Ingredients = append(out.Draft.Ingredients, generated.DraftIngredient{Name: item.Name, Amount: optional(item.Amount)})
	}
	return out
}

func optional(text string) *string {
	if text == "" {
		return nil
	}
	return &text
}

// fromOverrides reads the draft the owner edited.
func fromOverrides(overrides generated.RecipeDraft) (recipe.Draft, error) {
	draft := recipe.Draft{Title: strings.TrimSpace(overrides.Title), Steps: overrides.Steps, Tags: overrides.Tags}
	if draft.Title == "" {
		return draft, invalid("overrides.title must not be empty")
	}
	if overrides.Servings != nil {
		if *overrides.Servings < 1 || *overrides.Servings > 100 {
			return draft, invalid("overrides.servings must be between 1 and 100")
		}
		draft.Servings = *overrides.Servings
	}
	if len(overrides.Ingredients) > ingredientLimit {
		return draft, invalid("overrides.ingredients must hold at most 100 ingredients")
	}
	for _, item := range overrides.Ingredients {
		ingredient := recipe.Ingredient{Name: strings.TrimSpace(item.Name)}
		if item.Amount != nil {
			ingredient.Amount = strings.TrimSpace(*item.Amount)
		}
		draft.Ingredients = append(draft.Ingredients, ingredient)
	}
	if overrides.Description != nil {
		draft.Description = *overrides.Description
	}
	if overrides.ImageUrl != nil {
		draft.ImageURL = *overrides.ImageUrl
	}
	return draft, nil
}

// forFood is a draft as Food takes it, within Food's limits: empty
// ingredients left out, steps one per line, tags Food would refuse left
// out, and the description and image link in the note.
func forFood(draft recipe.Draft) food.NewRecipe {
	out := food.NewRecipe{Title: clip(strings.TrimSpace(draft.Title), titleLimit), Servings: draft.Servings, SourceURL: clip(draft.SourceURL, urlLimit)}
	for _, item := range draft.Ingredients {
		if name := strings.TrimSpace(item.Name); name != "" && len(out.Ingredients) < ingredientLimit {
			out.Ingredients = append(out.Ingredients, food.Ingredient{Name: clip(name, nameLimit), Amount: clip(strings.TrimSpace(item.Amount), amountLimit)})
		}
	}
	var steps []string
	for _, step := range draft.Steps {
		if step = strings.Join(strings.Fields(step), " "); step != "" {
			steps = append(steps, step)
		}
	}
	out.Steps = clip(strings.Join(steps, "\n"), stepsLimit)
	for _, tag := range draft.Tags {
		tag = strings.TrimSpace(tag)
		if tag != "" && utf8.RuneCountInString(tag) <= tagLimit && strings.IndexFunc(tag, isWordRune) >= 0 && len(out.Tags) < tagsLimit {
			out.Tags = append(out.Tags, tag)
		}
	}
	var note []string
	if description := strings.TrimSpace(draft.Description); description != "" {
		note = append(note, description)
	}
	if image := strings.TrimSpace(draft.ImageURL); image != "" {
		note = append(note, "Image: "+image)
	}
	out.Note = clip(strings.Join(note, "\n"), noteLimit)
	return out
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func clip(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit-1]) + "…"
}
