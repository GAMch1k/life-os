package api

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
	"github.com/GAMch1k/life-os/services/food/internal/domain"
	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

// Recipe limits (the contract's).
const (
	titleLimit          = 200
	ingredientLimit     = 100
	ingredientNoteLimit = 200
	stepsLimit          = 20000
	tagsLimit           = 20
	tagLimit            = 40
	sourceURLLimit      = 2000
	servingsLimit       = 100
)

// recipeTitleKey is the unique index that keeps one recipe per normalised
// title.
const recipeTitleKey = "recipes_normalized_title_key"

func recipeExists(title string) error {
	return apiError(fiber.StatusConflict, "recipe_exists", "A recipe called “"+title+"” already exists")
}

// recipeMethods is how a recipe is cooked: the methods set on it, or else
// those its title, tags, and steps name (inferred is then true).
func recipeMethods(recipe store.Recipe) (methods []string, inferred bool) {
	if recipe.Methods != nil {
		return recipe.Methods, false
	}
	steps := ""
	if recipe.Steps != nil {
		steps = *recipe.Steps
	}
	return domain.InferMethods(recipe.Title, recipe.Tags, steps), true
}

func cookingMethods(values []string) []generated.CookingMethod {
	out := make([]generated.CookingMethod, 0, len(values))
	for _, value := range values {
		out = append(out, generated.CookingMethod(value))
	}
	return out
}

// checkMethods keeps known methods once each, in the order domain.Methods
// lists them; an empty list stays empty (not nil).
func checkMethods(values []generated.CookingMethod) ([]string, error) {
	if len(values) > len(domain.Methods) {
		return nil, invalid(fmt.Sprintf("methods must hold at most %d methods", len(domain.Methods)))
	}
	given := map[string]bool{}
	for index, value := range values {
		if !domain.ValidMethod(string(value)) {
			return nil, invalid(fmt.Sprintf("methods[%d] must be one of %s", index, strings.Join(domain.Methods, ", ")))
		}
		given[string(value)] = true
	}
	out := []string{}
	for _, method := range domain.Methods {
		if given[method] {
			out = append(out, method)
		}
	}
	return out, nil
}

func recipeView(recipe store.Recipe, ingredients []store.RecipeIngredient) generated.Recipe {
	methods, inferred := recipeMethods(recipe)
	view := generated.Recipe{
		Id: uuid(recipe.ID), Title: recipe.Title, NormalizedTitle: recipe.NormalizedTitle, Steps: recipe.Steps,
		Tags: recipe.Tags, SourceUrl: recipe.SourceUrl, Note: recipe.Note,
		Methods: cookingMethods(methods), MethodsInferred: inferred,
		Ingredients: make([]generated.Ingredient, 0, len(ingredients)),
		CreatedAt:   recipe.CreatedAt.Time.UTC(), UpdatedAt: recipe.UpdatedAt.Time.UTC(),
		CreatedBy: actorOf(recipe.CreatedBy), UpdatedBy: actorOf(recipe.UpdatedBy),
	}
	if view.Tags == nil {
		view.Tags = []string{}
	}
	if recipe.Servings != nil {
		view.Servings = pointer(int(*recipe.Servings))
	}
	for _, ingredient := range ingredients {
		view.Ingredients = append(view.Ingredients, generated.Ingredient{Name: ingredient.Name, NormalizedName: ingredient.NormalizedName,
			Amount: ingredient.Amount, Note: ingredient.Note})
	}
	return view
}

// ingredientsOf reads the ingredients of recipes, by recipe id.
func ingredientsOf(ctx context.Context, q *store.Queries, recipes ...store.Recipe) (map[[16]byte][]store.RecipeIngredient, error) {
	ids := make([]pgtype.UUID, 0, len(recipes))
	for _, recipe := range recipes {
		ids = append(ids, recipe.ID)
	}
	rows, err := q.ListRecipeIngredients(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[[16]byte][]store.RecipeIngredient, len(recipes))
	for _, row := range rows {
		out[row.RecipeID.Bytes] = append(out[row.RecipeID.Bytes], row)
	}
	return out, nil
}

// recipeInput is a checked recipe body: what a create sets, or what an
// update names (see present).
type recipeInput struct {
	title, normalized string
	servings          *int32
	ingredients       []store.InsertRecipeIngredientParams
	steps             *string
	tags              []string
	sourceURL         *string
	note              *string
	// methods as set; nil lets Food infer them.
	methods []string
}

func checkServings(value *int) (*int32, error) {
	if value == nil {
		return nil, nil
	}
	if *value < 1 || *value > servingsLimit {
		return nil, invalid(fmt.Sprintf("servings must be between 1 and %d", servingsLimit))
	}
	return pointer(int32(*value)), nil
}

