package api

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
	"github.com/GAMch1k/life-os/services/food/internal/domain"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

// Idea limits.
const (
	reasonLimit  = 500
	ideasDefault = 50
	ideasMax     = 100
	// ideaTitleKey keeps one new idea per normalised title.
	ideaTitleKey = "food_ideas_new_title_key"
)

// ideaIngredient is one ingredient as an idea keeps it (JSON).
type ideaIngredient struct {
	Name   string  `json:"name"`
	Amount *string `json:"amount"`
	Note   *string `json:"note"`
}

func ideaIngredients(raw []byte) []ideaIngredient {
	var out []ideaIngredient
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return []ideaIngredient{}
	}
	return out
}

func ideaView(idea store.FoodIdea) generated.Idea {
	view := generated.Idea{
		Id: uuid(idea.ID), Title: idea.Title, NormalizedTitle: idea.NormalizedTitle, Steps: idea.Steps, Tags: idea.Tags,
		Methods: cookingMethods(idea.Methods), Source: generated.IdeaSource(idea.Source), SourceUrl: idea.SourceUrl, Reason: idea.Reason,
		State: generated.IdeaState(idea.State), RecipeId: nullableUUID(idea.RecipeID), Ingredients: []generated.IdeaIngredient{},
		CreatedAt: idea.CreatedAt.Time.UTC(), UpdatedAt: idea.UpdatedAt.Time.UTC(),
		CreatedBy: actorOf(idea.CreatedBy), UpdatedBy: actorOf(idea.UpdatedBy),
	}
	if view.Tags == nil {
		view.Tags = []string{}
	}
	if idea.Servings != nil {
		view.Servings = pointer(int(*idea.Servings))
	}
	for _, ingredient := range ideaIngredients(idea.Ingredients) {
		view.Ingredients = append(view.Ingredients, generated.IdeaIngredient{Name: ingredient.Name, Amount: ingredient.Amount, Note: ingredient.Note})
	}
	return view
}

func (h *Handler) ListIdeas(c fiber.Ctx, params generated.ListIdeasParams) error {
	state := generated.New
	if params.State != nil {
		if !params.State.Valid() {
			return invalid("state must be new, saved, or dismissed")
		}
		state = *params.State
	}
	limit := ideasDefault
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > ideasMax {
			return invalid("limit must be between 1 and 100")
		}
		limit = *params.Limit
	}
	ideas, err := h.queries.ListIdeas(c.Context(), store.ListIdeasParams{State: string(state), RowLimit: int32(limit)})
	if err != nil {
		return internal(err)
	}
	result := generated.IdeaList{Items: make([]generated.Idea, 0, len(ideas))}
	for _, idea := range ideas {
		result.Items = append(result.Items, ideaView(idea))
	}
	return c.JSON(result)
}

func (h *Handler) GetIdea(c fiber.Ctx, ideaID generated.IdeaId) error {
	idea, err := h.queries.GetIdea(c.Context(), pgUUID(ideaID))
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound("Idea")
	}
	if err != nil {
		return internal(err)
	}
	return c.JSON(ideaView(idea))
}

func (h *Handler) CreateIdea(c fiber.Ctx, _ generated.CreateIdeaParams) error {
	if actor, ok := activity.ParseActor(c.Get(activity.ActorHeader)); ok && actor.Kind == "user" {
		return apiError(fiber.StatusForbidden, "actor_not_allowed", "Ideas are written by the AI or a Life OS component, not by the owner; save a recipe instead")
	}
	var body generated.CreateIdeaRequest
	if _, err := decodeObject(c.Body(), &body); err != nil {
		return invalid(err.Error())
	}
	params := store.InsertIdeaParams{Source: string(generated.Ai), Tags: []string{}}
	var err error
	if params.Title, params.NormalizedTitle, err = cleanName("title", body.Title, titleLimit); err != nil {
		return err
	}
	if params.Servings, err = checkServings(body.Servings); err != nil {
		return err
	}
	ingredients := []ideaIngredient{}
	if body.Ingredients != nil {
		checked, err := checkIngredients(*body.Ingredients)
		if err != nil {
			return err
		}
		for _, ingredient := range checked {
			ingredients = append(ingredients, ideaIngredient{Name: ingredient.Name, Amount: ingredient.Amount, Note: ingredient.Note})
		}
	}
	if params.Ingredients, err = json.Marshal(ingredients); err != nil {
		return internal(err)
	}
	if params.Steps, err = checkSteps(body.Steps); err != nil {
		return err
	}
	if body.Tags != nil {
		if params.Tags, err = checkTags(*body.Tags); err != nil {
			return err
		}
	}
	if body.Methods != nil {
		if params.Methods, err = checkMethods(*body.Methods); err != nil {
			return err
		}
	} else {
		steps := ""
		if params.Steps != nil {
			steps = *params.Steps
		}
		params.Methods = domain.InferMethods(params.Title, params.Tags, steps)
	}
	if body.Source != nil {
		if !body.Source.Valid() {
			return invalid("source must be ai or web")
		}
		params.Source = string(*body.Source)
	}
	if params.SourceUrl, err = checkSourceURL(body.SourceUrl); err != nil {
		return err
	}
	reason, err := cleanText("reason", &body.Reason, reasonLimit)
	if err != nil {
		return err
	}
	if reason == nil {
		return invalid("reason must say why the idea is suggested")
	}
	params.Reason = *reason
	params.Actor = requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		if _, err := q.GetRecipeByTitle(ctx, params.NormalizedTitle); err == nil {
			return result{}, recipeExists(params.Title)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return result{}, err
		}
		idea, err := q.InsertIdea(ctx, params)
		if uniqueViolation(err, ideaTitleKey) {
			return result{}, apiError(fiber.StatusConflict, "idea_exists", "An idea called “"+params.Title+"” is already waiting")
		}
		if err != nil {
			return result{}, err
		}
		return result{status: fiber.StatusCreated, body: ideaView(idea), events: []activity.Event{ideaEvent(ideaCreated, idea)}}, nil
	})
}

