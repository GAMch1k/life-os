-- name: CreateInboxItem :one
INSERT INTO inbox_items (content, source, source_metadata, media_references, source_dedupe_key, correlation_id) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (source_dedupe_key) WHERE source_dedupe_key IS NOT NULL DO UPDATE SET updated_at = inbox_items.updated_at
RETURNING *;
-- name: GetInboxItem :one
SELECT * FROM inbox_items WHERE id = $1;
-- name: ListInboxItems :many
SELECT * FROM inbox_items
WHERE (sqlc.narg(unresolved_filter)::boolean IS NULL OR (state IN ('raw', 'processing', 'human_review')) = sqlc.narg(unresolved_filter)::boolean)
  AND (sqlc.narg(state_filter)::text IS NULL OR state = sqlc.narg(state_filter)::text)
ORDER BY created_at DESC LIMIT sqlc.arg(page_limit);
-- name: SetInboxItemState :one
UPDATE inbox_items SET state = $1, resolved_at = CASE WHEN $1 IN ('resolved', 'discarded') THEN now() ELSE resolved_at END, updated_at = now() WHERE id = $2 RETURNING *;
-- name: SetInboxItemHumanReview :one
UPDATE inbox_items SET state = 'human_review', processing_metadata = $1, updated_at = now() WHERE id = $2 RETURNING *;
-- name: ResolveInboxItemHumanReview :one
UPDATE inbox_items SET state = 'raw', processing_metadata = $1, updated_at = now() WHERE id = $2 AND state = 'human_review' RETURNING *;
-- name: ListProcessingAttempts :many
SELECT * FROM inbox_processing_attempts WHERE inbox_item_id = $1 ORDER BY created_at DESC;
-- name: CreateProcessingAttempt :one
INSERT INTO inbox_processing_attempts (inbox_item_id, idempotency_key, status, agent_name, model_profile, actions) VALUES ($1,$2,'pending',$3,$4,$5) RETURNING *;
-- name: GetProcessingAttemptForItem :one
SELECT * FROM inbox_processing_attempts WHERE id = $1 AND inbox_item_id = $2;
-- name: UpdateProcessingAttempt :one
UPDATE inbox_processing_attempts SET status=$2, actions=$3, error_summary=$4, started_at=CASE WHEN $2='processing' THEN now() ELSE started_at END, completed_at=CASE WHEN $2 IN ('human_review','processed','failed','retryable') THEN now() ELSE completed_at END, updated_at=now() WHERE id=$1 RETURNING *;

-- name: GetInboxBacklog :one
SELECT
    count(*) FILTER (WHERE i.state IN ('raw', 'processing'))::int AS pending_count,
    count(*) FILTER (WHERE i.state IN ('raw', 'processing') AND failed.item_id IS NOT NULL)::int AS retrying_count,
    count(*) FILTER (WHERE i.state = 'human_review')::int AS human_review_count,
    (min(i.created_at) FILTER (WHERE i.state IN ('raw', 'processing')))::timestamptz AS oldest_pending_at
FROM inbox_items i
LEFT JOIN LATERAL (
    SELECT a.inbox_item_id AS item_id FROM inbox_processing_attempts a
    WHERE a.inbox_item_id = i.id AND a.status IN ('retryable', 'failed') LIMIT 1
) failed ON true
WHERE i.state IN ('raw', 'processing', 'human_review');

-- name: GetLatestPendingFailure :one
SELECT a.error_summary, a.updated_at
FROM inbox_items i
JOIN inbox_processing_attempts a ON a.inbox_item_id = i.id
WHERE i.state IN ('raw', 'processing') AND a.status IN ('retryable', 'failed')
ORDER BY a.updated_at DESC
LIMIT 1;