func checkIngredients(items []generated.NewIngredient) ([]store.InsertRecipeIngredientParams, error) {
	if len(items) > ingredientLimit {
		return nil, invalid(fmt.Sprintf("ingredients must hold at most %d ingredients", ingredientLimit))
	}
	out := make([]store.InsertRecipeIngredientParams, 0, len(items))
	for index, item := range items {
		field := fmt.Sprintf("ingredients[%d]", index)
		var params store.InsertRecipeIngredientParams
		var err error
		if params.Name, params.NormalizedName, err = cleanName(field+".name", item.Name, nameLimit); err != nil {
			return nil, err
		}
		if params.Amount, err = cleanText(field+".amount", item.Amount, amountLimit); err != nil {
			return nil, err
		}
		if params.Note, err = cleanText(field+".note", item.Note, ingredientNoteLimit); err != nil {
			return nil, err
		}
		params.Position = int32(index)
		out = append(out, params)
	}
	return out, nil
}

// checkSteps keeps steps as plain text with Unix line ends; empty is none.
func checkSteps(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	text := strings.ReplaceAll(strings.ReplaceAll(*value, "\r\n", "\n"), "\r", "\n")
	return cleanText("steps", &text, stepsLimit)
}

// checkTags keeps tags normalised, without duplicates, in the order given.
func checkTags(values []string) ([]string, error) {
	if len(values) > tagsLimit {
		return nil, invalid(fmt.Sprintf("tags must hold at most %d tags", tagsLimit))
	}
	out := make([]string, 0, len(values))
	for index, value := range values {
		trimmed := strings.TrimSpace(value)
		tag := domain.Normalize(trimmed)
		if tag == "" || utf8.RuneCountInString(trimmed) > tagLimit {
			return nil, invalid(fmt.Sprintf("tags[%d] must be 1 to %d characters with a letter or a digit", index, tagLimit))
		}
		if !slices.Contains(out, tag) {
			out = append(out, tag)
		}
	}
	return out, nil
}

func checkSourceURL(value *string) (*string, error) {
	cleaned, err := cleanText("sourceUrl", value, sourceURLLimit)
	if err != nil || cleaned == nil {
		return cleaned, err
	}
	parsed, err := url.Parse(*cleaned)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, invalid("sourceUrl must be an http or https URL")
	}
	return cleaned, nil
}

func (h *Handler) ListRecipes(c fiber.Ctx, params generated.ListRecipesParams) error {
	limit, err := pageLimit(params.Limit)
	if err != nil {
		return err
	}
	query := store.ListRecipesParams{PageLimit: int32(limit + 1), TitleWords: []string{}}
	if params.Q != nil {
		if normalized := domain.Normalize(*params.Q); normalized != "" {
			query.TitleWords = strings.Fields(normalized)
		}
	}
	if params.Tag != nil && domain.Normalize(*params.Tag) != "" {
		query.Tag = pointer(domain.Normalize(*params.Tag))
	}
	if params.Ingredient != nil && domain.Normalize(*params.Ingredient) != "" {
		query.Ingredient = pointer(domain.Normalize(*params.Ingredient))
	}
	if params.Cursor != nil {
		var position recipeCursor
		id, err := decodeCursor(*params.Cursor, &position)
		if err != nil {
			return err
		}
		if position.Title == "" {
			return invalidCursor()
		}
		query.AfterTitle, query.AfterID = &position.Title, id
	}
	ctx := c.Context()
	recipes, err := h.queries.ListRecipes(ctx, query)
	if err != nil {
		return internal(err)
	}
	page := recipes[:min(len(recipes), limit)]
	ingredients, err := ingredientsOf(ctx, h.queries, page...)
	if err != nil {
		return internal(err)
	}
	result := generated.RecipeList{Items: make([]generated.Recipe, 0, len(page))}
	for _, recipe := range page {
		result.Items = append(result.Items, recipeView(recipe, ingredients[recipe.ID.Bytes]))
	}
	if len(recipes) > limit {
		last := page[len(page)-1]
		result.NextCursor = pointer(encodeCursor(recipeCursor{Title: last.NormalizedTitle, ID: uuid(last.ID).String()}))
	}
	return c.JSON(result)
}

