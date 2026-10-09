-- name: CreateTask :one
INSERT INTO tasks (title, notes, due_at, recurrence_frequency, recurrence_interval, recurrence_until, automation_idempotency_key, automation_payload_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (automation_idempotency_key) WHERE automation_idempotency_key IS NOT NULL DO UPDATE SET automation_idempotency_key = EXCLUDED.automation_idempotency_key
RETURNING *;

-- name: GetTask :one
SELECT * FROM tasks WHERE id = $1;

-- name: ListTasks :many
SELECT * FROM tasks
WHERE (sqlc.narg(completed_filter)::boolean IS NULL OR (completed_at IS NOT NULL) = sqlc.narg(completed_filter)::boolean)
  AND (
    sqlc.narg(due_before)::timestamptz IS NULL
    OR due_at <= sqlc.narg(due_before)::timestamptz
    OR (sqlc.arg(include_undated)::boolean AND due_at IS NULL)
  )
ORDER BY completed_at NULLS FIRST, due_at NULLS LAST, created_at DESC
LIMIT sqlc.arg(page_limit);

-- name: UpdateTask :one
UPDATE tasks
SET title = COALESCE(sqlc.narg(title), title), notes = COALESCE(sqlc.narg(notes), notes), due_at = COALESCE(sqlc.narg(due_at), due_at),
    recurrence_frequency = COALESCE(sqlc.narg(recurrence_frequency), recurrence_frequency), recurrence_interval = COALESCE(sqlc.narg(recurrence_interval), recurrence_interval), recurrence_until = COALESCE(sqlc.narg(recurrence_until), recurrence_until), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: CompleteTask :one
UPDATE tasks SET completed_at = COALESCE(completed_at, now()), updated_at = now()
WHERE id = $1
RETURNING *;
