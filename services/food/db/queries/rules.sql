-- name: ListAisleRules :many
SELECT * FROM aisle_rules
WHERE sqlc.narg(aisle)::text IS NULL OR aisle = sqlc.narg(aisle)::text
ORDER BY keyword, id;

-- name: InsertAisleRule :one
-- Nothing is inserted (no row) when the keyword already has a rule.
INSERT INTO aisle_rules (keyword, aisle, match, created_by)
VALUES (@keyword, @aisle, @match, @actor)
ON CONFLICT (keyword) DO NOTHING
RETURNING *;

-- name: DeleteAisleRule :one
DELETE FROM aisle_rules WHERE id = @id RETURNING *;
