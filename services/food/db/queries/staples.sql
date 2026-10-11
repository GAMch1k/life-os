-- name: ListStaples :many
-- Every staple by name, with whether it is needed on the list and whether
-- some of it is in stock at home.
SELECT s.*,
    EXISTS (SELECT 1 FROM shopping_items i WHERE i.state = 'needed' AND i.normalized_name = s.normalized_name) AS on_list,
    EXISTS (SELECT 1 FROM pantry_items p WHERE p.state = 'in_stock' AND p.normalized_name = s.normalized_name) AS in_stock,
    EXISTS (SELECT 1 FROM pantry_items p WHERE p.state = 'in_stock' AND p.running_low_at IS NOT NULL AND p.normalized_name = s.normalized_name) AS running_low
FROM staples s
ORDER BY s.normalized_name, s.id;

-- name: GetStaple :one
SELECT * FROM staples WHERE id = @id;

-- name: GetStapleForUpdate :one
SELECT * FROM staples WHERE id = @id FOR UPDATE;

-- name: GetStapleByName :one
SELECT * FROM staples WHERE normalized_name = @normalized_name;

-- name: InsertStaple :one
INSERT INTO staples (name, normalized_name, amount, store_id, aisle, created_by, updated_by)
VALUES (@name, @normalized_name, sqlc.narg(amount), sqlc.narg(store_id), sqlc.narg(aisle), @actor, @actor)
RETURNING *;

-- name: UpdateStaple :one
UPDATE staples
SET name = @name, normalized_name = @normalized_name, amount = sqlc.narg(amount), store_id = sqlc.narg(store_id),
    aisle = sqlc.narg(aisle), updated_at = now(), updated_by = @actor
WHERE id = @id
RETURNING *;

-- name: DeleteStaple :one
DELETE FROM staples WHERE id = @id RETURNING *;

-- name: SetPantryItemRunningLow :one
-- Flags an in-stock item as running low (running = true) or clears the flag.
UPDATE pantry_items
SET running_low_at = CASE WHEN @running::boolean THEN now() ELSE NULL END,
    updated_at = now(), updated_by = @actor
WHERE id = @id
RETURNING *;

-- name: ListInStockPantryItemsByName :many
-- The in-stock items with a normalised name, soonest expiry first.
SELECT * FROM pantry_items
WHERE state = 'in_stock' AND normalized_name = @normalized_name
ORDER BY COALESCE(expires_on, 'infinity'::date), id
FOR UPDATE;

-- name: ClearRunningLowByName :many
-- Clears the running-low flag of the items with a normalised name, as when
-- more of it comes home.
UPDATE pantry_items SET running_low_at = NULL, updated_at = now(), updated_by = @actor
WHERE normalized_name = @normalized_name AND running_low_at IS NOT NULL
RETURNING *;
