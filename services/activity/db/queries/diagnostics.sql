-- name: RecordDiagnosticEvent :exec
INSERT INTO diagnostic_events (service, code, route, status, subject_id, request_id, count, duration_ms, first_at, last_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);
-- name: ListDiagnosticEvents :many
SELECT * FROM diagnostic_events
WHERE (sqlc.narg(service)::text IS NULL OR service = sqlc.narg(service)::text)
  AND (sqlc.narg(code)::text IS NULL OR code = sqlc.narg(code)::text)
  AND (sqlc.narg(last_from)::timestamptz IS NULL OR last_at >= sqlc.narg(last_from)::timestamptz)
  AND (sqlc.narg(last_to)::timestamptz IS NULL OR last_at < sqlc.narg(last_to)::timestamptz)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (last_at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::bigint))
ORDER BY last_at DESC, id DESC
LIMIT sqlc.arg(page_limit);
-- name: SummarizeDiagnosticEvents :many
SELECT service, code, sum(count)::bigint AS total, max(last_at)::timestamptz AS last_at
FROM diagnostic_events
WHERE last_at >= sqlc.arg(since)::timestamptz
GROUP BY service, code
ORDER BY total DESC, service, code;
-- name: DeleteDiagnosticEventsBefore :execrows
DELETE FROM diagnostic_events WHERE last_at < sqlc.arg(before)::timestamptz;
