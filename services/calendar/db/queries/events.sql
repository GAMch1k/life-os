-- name: CreateEvent :one
INSERT INTO events (title, description, all_day, start_at, end_at, start_date, end_date, recurrence_frequency, recurrence_interval, recurrence_until, external_provider, external_id, automation_idempotency_key, automation_payload_hash)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (automation_idempotency_key) WHERE automation_idempotency_key IS NOT NULL DO UPDATE SET automation_idempotency_key = EXCLUDED.automation_idempotency_key RETURNING *;
-- name: GetEvent :one
SELECT * FROM events WHERE id = $1;
-- name: ListEvents :many
SELECT * FROM events WHERE (all_day AND start_date <= sqlc.arg(to_date)::date AND end_date >= sqlc.arg(from_date)::date) OR (NOT all_day AND start_at < sqlc.arg(to_time)::timestamptz AND end_at > sqlc.arg(from_time)::timestamptz) ORDER BY COALESCE(start_at, start_date::timestamptz) LIMIT sqlc.arg(page_limit);
-- name: UpdateEvent :one
UPDATE events SET title=COALESCE(sqlc.narg(title),title), description=COALESCE(sqlc.narg(description),description), all_day=COALESCE(sqlc.narg(all_day),all_day), start_at=COALESCE(sqlc.narg(start_at),start_at), end_at=COALESCE(sqlc.narg(end_at),end_at), start_date=COALESCE(sqlc.narg(start_date),start_date), end_date=COALESCE(sqlc.narg(end_date),end_date), recurrence_frequency=COALESCE(sqlc.narg(recurrence_frequency),recurrence_frequency), recurrence_interval=COALESCE(sqlc.narg(recurrence_interval),recurrence_interval), recurrence_until=COALESCE(sqlc.narg(recurrence_until),recurrence_until), updated_at=now() WHERE id=sqlc.arg(id) RETURNING *;
-- name: DeleteEvent :execrows
DELETE FROM events WHERE id = $1;
