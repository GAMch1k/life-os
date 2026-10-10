-- name: InsertDelivery :one
INSERT INTO deliveries (notification_id, channel, next_attempt_at)
VALUES ($1, $2, now())
RETURNING *;

-- name: ListDeliveries :many
SELECT * FROM deliveries
WHERE notification_id = ANY(sqlc.arg(notification_ids)::uuid[])
ORDER BY notification_id, channel;

-- name: ClaimDelivery :one
-- Takes the most overdue delivery that no live lease holds. An expired lease
-- (a worker that stopped mid-send) is claimable again; the attempt it started
-- still counts.
WITH candidate AS (
  SELECT id FROM deliveries
  WHERE status IN ('pending', 'retrying') AND next_attempt_at <= now()
    AND (lease_expires_at IS NULL OR lease_expires_at <= now())
  ORDER BY next_attempt_at, created_at
  FOR UPDATE SKIP LOCKED
  LIMIT 1
)
UPDATE deliveries SET attempts = attempts + 1, lease_owner = sqlc.arg(lease_owner)::text,
  lease_expires_at = now() + (sqlc.arg(lease_seconds)::integer * INTERVAL '1 second'), updated_at = now()
WHERE id = (SELECT id FROM candidate)
RETURNING *;

-- name: MarkDeliverySent :one
UPDATE deliveries SET status = 'sent', sent_at = now(), next_attempt_at = NULL,
  lease_owner = NULL, lease_expires_at = NULL, updated_at = now()
WHERE id = $1 AND lease_owner = sqlc.arg(lease_owner)::text AND status IN ('pending', 'retrying')
RETURNING *;

-- name: RetryDelivery :one
UPDATE deliveries SET status = 'retrying', last_error = $2, next_attempt_at = $3,
  lease_owner = NULL, lease_expires_at = NULL, updated_at = now()
WHERE id = $1 AND lease_owner = sqlc.arg(lease_owner)::text AND status IN ('pending', 'retrying')
RETURNING *;

-- name: FinishDelivery :one
-- Ends a claimed delivery without sending it: failed or skipped.
UPDATE deliveries SET status = sqlc.arg(status)::text, last_error = $2, next_attempt_at = NULL,
  lease_owner = NULL, lease_expires_at = NULL, updated_at = now()
WHERE id = $1 AND lease_owner = sqlc.arg(lease_owner)::text AND status IN ('pending', 'retrying')
RETURNING *;
