-- name: GetPreferences :one
SELECT * FROM food_preferences WHERE id;

-- name: UpdatePreferences :one
UPDATE food_preferences
SET methods = @methods, dislikes = @dislikes, dietary_notes = sqlc.narg(dietary_notes), updated_at = now(), updated_by = @actor
WHERE id
RETURNING *;

-- name: ListAllRecipes :many
-- Every recipe, by normalised title, for ranking (at most row_limit).
SELECT * FROM recipes ORDER BY normalized_title, id LIMIT sqlc.arg(row_limit);

-- name: ListInStockPantryItems :many
SELECT * FROM pantry_items WHERE state = 'in_stock' ORDER BY COALESCE(expires_on, 'infinity'::date), normalized_name, id;

-- name: ListPlannedRecipes :many
-- The days recipes are planned from one day through another.
SELECT recipe_id, day, slot FROM meal_plan_entries
WHERE recipe_id IS NOT NULL AND day >= sqlc.arg(from_day)::date AND day <= sqlc.arg(through_day)::date
ORDER BY day, slot_rank;

-- name: ListBoughtNames :many
-- The normalised names bought or put away since a time: pantry items added
-- (put away from the list or directly), and bought shopping items.
SELECT DISTINCT normalized_name FROM (
    SELECT normalized_name FROM pantry_items WHERE created_at >= sqlc.arg(since)::timestamptz
    UNION ALL
    SELECT normalized_name FROM shopping_items WHERE state = 'bought' AND bought_at >= sqlc.arg(since)::timestamptz
) AS bought
ORDER BY normalized_name;

-- name: ListIdeas :many
-- Ideas in a state, newest first.
SELECT * FROM food_ideas WHERE state = @state ORDER BY created_at DESC, id LIMIT sqlc.arg(row_limit);

-- name: GetIdea :one
SELECT * FROM food_ideas WHERE id = @id;

-- name: GetIdeaForUpdate :one
SELECT * FROM food_ideas WHERE id = @id FOR UPDATE;

-- name: InsertIdea :one
INSERT INTO food_ideas (title, normalized_title, servings, ingredients, steps, tags, methods, source, source_url, reason, created_by, updated_by)
VALUES (@title, @normalized_title, sqlc.narg(servings), @ingredients, sqlc.narg(steps), @tags, @methods, @source, sqlc.narg(source_url), @reason, @actor, @actor)
RETURNING *;

-- name: SetIdeaState :one
UPDATE food_ideas SET state = @state, recipe_id = sqlc.narg(recipe_id), updated_at = now(), updated_by = @actor
WHERE id = @id
RETURNING *;

-- name: GetRecipeByTitle :one
SELECT * FROM recipes WHERE normalized_title = @normalized_title;
