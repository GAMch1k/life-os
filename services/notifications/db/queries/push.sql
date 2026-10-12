-- name: UpsertPushSubscription :one
-- Subscribing is idempotent by endpoint: a known endpoint gets the keys and
-- label it was sent with and keeps its id, creation time, and history.
-- created is true when the row is new.
INSERT INTO push_subscriptions (endpoint, p256dh, auth, device_label)
VALUES ($1, $2, $3, $4)
ON CONFLICT (endpoint) DO UPDATE SET p256dh = EXCLUDED.p256dh, auth = EXCLUDED.auth,
  device_label = EXCLUDED.device_label, updated_at = now()
RETURNING id, endpoint, p256dh, auth, device_label, created_at, updated_at,
  last_success_at, last_failure_at, last_error, (xmax = 0)::boolean AS created;

-- name: ListPushSubscriptions :many
SELECT * FROM push_subscriptions ORDER BY created_at, id;

-- name: GetPushSubscription :one
SELECT * FROM push_subscriptions WHERE id = $1;

-- name: DeletePushSubscription :execrows
DELETE FROM push_subscriptions WHERE id = $1;

-- name: ListPushTargets :many
-- The devices a notification still has to reach: subscribed by the time it
-- was created (a device added later does not get older notifications) and
-- without an outcome for it yet.
SELECT s.* FROM push_subscriptions s
WHERE s.created_at <= sqlc.arg(created_before)::timestamptz
  AND NOT EXISTS (
    SELECT 1 FROM push_outcomes o
    WHERE o.notification_id = sqlc.arg(notification_id)::uuid AND o.subscription_id = s.id)
ORDER BY s.created_at, s.id;

-- name: RecordPushSent :exec
-- A device accepted the notification: its final outcome, and the device's
-- latest success. Nothing when the device was removed meanwhile.
WITH device AS (
  UPDATE push_subscriptions SET last_success_at = now()
  WHERE id = sqlc.arg(subscription_id)::uuid
  RETURNING id
)
INSERT INTO push_outcomes (notification_id, subscription_id, status)
SELECT sqlc.arg(notification_id)::uuid, id, 'sent' FROM device
ON CONFLICT (notification_id, subscription_id) DO NOTHING;

-- name: RecordPushFailed :exec
-- The push service refused the notification for a device for good: its final
-- outcome, and the device's latest failure.
WITH device AS (
  UPDATE push_subscriptions SET last_failure_at = now(), last_error = sqlc.arg(error)::text
  WHERE id = sqlc.arg(subscription_id)::uuid
  RETURNING id
)
INSERT INTO push_outcomes (notification_id, subscription_id, status, error)
SELECT sqlc.arg(notification_id)::uuid, id, 'failed', sqlc.arg(error)::text FROM device
ON CONFLICT (notification_id, subscription_id) DO NOTHING;

-- name: MarkPushSuccess :exec
-- A push to the device was accepted (a test push).
UPDATE push_subscriptions SET last_success_at = now() WHERE id = $1;

-- name: MarkPushFailure :exec
-- A push to the device failed: for now (it is tried again), or a test push.
UPDATE push_subscriptions SET last_failure_at = now(), last_error = sqlc.arg(error)::text WHERE id = $1;

-- name: CountPushOutcomes :one
-- How many devices a notification reached, and how many refused it for good.
SELECT (count(*) FILTER (WHERE status = 'sent'))::integer AS sent,
       (count(*) FILTER (WHERE status = 'failed'))::integer AS failed
FROM push_outcomes WHERE notification_id = $1;
