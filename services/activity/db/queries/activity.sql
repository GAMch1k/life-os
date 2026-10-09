-- name: RecordActivityEvent :one
INSERT INTO activity_events (id, type, source, subject_type, subject_id, correlation_id, causation_id, actor_kind, actor_name, outcome, summary, data, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (id) DO NOTHING
RETURNING *;
-- name: GetActivityEvent :one
SELECT * FROM activity_events WHERE id = $1;
-- name: ListActivityEvents :many
SELECT * FROM activity_events
WHERE (sqlc.narg(correlation_id)::text IS NULL OR correlation_id = sqlc.narg(correlation_id)::text)
  AND (sqlc.narg(subject_type)::text IS NULL OR subject_type = sqlc.narg(subject_type)::text)
  AND (sqlc.narg(subject_id)::text IS NULL OR subject_id = sqlc.narg(subject_id)::text)
  AND (sqlc.narg(event_type)::text IS NULL OR type = sqlc.narg(event_type)::text)
  AND (sqlc.narg(source)::text IS NULL OR source = sqlc.narg(source)::text)
  AND (sqlc.narg(outcome)::text IS NULL OR outcome = sqlc.narg(outcome)::text)
  AND (sqlc.narg(occurred_from)::timestamptz IS NULL OR occurred_at >= sqlc.narg(occurred_from)::timestamptz)
  AND (sqlc.narg(occurred_to)::timestamptz IS NULL OR occurred_at < sqlc.narg(occurred_to)::timestamptz)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (occurred_at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY occurred_at DESC, id DESC
LIMIT sqlc.arg(page_limit);
