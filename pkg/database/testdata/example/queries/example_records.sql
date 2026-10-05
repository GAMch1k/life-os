-- name: CreateExampleRecord :one
INSERT INTO example_records (label)
VALUES ($1)
RETURNING id, label, created_at;

-- name: GetExampleRecord :one
SELECT id, label, created_at
FROM example_records
WHERE id = $1;
