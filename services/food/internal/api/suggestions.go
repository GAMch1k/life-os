package api

import (
	"context"
	"sort"
	"time"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
	"github.com/GAMch1k/life-os/services/food/internal/domain"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

// Suggestion limits.
const (
	suggestionsDefault = 10
	suggestionsMax     = 50
	// rankedRecipesLimit bounds how many recipes one ranking reads.
	rankedRecipesLimit = 2000
)

// household reads what the ranking knows of the household: the in-stock
// pantry, what was bought lately, and the preferences.
func (h *Handler) household(ctx context.Context, q *store.Queries, today time.Time, slot string) (domain.Household, error) {
	home := domain.Household{Today: today, Slot: slot}
	stocked, err := q.ListInStockPantryItems(ctx)
	if err != nil {
		return home, err
	}
	for _, item := range stocked {
		entry := domain.StockedItem{ID: uuid(item.ID).String(), Name: item.Name, Normalized: item.NormalizedName}
		if item.ExpiresOn.Valid {
			entry.ExpiresOn = item.ExpiresOn.Time
		}
		home.Stocked = append(home.Stocked, entry)
	}
	since := h.now().AddDate(0, 0, -domain.HistoryDays)
	if home.Bought, err = q.ListBoughtNames(ctx, pgtype.Timestamptz{Time: since, Valid: true}); err != nil {
		return home, err
	}
	preferences, err := q.GetPreferences(ctx)
	if err != nil {
		return home, err
	}
	home.Preferred = preferences.Methods
	for _, dislike := range preferences.Dislikes {
		if normalized := domain.Normalize(dislike); normalized != "" {
			home.Dislikes = append(home.Dislikes, normalized)
		}
	}
	return home, nil
}

// ranked is a recipe with its ranking.
type ranked struct {
	recipe      store.Recipe
	ingredients []store.RecipeIngredient
	result      domain.Ranked
}

func (h *Handler) ListSuggestions(c fiber.Ctx, params generated.ListSuggestionsParams) error {
	limit := suggestionsDefault
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > suggestionsMax {
			return invalid("limit must be between 1 and 50")
		}
		limit = *params.Limit
	}
	slot := ""
	if params.MealSlot != nil {
		if !params.MealSlot.Valid() {
			return invalid("mealSlot must be breakfast, lunch, or dinner")
		}
		slot = string(*params.MealSlot)
	}
	ctx := c.Context()
	today := h.today()
	home, err := h.household(ctx, h.queries, today, slot)
	if err != nil {
		return internal(err)
	}
	recipes, err := h.queries.ListAllRecipes(ctx, rankedRecipesLimit)
	if err != nil {
		return internal(err)
	}
	ingredients, err := ingredientsOf(ctx, h.queries, recipes...)
	if err != nil {
		return internal(err)
	}
	planned, err := h.queries.ListPlannedRecipes(ctx, store.ListPlannedRecipesParams{
		FromDay:    pgtype.Date{Time: today.AddDate(0, 0, -domain.HistoryDays), Valid: true},
		ThroughDay: pgtype.Date{Time: today.AddDate(0, 0, domain.RecentDays), Valid: true},
	})
	if err != nil {
		return internal(err)
	}
	meals := map[[16]byte][]domain.PlannedMeal{}
	for _, meal := range planned {
		meals[meal.RecipeID.Bytes] = append(meals[meal.RecipeID.Bytes], domain.PlannedMeal{Day: meal.Day.Time, Slot: meal.Slot})
	}

	response := generated.SuggestionList{Today: types.Date{Time: today}, Items: []generated.Suggestion{}, Excluded: []generated.SuggestionExclusion{}}
	if params.MealSlot != nil {
		response.MealSlot = params.MealSlot
	}
	var candidates []ranked
	for _, recipe := range recipes {
		methods, _ := recipeMethods(recipe)
		facts := domain.RecipeFacts{Title: recipe.Title, NormalizedTitle: recipe.NormalizedTitle, Methods: methods, Tags: recipe.Tags,
			Planned: meals[recipe.ID.Bytes]}
		for _, ingredient := range ingredients[recipe.ID.Bytes] {
			facts.Ingredients = append(facts.Ingredients, domain.RecipeIngredient{Name: ingredient.Name, Normalized: ingredient.NormalizedName, Amount: ingredient.Amount})
		}
		result := domain.Rank(facts, home)
		if result.Disliked != "" {
			response.Excluded = append(response.Excluded, generated.SuggestionExclusion{
				Recipe: generated.RecipeRef{Id: uuid(recipe.ID), Title: recipe.Title}, Disliked: result.Disliked})
			continue
		}
		candidates = append(candidates, ranked{recipe: recipe, ingredients: ingredients[recipe.ID.Bytes], result: result})
	}
	// Highest score first, then fewer to buy, then by title (the recipes
	// are read by title, and the sort is stable).
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i].result, candidates[j].result
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return len(a.Missing) < len(b.Missing)
	})
	for _, candidate := range candidates[:min(len(candidates), limit)] {
		response.Items = append(response.Items, suggestionView(candidate))
	}
	return c.JSON(response)
}

func suggestionView(candidate ranked) generated.Suggestion {
	result := candidate.result
	view := generated.Suggestion{
		Recipe: recipeView(candidate.recipe, candidate.ingredients), Score: result.Score,
		Reasons: make([]generated.SuggestionReason, 0, len(result.Reasons)), Have: make([]generated.SuggestionUse, 0, len(result.Have)),
		Missing: make([]generated.SuggestionMissing, 0, len(result.Missing)), Basics: make([]string, 0, len(result.Basics)),
		Expiring: []generated.SuggestionUse{},
	}
	for _, reason := range result.Reasons {
		view.Reasons = append(view.Reasons, generated.SuggestionReason{Kind: generated.SuggestionReasonKind(reason.Kind), Points: reason.Points, Text: reason.Text})
	}
	for _, use := range result.Have {
		entry := useView(use)
		view.Have = append(view.Have, entry)
		if use.Expiring {
			view.Expiring = append(view.Expiring, entry)
		}
	}
	sort.SliceStable(view.Expiring, func(i, j int) bool { return *view.Expiring[i].ExpiresInDays < *view.Expiring[j].ExpiresInDays })
	for _, ingredient := range result.Missing {
		view.Missing = append(view.Missing, generated.SuggestionMissing{Name: ingredient.Name, Amount: ingredient.Amount})
	}
	for _, ingredient := range result.Basics {
		view.Basics = append(view.Basics, ingredient.Name)
	}
	return view
}

func useView(use domain.Use) generated.SuggestionUse {
	view := generated.SuggestionUse{Ingredient: use.Ingredient.Name, PantryItemName: use.Item.Name}
	if id, err := googleuuid.Parse(use.Item.ID); err == nil {
		view.PantryItemId = id
	}
	if !use.Item.ExpiresOn.IsZero() {
		view.ExpiresOn = &types.Date{Time: use.Item.ExpiresOn}
	}
	if use.Expiring {
		view.ExpiresInDays = pointer(use.Days)
	}
	return view
}
