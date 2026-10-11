-- name: ListPantryItems :many
-- In-stock items first, soonest expiry first (no expiry last), then name;
-- then ended items, the most recently ended first. expires_by keeps in-stock
-- items that expire on or before that date (already expired ones too). The
-- after_* arguments are the previous page's last item: whether it was in
-- stock and, if so, its expiry (infinity for none) and name, else its end.
SELECT * FROM pantry_items
WHERE (sqlc.narg(state)::text IS NULL OR state = sqlc.narg(state)::text)
  AND (sqlc.narg(location)::text IS NULL OR location = sqlc.narg(location)::text)
  AND (sqlc.narg(expires_by)::date IS NULL OR (state = 'in_stock' AND expires_on <= sqlc.narg(expires_by)::date))
  AND (sqlc.narg(running_low)::boolean IS NULL OR (running_low_at IS NOT NULL) = sqlc.narg(running_low)::boolean)
  AND (
    sqlc.narg(after_in_stock)::boolean IS NULL
    OR (sqlc.narg(after_in_stock)::boolean AND (state <> 'in_stock'
        OR (COALESCE(expires_on, 'infinity'::date), normalized_name, id) > (sqlc.narg(after_expires_on)::date, sqlc.narg(after_name)::text, sqlc.narg(after_id)::uuid)))
    OR (NOT sqlc.narg(after_in_stock)::boolean AND state <> 'in_stock'
        AND (ended_at, id) < (sqlc.narg(after_ended_at)::timestamptz, sqlc.narg(after_id)::uuid))
  )
ORDER BY state <> 'in_stock',
  CASE WHEN state = 'in_stock' THEN COALESCE(expires_on, 'infinity'::date) END,
  CASE WHEN state = 'in_stock' THEN normalized_name END,
  CASE WHEN state = 'in_stock' THEN id END,
  ended_at DESC,
  id DESC
LIMIT sqlc.arg(page_limit);

-- name: GetPantryItem :one
SELECT * FROM pantry_items WHERE id = @id;

-- name: GetPantryItemForUpdate :one
SELECT * FROM pantry_items WHERE id = @id FOR UPDATE;

-- name: InsertPantryItem :one
INSERT INTO pantry_items (name, normalized_name, amount, location, expires_on, opened_on, note, source, source_ref, from_shopping_item_id, created_by, updated_by)
VALUES (@name, @normalized_name, sqlc.narg(amount), @location, sqlc.narg(expires_on), sqlc.narg(opened_on), sqlc.narg(note), @source, sqlc.narg(source_ref), sqlc.narg(from_shopping_item_id), @actor, @actor)
RETURNING *;

-- name: UpdatePantryItem :one
UPDATE pantry_items
SET name = @name, normalized_name = @normalized_name, amount = sqlc.narg(amount), location = @location,
    expires_on = sqlc.narg(expires_on), opened_on = sqlc.narg(opened_on), note = sqlc.narg(note),
    updated_at = now(), updated_by = @actor
WHERE id = @id
RETURNING *;

-- name: SetPantryItemState :one
-- in_stock clears ended_at; an ended state keeps an earlier end when the item
-- had already ended (used up, then marked thrown away instead). A change of
-- state clears the running-low flag.
UPDATE pantry_items
SET state = @state,
    ended_at = CASE WHEN @state::text = 'in_stock' THEN NULL ELSE COALESCE(ended_at, now()) END,
    running_low_at = NULL,
    updated_at = now(), updated_by = @actor
WHERE id = @id
RETURNING *;

-- name: DeletePantryItem :one
DELETE FROM pantry_items WHERE id = @id RETURNING *;