// SaveIdea creates a recipe from an idea and marks the idea saved.
func (h *Handler) SaveIdea(c fiber.Ctx, ideaID generated.IdeaId, _ generated.SaveIdeaParams) error {
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		idea, err := q.GetIdeaForUpdate(ctx, pgUUID(ideaID))
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, notFound("Idea")
		}
		if err != nil {
			return result{}, err
		}
		if idea.State == string(generated.Saved) && idea.RecipeID.Valid {
			recipe, err := q.GetRecipe(ctx, idea.RecipeID)
			if err != nil {
				return result{}, err
			}
			stored, err := ingredientsOf(ctx, q, recipe)
			if err != nil {
				return result{}, err
			}
			return result{status: fiber.StatusOK, body: generated.SaveIdeaResult{Idea: ideaView(idea), Recipe: recipeView(recipe, stored[recipe.ID.Bytes])}}, nil
		}
		params := store.InsertRecipeParams{Title: idea.Title, NormalizedTitle: idea.NormalizedTitle, Servings: idea.Servings, Steps: idea.Steps,
			Tags: idea.Tags, SourceUrl: idea.SourceUrl, Note: &idea.Reason, Actor: actor}
		if params.Tags == nil {
			params.Tags = []string{}
		}
		// Methods the idea was given are kept; inferred ones (or none) are
		// left for the recipe to infer, so they follow later edits.
		steps := ""
		if idea.Steps != nil {
			steps = *idea.Steps
		}
		if len(idea.Methods) > 0 && !slices.Equal(idea.Methods, domain.InferMethods(idea.Title, idea.Tags, steps)) {
			params.Methods = idea.Methods
		}
		recipe, err := q.InsertRecipe(ctx, params)
		if uniqueViolation(err, recipeTitleKey) {
			return result{}, recipeExists(idea.Title)
		}
		if err != nil {
			return result{}, err
		}
		var rows []store.InsertRecipeIngredientParams
		for index, ingredient := range ideaIngredients(idea.Ingredients) {
			normalized := domain.Normalize(ingredient.Name)
			if normalized == "" {
				continue
			}
			rows = append(rows, store.InsertRecipeIngredientParams{Position: int32(index), Name: ingredient.Name, NormalizedName: normalized,
				Amount: ingredient.Amount, Note: ingredient.Note})
		}
		ingredients, err := writeIngredients(ctx, q, recipe.ID, rows)
		if err != nil {
			return result{}, err
		}
		saved, err := q.SetIdeaState(ctx, store.SetIdeaStateParams{ID: idea.ID, State: string(generated.Saved), RecipeID: recipe.ID, Actor: actor})
		if err != nil {
			return result{}, err
		}
		return result{status: fiber.StatusCreated, body: generated.SaveIdeaResult{Idea: ideaView(saved), Recipe: recipeView(recipe, ingredients)},
			events: []activity.Event{recipeEvent(recipeCreated, recipe, len(ingredients), nil), ideaEvent(ideaSaved, saved)}}, nil
	})
}

func (h *Handler) DismissIdea(c fiber.Ctx, ideaID generated.IdeaId, _ generated.DismissIdeaParams) error {
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		idea, err := q.GetIdeaForUpdate(ctx, pgUUID(ideaID))
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, notFound("Idea")
		}
		if err != nil {
			return result{}, err
		}
		switch idea.State {
		case string(generated.Saved):
			return result{}, apiError(fiber.StatusConflict, "idea_saved", "“"+idea.Title+"” was already saved as a recipe")
		case string(generated.Dismissed):
			return result{status: fiber.StatusOK, body: ideaView(idea)}, nil
		}
		dismissed, err := q.SetIdeaState(ctx, store.SetIdeaStateParams{ID: idea.ID, State: string(generated.Dismissed), Actor: actor})
		if err != nil {
			return result{}, err
		}
		return result{status: fiber.StatusOK, body: ideaView(dismissed), events: []activity.Event{ideaEvent(ideaDismissed, dismissed)}}, nil
	})
}
