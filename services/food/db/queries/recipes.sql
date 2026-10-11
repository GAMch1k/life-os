-- name: ListRecipes :many
-- Recipes by normalised title. title_words keeps recipes whose normalised
-- title contains every word (also inside a word); tag keeps those with the
-- tag; ingredient those with an ingredient whose normalised name contains
-- it. after_* are the previous page's last recipe.
SELECT * FROM recipes r
WHERE (sqlc.narg(tag)::text IS NULL OR sqlc.narg(tag)::text = ANY(r.tags))
  AND (cardinality(sqlc.arg(title_words)::text[]) = 0
       OR NOT EXISTS (SELECT 1 FROM unnest(sqlc.arg(title_words)::text[]) AS word
                      WHERE strpos(r.normalized_title, word) = 0))
  AND (sqlc.narg(ingredient)::text IS NULL
       OR EXISTS (SELECT 1 FROM recipe_ingredients i
                  WHERE i.recipe_id = r.id AND strpos(i.normalized_name, sqlc.narg(ingredient)::text) > 0))
  AND (sqlc.narg(after_title)::text IS NULL
       OR (r.normalized_title, r.id) > (sqlc.narg(after_title)::text, sqlc.narg(after_id)::uuid))
ORDER BY r.normalized_title, r.id
LIMIT sqlc.arg(page_limit);

-- name: GetRecipe :one
SELECT * FROM recipes WHERE id = @id;

-- name: GetRecipeForUpdate :one
SELECT * FROM recipes WHERE id = @id FOR UPDATE;

-- name: InsertRecipe :one
INSERT INTO recipes (title, normalized_title, servings, steps, tags, source_url, note, created_by, updated_by)
VALUES (@title, @normalized_title, sqlc.narg(servings), sqlc.narg(steps), @tags, sqlc.narg(source_url), sqlc.narg(note), @actor, @actor)
RETURNING *;

-- name: UpdateRecipe :one
UPDATE recipes
SET title = @title, normalized_title = @normalized_title, servings = sqlc.narg(servings), steps = sqlc.narg(steps),
    tags = @tags, source_url = sqlc.narg(source_url), note = sqlc.narg(note), updated_at = now(), updated_by = @actor
WHERE id = @id
RETURNING *;

-- name: TouchRecipe :one
-- Marks a recipe changed when only its ingredients did.
UPDATE recipes SET updated_at = now(), updated_by = @actor WHERE id = @id RETURNING *;

-- name: DeleteRecipe :one
DELETE FROM recipes WHERE id = @id RETURNING *;

-- name: ListRecipeIngredients :many
-- The ingredients of the given recipes, each recipe's in order.
SELECT * FROM recipe_ingredients WHERE recipe_id = ANY(sqlc.arg(recipe_ids)::uuid[]) ORDER BY recipe_id, position;

-- name: DeleteRecipeIngredients :exec
DELETE FROM recipe_ingredients WHERE recipe_id = @recipe_id;

-- name: InsertRecipeIngredient :exec
INSERT INTO recipe_ingredients (recipe_id, position, name, normalized_name, amount, note)
VALUES (@recipe_id, @position, @name, @normalized_name, sqlc.narg(amount), sqlc.narg(note));

-- name: RenameMealPlanRecipe :exec
-- Keeps the meal plan's title of a recipe in step with the recipe.
UPDATE meal_plan_entries SET title = @title WHERE recipe_id = @recipe_id;

-- name: ListMealPlan :many
-- The meal plan from one day through another, by day, then slot.
SELECT * FROM meal_plan_entries
WHERE day >= sqlc.arg(from_day)::date AND day <= sqlc.arg(through_day)::date
ORDER BY day, slot_rank;

-- name: GetMealPlanEntryForUpdate :one
SELECT * FROM meal_plan_entries WHERE day = @day AND slot = @slot FOR UPDATE;

-- name: UpsertMealPlanEntry :one
-- Sets a day's slot, replacing what it held (the entry keeps its id).
INSERT INTO meal_plan_entries (day, slot, recipe_id, title, servings, note, created_by, updated_by)
VALUES (@day, @slot, sqlc.narg(recipe_id), @title, sqlc.narg(servings), sqlc.narg(note), @actor, @actor)
ON CONFLICT (day, slot) DO UPDATE SET
    recipe_id = EXCLUDED.recipe_id, title = EXCLUDED.title, servings = EXCLUDED.servings, note = EXCLUDED.note,
    updated_at = now(), updated_by = EXCLUDED.updated_by
RETURNING *;

-- name: DeleteMealPlanEntry :one
DELETE FROM meal_plan_entries WHERE day = @day AND slot = @slot RETURNING *;

-- name: ListPlannedRecipeIDs :many
-- The recipes planned from one day through another, in plan order (a
-- recipe planned twice is listed twice).
SELECT recipe_id::uuid AS recipe_id FROM meal_plan_entries
WHERE recipe_id IS NOT NULL AND day >= sqlc.arg(from_day)::date AND day <= sqlc.arg(through_day)::date
ORDER BY day, slot_rank;

-- name: ListInStockNames :many
-- Which of the given normalised names are in stock in the pantry, with one
-- such item each (the one expiring soonest).
SELECT DISTINCT ON (normalized_name) * FROM pantry_items
WHERE state = 'in_stock' AND normalized_name = ANY(sqlc.arg(names)::text[])
ORDER BY normalized_name, COALESCE(expires_on, 'infinity'::date), id;

-- name: ListOpenShoppingItemsByName :many
-- The needed items with the given normalised names.
SELECT * FROM shopping_items WHERE state = 'needed' AND normalized_name = ANY(sqlc.arg(names)::text[]);
