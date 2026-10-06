-- name: CreateTask :one
INSERT INTO tasks (title, notes, due_at, recurrence_frequency, recurrence_interval, recurrence_until)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetTask :one
SELECT * FROM tasks WHERE id = $1;

-- name: ListTasks :many
SELECT * FROM tasks
WHERE (sqlc.narg(completed)::timestamptz IS NULL OR (completed_at IS NOT NULL) = sqlc.arg(completed)::boolean)
  AND (sqlc.narg(due_before)::timestamptz IS NULL OR due_at <= sqlc.narg(due_before)::timestamptz)
ORDER BY completed_at NULLS FIRST, due_at NULLS LAST, created_at DESC
LIMIT sqlc.arg(page_limit);

-- name: UpdateTask :one
UPDATE tasks
SET title = COALESCE(sqlc.narg(title), title), notes = sqlc.narg(notes), due_at = sqlc.narg(due_at),
    recurrence_frequency = sqlc.narg(recurrence_frequency), recurrence_interval = sqlc.narg(recurrence_interval), recurrence_until = sqlc.narg(recurrence_until), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: CompleteTask :one
UPDATE tasks SET completed_at = COALESCE(completed_at, now()), updated_at = now()
WHERE id = $1
RETURNING *;
