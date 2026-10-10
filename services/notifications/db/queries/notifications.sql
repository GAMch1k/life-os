-- name: InsertNotification :one
-- A repeated dedupe key inserts nothing (no rows): the caller returns the
-- original instead.
INSERT INTO notifications (title, body, importance, category, source, link, dedupe_key, correlation_id, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (dedupe_key) DO NOTHING
RETURNING *;

-- name: GetNotification :one
SELECT * FROM notifications WHERE id = $1;

-- name: GetNotificationByDedupeKey :one
SELECT * FROM notifications WHERE dedupe_key = $1;

-- name: ListNotifications :many
SELECT * FROM notifications
WHERE (sqlc.narg(importance)::text IS NULL OR importance = sqlc.narg(importance)::text)
  AND (sqlc.narg(category)::text IS NULL OR category = sqlc.narg(category)::text
       OR starts_with(category, sqlc.narg(category)::text || '.'))
  AND (sqlc.narg(source)::text IS NULL OR source = sqlc.narg(source)::text)
  AND (sqlc.narg(unread)::boolean IS NULL OR (read_at IS NULL) = sqlc.narg(unread)::boolean)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (created_at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: CountUnreadNotifications :one
SELECT count(*)::integer AS count,
       (count(*) FILTER (WHERE importance = 'high'))::integer AS high_count
FROM notifications WHERE read_at IS NULL;

-- name: MarkNotificationRead :one
UPDATE notifications SET read_at = COALESCE(read_at, now()) WHERE id = $1 RETURNING *;

-- name: MarkAllNotificationsRead :execrows
UPDATE notifications SET read_at = now()
WHERE read_at IS NULL
  AND (sqlc.narg(before)::timestamptz IS NULL OR created_at <= sqlc.narg(before)::timestamptz);
