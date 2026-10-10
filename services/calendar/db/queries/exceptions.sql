-- name: ListEventExceptions :many
-- The occurrence exceptions of the given series.
SELECT * FROM event_exceptions
WHERE event_id = ANY(sqlc.arg(event_ids)::uuid[])
ORDER BY event_id, recurrence_id;

-- name: GetEventException :one
SELECT * FROM event_exceptions WHERE event_id = $1 AND recurrence_id = $2;

-- name: UpsertEventException :one
-- Writes one occurrence's changes and skip, replacing any it had.
INSERT INTO event_exceptions (event_id, recurrence_id, changes, cancelled_at)
VALUES (sqlc.arg(event_id), sqlc.arg(recurrence_id), sqlc.arg(changes), sqlc.narg(cancelled_at))
ON CONFLICT (event_id, recurrence_id) DO UPDATE
SET changes = EXCLUDED.changes, cancelled_at = EXCLUDED.cancelled_at, updated_at = now()
RETURNING *;

-- name: DeleteEventExceptions :execrows
DELETE FROM event_exceptions
WHERE event_id = sqlc.arg(event_id) AND recurrence_id = ANY(sqlc.arg(recurrence_ids)::text[]);

-- name: MoveEventExceptions :execrows
-- Hands occurrence exceptions to another series (a split's new series).
UPDATE event_exceptions
SET event_id = sqlc.arg(to_event_id), updated_at = now()
WHERE event_id = sqlc.arg(from_event_id) AND recurrence_id = ANY(sqlc.arg(recurrence_ids)::text[]);
