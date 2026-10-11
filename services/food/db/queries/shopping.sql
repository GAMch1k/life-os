-- name: ListShoppingItems :many
-- Open (needed) items first, by aisle, then name; then bought items, the
-- most recently bought first. The after_* arguments are the previous page's
-- last item: its state and, for an open item, its aisle rank and name, for a
-- bought one its bought time.
SELECT * FROM shopping_items
WHERE (sqlc.narg(state)::text IS NULL OR state = sqlc.narg(state)::text)
  AND (sqlc.narg(store_id)::uuid IS NULL OR store_id = sqlc.narg(store_id)::uuid)
  AND (sqlc.narg(aisle)::text IS NULL OR aisle = sqlc.narg(aisle)::text)
  AND (
    sqlc.narg(after_state)::text IS NULL
    OR (sqlc.narg(after_state)::text = 'needed' AND (state = 'bought'
        OR (aisle_rank, normalized_name, id) > (sqlc.narg(after_rank)::smallint, sqlc.narg(after_name)::text, sqlc.narg(after_id)::uuid)))
    OR (sqlc.narg(after_state)::text = 'bought' AND state = 'bought'
        AND (bought_at, id) < (sqlc.narg(after_bought_at)::timestamptz, sqlc.narg(after_id)::uuid))
  )
ORDER BY state = 'bought',
  CASE WHEN state = 'needed' THEN aisle_rank END,
  CASE WHEN state = 'needed' THEN normalized_name END,
  CASE WHEN state = 'needed' THEN id END,
  bought_at DESC,
  id DESC
LIMIT sqlc.arg(page_limit);

-- name: GetShoppingItem :one
SELECT * FROM shopping_items WHERE id = @id;

-- name: GetShoppingItemForUpdate :one
SELECT * FROM shopping_items WHERE id = @id FOR UPDATE;

-- name: GetOpenShoppingItemByName :one
SELECT * FROM shopping_items WHERE normalized_name = @normalized_name AND state = 'needed' FOR UPDATE;

-- name: AddShoppingItem :one
-- Adds an item, or merges it into the open item with the same normalised
-- name: the amount, note, and store given replace the open item's, the others
-- stay, and an aisle a request named replaces the open item's. The caller
-- chooses the new row's id, so an id other than @id means a merge.
INSERT INTO shopping_items (id, name, normalized_name, amount, note, aisle, aisle_set_by, store_id, source, source_ref, created_by, updated_by)
VALUES (@id, @name, @normalized_name, sqlc.narg(amount), sqlc.narg(note), @aisle, @aisle_set_by, sqlc.narg(store_id), @source, sqlc.narg(source_ref), @actor, @actor)
ON CONFLICT (normalized_name) WHERE state = 'needed' DO UPDATE SET
    amount = COALESCE(EXCLUDED.amount, shopping_items.amount),
    note = COALESCE(EXCLUDED.note, shopping_items.note),
    store_id = COALESCE(EXCLUDED.store_id, shopping_items.store_id),
    aisle = CASE WHEN EXCLUDED.aisle_set_by = 'manual' THEN EXCLUDED.aisle ELSE shopping_items.aisle END,
    aisle_set_by = CASE WHEN EXCLUDED.aisle_set_by = 'manual' THEN 'manual' ELSE shopping_items.aisle_set_by END,
    updated_at = now(),
    updated_by = EXCLUDED.updated_by
RETURNING *;

-- name: UpdateShoppingItem :one
UPDATE shopping_items
SET name = @name, normalized_name = @normalized_name, amount = sqlc.narg(amount), note = sqlc.narg(note),
    aisle = @aisle, aisle_set_by = @aisle_set_by, store_id = sqlc.narg(store_id),
    updated_at = now(), updated_by = @actor
WHERE id = @id
RETURNING *;

-- name: MarkShoppingItemBought :one
UPDATE shopping_items
SET state = 'bought', bought_at = now(), updated_at = now(), updated_by = @actor
WHERE id = @id
RETURNING *;

-- name: MarkShoppingItemNeeded :one
UPDATE shopping_items
SET state = 'needed', bought_at = NULL, updated_at = now(), updated_by = @actor
WHERE id = @id
RETURNING *;

-- name: DeleteShoppingItem :one
DELETE FROM shopping_items WHERE id = @id RETURNING *;

-- name: ClearBoughtShoppingItems :execrows
DELETE FROM shopping_items
WHERE state = 'bought' AND (sqlc.narg(bought_before)::timestamptz IS NULL OR bought_at < sqlc.narg(bought_before)::timestamptz);
