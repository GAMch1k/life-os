-- name: ListStores :many
SELECT * FROM stores
WHERE sqlc.arg(include_archived)::boolean OR archived_at IS NULL
ORDER BY sort_order, normalized_name, id;

-- name: GetStore :one
SELECT * FROM stores WHERE id = @id;

-- name: GetStoreForUpdate :one
SELECT * FROM stores WHERE id = @id FOR UPDATE;

-- name: GetStoreByName :one
SELECT * FROM stores WHERE normalized_name = @normalized_name;

-- name: InsertStore :one
-- A new store goes after the others unless it names its place. Nothing is
-- inserted (no row) when a store with the name already exists.
INSERT INTO stores (name, normalized_name, sort_order, created_by, updated_by)
VALUES (@name, @normalized_name,
        COALESCE(sqlc.narg(sort_order)::integer, (SELECT COALESCE(max(sort_order), 0) + 1 FROM stores)),
        @actor, @actor)
ON CONFLICT (normalized_name) DO NOTHING
RETURNING *;

-- name: UpdateStore :one
UPDATE stores
SET name = @name, normalized_name = @normalized_name, sort_order = @sort_order, archived_at = @archived_at,
    updated_at = now(), updated_by = @actor
WHERE id = @id
RETURNING *;
