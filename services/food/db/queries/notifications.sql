-- name: ListPantryItemsExpiringBetween :many
-- In-stock items that expire from one date through another, soonest first,
-- then by name: what an expiry reminder names.
SELECT * FROM pantry_items
WHERE state = 'in_stock'
  AND expires_on >= sqlc.arg(from_date)::date
  AND expires_on <= sqlc.arg(through_date)::date
ORDER BY expires_on, normalized_name, created_at, id;

-- name: ClaimNotificationRun :one
-- Takes the dispatcher's lease until lease_until, creating the state row on
-- the first run. Returns no row while another process holds an unexpired
-- lease.
INSERT INTO food_notification_dispatch (id, lease_owner, lease_until, last_attempt_at, updated_at)
VALUES (true, sqlc.arg(owner)::text, sqlc.arg(lease_until)::timestamptz, sqlc.arg(now)::timestamptz, now())
ON CONFLICT (id) DO UPDATE
SET lease_owner = EXCLUDED.lease_owner, lease_until = EXCLUDED.lease_until,
    last_attempt_at = EXCLUDED.last_attempt_at, updated_at = now()
WHERE food_notification_dispatch.lease_until IS NULL
   OR food_notification_dispatch.lease_until <= sqlc.arg(now)::timestamptz
   OR food_notification_dispatch.lease_owner = sqlc.arg(owner)::text
RETURNING *;

-- name: FinishNotificationRun :execrows
-- Moves the cursor and releases the lease, if owner still holds it. A run
-- with an error keeps its message; one without records its success.
UPDATE food_notification_dispatch
SET last_run_at = sqlc.arg(last_run_at)::timestamptz,
    lease_owner = NULL, lease_until = NULL,
    last_success_at = CASE WHEN sqlc.narg(error)::text IS NULL THEN sqlc.arg(now)::timestamptz ELSE last_success_at END,
    last_error = COALESCE(sqlc.narg(error)::text, last_error),
    last_error_at = CASE WHEN sqlc.narg(error)::text IS NULL THEN last_error_at ELSE sqlc.arg(now)::timestamptz END,
    last_sent_at = COALESCE(sqlc.narg(sent_at)::timestamptz, last_sent_at),
    updated_at = now()
WHERE id AND lease_owner = sqlc.arg(owner)::text;

-- name: GetNotificationDispatch :one
SELECT * FROM food_notification_dispatch WHERE id;

-- name: ListHandledNotifications :many
-- Which of the given dedupe keys were already handled.
SELECT dedupe_key FROM food_notification_log WHERE dedupe_key = ANY(sqlc.arg(keys)::text[]);

-- name: RecordNotification :execrows
-- Records one reminder's outcome; 0 rows when it was already recorded.
INSERT INTO food_notification_log (dedupe_key, due_at, outcome, recorded_at)
VALUES (sqlc.arg(dedupe_key), sqlc.arg(due_at), sqlc.arg(outcome), sqlc.arg(recorded_at))
ON CONFLICT (dedupe_key) DO NOTHING;

-- name: PruneNotificationLog :execrows
DELETE FROM food_notification_log WHERE due_at < sqlc.arg(before)::timestamptz;
