package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

// recipeBatchLimit is how many recipes one from-recipes request may name.
const recipeBatchLimit = 50

// needed is one ingredient the recipes need: the first recipe's name for it,
// the amounts every recipe gives, and the recipes.
type needed struct {
	name, normalized string
	amounts          []string
	recipes          []generated.RecipeRef
	firstRecipe      pgtype.UUID
}

// joinAmounts joins the amounts several recipes give one ingredient, in
// order: "200 g + 1 Pck.". Amounts are free text, so nothing is added up;
// empty ones are left out. The result is shortened to the amount limit.
func joinAmounts(amounts []string) *string {
	parts := make([]string, 0, len(amounts))
	for _, amount := range amounts {
		if text := strings.TrimSpace(amount); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	joined := strings.Join(parts, " + ")
	if utf8.RuneCountInString(joined) > amountLimit {
		joined = string([]rune(joined)[:amountLimit-1]) + "…"
	}
	return &joined
}

// collectIngredients gathers the ingredients of recipes in order, one line
// per normalised name.
func collectIngredients(recipes []store.Recipe, ingredients map[[16]byte][]store.RecipeIngredient) []*needed {
	var out []*needed
	index := map[string]*needed{}
	for _, recipe := range recipes {
		ref := generated.RecipeRef{Id: uuid(recipe.ID), Title: recipe.Title}
		for _, ingredient := range ingredients[recipe.ID.Bytes] {
			entry, ok := index[ingredient.NormalizedName]
			if !ok {
				entry = &needed{name: ingredient.Name, normalized: ingredient.NormalizedName, firstRecipe: recipe.ID}
				index[ingredient.NormalizedName] = entry
				out = append(out, entry)
			}
			if ingredient.Amount != nil {
				entry.amounts = append(entry.amounts, *ingredient.Amount)
			}
			if !containsRecipe(entry.recipes, ref) {
				entry.recipes = append(entry.recipes, ref)
			}
		}
	}
	return out
}

func containsRecipe(refs []generated.RecipeRef, ref generated.RecipeRef) bool {
	for _, existing := range refs {
		if existing.Id == ref.Id {
			return true
		}
	}
	return false
}

// fromRecipesRequest is a checked from-recipes body: the recipes named, or
// the plan's date range.
type fromRecipesRequest struct {
	recipeIDs []pgtype.UUID
	from, to  pgtype.Date
	dryRun    bool
}

func checkFromRecipes(body generated.FromRecipesRequest) (fromRecipesRequest, error) {
	request := fromRecipesRequest{dryRun: body.DryRun != nil && *body.DryRun}
	byRange := body.From != nil || body.To != nil
	switch {
	case body.RecipeIds != nil && byRange:
		return request, invalid("give recipeIds, or from and to, not both")
	case body.RecipeIds != nil:
		if len(*body.RecipeIds) == 0 || len(*body.RecipeIds) > recipeBatchLimit {
			return request, invalid(fmt.Sprintf("recipeIds must hold 1 to %d recipes", recipeBatchLimit))
		}
		seen := map[[16]byte]bool{}
		for _, id := range *body.RecipeIds {
			if !seen[id] {
				seen[id] = true
				request.recipeIDs = append(request.recipeIDs, pgUUID(id))
			}
		}
	case body.From != nil && body.To != nil:
		var err error
		if request.from, request.to, err = planRange(body.From.Time, body.To.Time); err != nil {
			return request, err
		}
	default:
		return request, invalid("give recipeIds, or from and to")
	}
	return request, nil
}

// plannedRecipes reads the recipes a request names, in its order (each
// once): those given, or those planned in its range.
func plannedRecipes(ctx context.Context, q *store.Queries, request fromRecipesRequest) ([]store.Recipe, error) {
	ids := request.recipeIDs
	if ids == nil {
		planned, err := q.ListPlannedRecipeIDs(ctx, store.ListPlannedRecipeIDsParams{FromDay: request.from, ThroughDay: request.to})
		if err != nil {
			return nil, err
		}
		seen := map[[16]byte]bool{}
		for _, id := range planned {
			if !seen[id.Bytes] {
				seen[id.Bytes] = true
				ids = append(ids, id)
			}
		}
	}
	recipes := make([]store.Recipe, 0, len(ids))
	for index, id := range ids {
		recipe, err := q.GetRecipe(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apiError(fiber.StatusNotFound, "not_found", fmt.Sprintf("recipeIds[%d]: Recipe not found", index))
		}
		if err != nil {
			return nil, err
		}
		recipes = append(recipes, recipe)
	}
	return recipes, nil
}

// missingIngredients works out, for a request, every ingredient and whether
// it is in stock or already on the list; the rest are to add.
func missingIngredients(ctx context.Context, q *store.Queries, request fromRecipesRequest) ([]*needed, []generated.IngredientResult, error) {
	recipes, err := plannedRecipes(ctx, q, request)
	if err != nil {
		return nil, nil, err
	}
	ingredients, err := ingredientsOf(ctx, q, recipes...)
	if err != nil {
		return nil, nil, err
	}
	needs := collectIngredients(recipes, ingredients)
	names := make([]string, 0, len(needs))
	for _, need := range needs {
		names = append(names, need.normalized)
	}
	stocked, err := q.ListInStockNames(ctx, names)
	if err != nil {
		return nil, nil, err
	}
	inPantry := make(map[string]store.PantryItem, len(stocked))
	for _, item := range stocked {
		inPantry[item.NormalizedName] = item
	}
	open, err := q.ListOpenShoppingItemsByName(ctx, names)
	if err != nil {
		return nil, nil, err
	}
	onList := make(map[string]store.ShoppingItem, len(open))
	for _, item := range open {
		onList[item.NormalizedName] = item
	}
	storeNameMap, err := storeNames(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	results := make([]generated.IngredientResult, len(needs))
	for index, need := range needs {
		results[index] = generated.IngredientResult{Name: need.name, NormalizedName: need.normalized, Amount: joinAmounts(need.amounts),
			Recipes: need.recipes, Status: generated.Added}
		if item, ok := inPantry[need.normalized]; ok {
			results[index].Status, results[index].PantryItem = generated.InPantry, pointer(pantryView(item))
		} else if item, ok := onList[need.normalized]; ok {
			results[index].Status, results[index].ShoppingItem = generated.OnList, pointer(shoppingView(item, storeNameMap))
		}
	}
	return needs, results, nil
}

// AddShoppingItemsFromRecipes puts the ingredients of recipes that are
// neither in stock nor already needed on the shopping list, or with dryRun
// only says what it would do.
func (h *Handler) AddShoppingItemsFromRecipes(c fiber.Ctx, _ generated.AddShoppingItemsFromRecipesParams) error {
	var body generated.FromRecipesRequest
	if _, err := decodeObject(c.Body(), &body); err != nil {
		return invalid(err.Error())
	}
	request, err := checkFromRecipes(body)
	if err != nil {
		return err
	}
	if request.dryRun {
		_, results, err := missingIngredients(c.Context(), h.queries, request)
		if err != nil {
			return passThrough(err)
		}
		return c.JSON(generated.FromRecipesResult{DryRun: true, Items: results})
	}
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		needs, results, err := missingIngredients(ctx, q, request)
		if err != nil {
			return result{}, err
		}
		var inputs []newShoppingItem
		var positions []int
		for index, need := range needs {
			if results[index].Status != generated.Added {
				continue
			}
			inputs = append(inputs, newShoppingItem{name: need.name, normalized: need.normalized, amount: results[index].Amount,
				source: string(generated.ItemSourceRecipe), sourceRef: pointer(uuid(need.firstRecipe).String())})
			positions = append(positions, index)
		}
		response := generated.FromRecipesResult{Items: results}
		if len(inputs) == 0 {
			return result{status: fiber.StatusOK, body: response}, nil
		}
		added, err := h.addShoppingItems(ctx, q, actor, inputs)
		if err != nil {
			return result{}, err
		}
		for at, index := range positions {
			response.Items[index].ShoppingItem = pointer(shoppingView(added.rows[at], added.names))
			if added.merged[at] {
				response.Items[index].Status = generated.Merged
			}
		}
		return result{status: fiber.StatusOK, body: response, events: added.events}, nil
	})
}