func (h *Handler) GetRecipe(c fiber.Ctx, recipeID generated.RecipeId) error {
	ctx := c.Context()
	recipe, err := h.queries.GetRecipe(ctx, pgUUID(recipeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound("Recipe")
	}
	if err != nil {
		return internal(err)
	}
	ingredients, err := ingredientsOf(ctx, h.queries, recipe)
	if err != nil {
		return internal(err)
	}
	return c.JSON(recipeView(recipe, ingredients[recipe.ID.Bytes]))
}

// writeIngredients replaces a recipe's ingredients.
func writeIngredients(ctx context.Context, q *store.Queries, recipeID pgtype.UUID, ingredients []store.InsertRecipeIngredientParams) ([]store.RecipeIngredient, error) {
	if err := q.DeleteRecipeIngredients(ctx, recipeID); err != nil {
		return nil, err
	}
	out := make([]store.RecipeIngredient, 0, len(ingredients))
	for _, ingredient := range ingredients {
		ingredient.RecipeID = recipeID
		if err := q.InsertRecipeIngredient(ctx, ingredient); err != nil {
			return nil, err
		}
		out = append(out, store.RecipeIngredient(ingredient))
	}
	return out, nil
}

func (h *Handler) CreateRecipe(c fiber.Ctx, _ generated.CreateRecipeParams) error {
	var body generated.CreateRecipeRequest
	if _, err := decodeObject(c.Body(), &body); err != nil {
		return invalid(err.Error())
	}
	var input recipeInput
	var err error
	if input.title, input.normalized, err = cleanName("title", body.Title, titleLimit); err != nil {
		return err
	}
	if input.servings, err = checkServings(body.Servings); err != nil {
		return err
	}
	if body.Ingredients != nil {
		if input.ingredients, err = checkIngredients(*body.Ingredients); err != nil {
			return err
		}
	}
	if input.steps, err = checkSteps(body.Steps); err != nil {
		return err
	}
	input.tags = []string{}
	if body.Tags != nil {
		if input.tags, err = checkTags(*body.Tags); err != nil {
			return err
		}
	}
	if input.sourceURL, err = checkSourceURL(body.SourceUrl); err != nil {
		return err
	}
	if input.note, err = cleanText("note", body.Note, noteLimit); err != nil {
		return err
	}
	if body.Methods != nil {
		if input.methods, err = checkMethods(*body.Methods); err != nil {
			return err
		}
	}
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		recipe, err := q.InsertRecipe(ctx, store.InsertRecipeParams{Title: input.title, NormalizedTitle: input.normalized, Servings: input.servings,
			Steps: input.steps, Tags: input.tags, SourceUrl: input.sourceURL, Note: input.note, Methods: input.methods, Actor: actor})
		if uniqueViolation(err, recipeTitleKey) {
			return result{}, recipeExists(input.title)
		}
		if err != nil {
			return result{}, err
		}
		ingredients, err := writeIngredients(ctx, q, recipe.ID, input.ingredients)
		if err != nil {
			return result{}, err
		}
		return result{status: fiber.StatusCreated, body: recipeView(recipe, ingredients),
			events: []activity.Event{recipeEvent(recipeCreated, recipe, len(ingredients), nil)}}, nil
	})
}

// recipeFields are the UpdateRecipeRequest properties; nullable ones may be
// null.
var (
	recipeFields   = []string{"title", "servings", "ingredients", "steps", "tags", "sourceUrl", "note", "methods"}
	recipeNullable = map[string]bool{"servings": true, "steps": true, "sourceUrl": true, "note": true, "methods": true}
)

func sameIngredients(current []store.RecipeIngredient, next []store.InsertRecipeIngredientParams) bool {
	if len(current) != len(next) {
		return false
	}
	for index, ingredient := range current {
		other := next[index]
		if ingredient.Name != other.Name || !sameText(ingredient.Amount, other.Amount) || !sameText(ingredient.Note, other.Note) {
			return false
		}
	}
	return true
}

func sameServings(a, b *int32) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func (h *Handler) UpdateRecipe(c fiber.Ctx, recipeID generated.RecipeId, _ generated.UpdateRecipeParams) error {
	var body generated.UpdateRecipeRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	present := map[string]bool{}
	for _, field := range recipeFields {
		if _, ok := properties[field]; ok {
			if !recipeNullable[field] && isNull(properties, field) {
				return invalid(field + " cannot be null")
			}
			present[field] = true
		}
	}
	if len(present) == 0 {
		return invalid("the update names no recipe field")
	}
	var input recipeInput
	if present["title"] {
		if input.title, input.normalized, err = cleanName("title", *body.Title, titleLimit); err != nil {
			return err
		}
	}
	if input.servings, err = checkServings(body.Servings); err != nil {
		return err
	}
	if present["ingredients"] {
		if input.ingredients, err = checkIngredients(*body.Ingredients); err != nil {
			return err
		}
	}
	if input.steps, err = checkSteps(body.Steps); err != nil {
		return err
	}
	if present["tags"] {
		if input.tags, err = checkTags(*body.Tags); err != nil {
			return err
		}
	}
	if input.sourceURL, err = checkSourceURL(body.SourceUrl); err != nil {
		return err
	}
	if input.note, err = cleanText("note", body.Note, noteLimit); err != nil {
		return err
	}
	if body.Methods != nil {
		if input.methods, err = checkMethods(*body.Methods); err != nil {
			return err
		}
	}
	actor := requestActor(c)
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		current, err := q.GetRecipeForUpdate(ctx, pgUUID(recipeID))
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, notFound("Recipe")
		}
		if err != nil {
			return result{}, err
		}
		stored, err := ingredientsOf(ctx, q, current)
		if err != nil {
			return result{}, err
		}
		ingredients := stored[current.ID.Bytes]
		next := store.UpdateRecipeParams{ID: current.ID, Title: current.Title, NormalizedTitle: current.NormalizedTitle, Servings: current.Servings,
			Steps: current.Steps, Tags: current.Tags, SourceUrl: current.SourceUrl, Note: current.Note, Methods: current.Methods, Actor: actor}
		var changes []string
		if present["title"] && input.title != current.Title {
			next.Title, next.NormalizedTitle = input.title, input.normalized
			changes = append(changes, "title")
		}
		if present["servings"] && !sameServings(input.servings, current.Servings) {
			next.Servings = input.servings
			changes = append(changes, "servings")
		}
		replace := present["ingredients"] && !sameIngredients(ingredients, input.ingredients)
		if replace {
			changes = append(changes, "ingredients")
		}
		if present["steps"] && !sameText(input.steps, current.Steps) {
			next.Steps = input.steps
			changes = append(changes, "steps")
		}
		if present["tags"] && !slices.Equal(input.tags, current.Tags) {
			next.Tags = input.tags
			changes = append(changes, "tags")
		}
		if present["sourceUrl"] && !sameText(input.sourceURL, current.SourceUrl) {
			next.SourceUrl = input.sourceURL
			changes = append(changes, "sourceUrl")
		}
		if present["note"] && !sameText(input.note, current.Note) {
			next.Note = input.note
			changes = append(changes, "note")
		}
		if present["methods"] && ((input.methods == nil) != (current.Methods == nil) || !slices.Equal(input.methods, current.Methods)) {
			next.Methods = input.methods
			changes = append(changes, "methods")
		}
		if len(changes) == 0 {
			return result{status: fiber.StatusOK, body: recipeView(current, ingredients)}, nil
		}
		if next.Tags == nil {
			next.Tags = []string{}
		}
		recipe, err := q.UpdateRecipe(ctx, next)
		if uniqueViolation(err, recipeTitleKey) {
			return result{}, recipeExists(input.title)
		}
		if err != nil {
			return result{}, err
		}
		if replace {
			if ingredients, err = writeIngredients(ctx, q, recipe.ID, input.ingredients); err != nil {
				return result{}, err
			}
		}
		if recipe.Title != current.Title {
			// The meal plan names the recipe by its title.
			if err := q.RenameMealPlanRecipe(ctx, store.RenameMealPlanRecipeParams{Title: recipe.Title, RecipeID: recipe.ID}); err != nil {
				return result{}, err
			}
		}
		return result{status: fiber.StatusOK, body: recipeView(recipe, ingredients),
			events: []activity.Event{recipeEvent(recipeUpdated, recipe, len(ingredients), changes)}}, nil
	})
}

// DeleteRecipe deletes a recipe; meal plan entries that named it stay, as
// plain text with its title (the foreign key sets their recipe_id to NULL).
func (h *Handler) DeleteRecipe(c fiber.Ctx, recipeID generated.RecipeId, _ generated.DeleteRecipeParams) error {
	return h.write(c, func(ctx context.Context, q *store.Queries) (result, error) {
		stored, err := q.GetRecipeForUpdate(ctx, pgUUID(recipeID))
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, notFound("Recipe")
		}
		if err != nil {
			return result{}, err
		}
		ingredients, err := ingredientsOf(ctx, q, stored)
		if err != nil {
			return result{}, err
		}
		recipe, err := q.DeleteRecipe(ctx, stored.ID)
		if err != nil {
			return result{}, err
		}
		return result{status: fiber.StatusNoContent,
			events: []activity.Event{recipeEvent(recipeDeleted, recipe, len(ingredients[stored.ID.Bytes]), nil)}}, nil
	})
}
