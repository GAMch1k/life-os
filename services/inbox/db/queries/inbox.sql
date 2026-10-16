-- name: CreateInboxItem :one
INSERT INTO inbox_items (content, source, source_metadata, media_references, source_dedupe_key, correlation_id) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (source_dedupe_key) WHERE source_dedupe_key IS NOT NULL DO UPDATE SET updated_at = inbox_items.updated_at
RETURNING *;
-- name: GetInboxItem :one
SELECT * FROM inbox_items WHERE id = $1;
-- name: ListInboxItems :many
SELECT * FROM inbox_items
WHERE (sqlc.narg(unresolved_filter)::boolean IS NULL OR (state IN ('raw', 'processing', 'human_review', 'failed')) = sqlc.narg(unresolved_filter)::boolean)
  AND (sqlc.narg(state_filter)::text IS NULL OR state = sqlc.narg(state_filter)::text)
  -- due: only items without a scheduled retry, or whose retry time has come.
  AND (sqlc.narg(due_filter)::boolean IS NOT TRUE OR next_retry_at IS NULL OR next_retry_at <= now())
-- Newest first, or with oldest_first the earliest first (the processor's
-- order, so a backlog is planned as it was captured).
ORDER BY CASE WHEN sqlc.narg(oldest_first)::boolean IS TRUE THEN created_at END ASC,
         created_at DESC
LIMIT sqlc.arg(page_limit);
-- name: SetInboxItemState :one
-- Any state change other than a scheduled retry clears the retry schedule.
UPDATE inbox_items SET state = $1, resolved_at = CASE WHEN $1 IN ('resolved', 'discarded') THEN now() ELSE resolved_at END, next_retry_at = NULL, retry_reason = NULL, updated_at = now() WHERE id = $2 RETURNING *;
-- name: ScheduleInboxItemRetry :one
UPDATE inbox_items SET state = 'raw', next_retry_at = sqlc.narg(next_retry_at), retry_reason = sqlc.narg(retry_reason), updated_at = now() WHERE id = sqlc.arg(id) RETURNING *;
-- name: SetInboxItemHumanReview :one
UPDATE inbox_items SET state = 'human_review', processing_metadata = $1, next_retry_at = NULL, retry_reason = NULL, updated_at = now() WHERE id = $2 RETURNING *;
-- name: ResolveInboxItemHumanReview :one
UPDATE inbox_items SET state = 'raw', processing_metadata = $1, updated_at = now() WHERE id = $2 AND state = 'human_review' RETURNING *;
-- name: ListProcessingAttempts :many
SELECT * FROM inbox_processing_attempts WHERE inbox_item_id = $1 ORDER BY created_at DESC;
-- name: CreateProcessingAttempt :one
INSERT INTO inbox_processing_attempts (inbox_item_id, idempotency_key, status, agent_name, model_profile, actions) VALUES ($1,$2,'pending',$3,$4,$5) RETURNING *;
-- name: GetProcessingAttemptForItem :one
SELECT * FROM inbox_processing_attempts WHERE id = $1 AND inbox_item_id = $2;
-- name: UpdateProcessingAttempt :one
UPDATE inbox_processing_attempts SET status=sqlc.arg(status), actions=sqlc.arg(actions), error_summary=sqlc.narg(error_summary), next_retry_at=sqlc.narg(next_retry_at),
  started_at=CASE WHEN sqlc.arg(status)='processing' THEN now() ELSE started_at END,
  completed_at=CASE WHEN sqlc.arg(status) IN ('human_review','processed','failed','retryable') THEN now() ELSE completed_at END,
  updated_at=now()
WHERE id=sqlc.arg(id) RETURNING *;

-- name: GetInboxBacklog :one
SELECT
    count(*) FILTER (WHERE i.state IN ('raw', 'processing'))::int AS pending_count,
    count(*) FILTER (WHERE i.state IN ('raw', 'processing') AND failed.item_id IS NOT NULL)::int AS retrying_count,
    count(*) FILTER (WHERE i.state = 'human_review')::int AS human_review_count,
    (min(i.created_at) FILTER (WHERE i.state IN ('raw', 'processing')))::timestamptz AS oldest_pending_at,
    count(*) FILTER (WHERE i.state = 'failed')::int AS failed_count,
    (min(i.next_retry_at) FILTER (WHERE i.state = 'raw' AND i.next_retry_at > now()))::timestamptz AS next_retry_at,
    COALESCE((array_agg(i.retry_reason ORDER BY i.next_retry_at) FILTER (WHERE i.state = 'raw' AND i.next_retry_at > now()))[1], '')::text AS next_retry_reason
FROM inbox_items i
LEFT JOIN LATERAL (
    SELECT a.inbox_item_id AS item_id FROM inbox_processing_attempts a
    WHERE a.inbox_item_id = i.id AND a.status IN ('retryable', 'failed') LIMIT 1
) failed ON true
WHERE i.state IN ('raw', 'processing', 'human_review', 'failed');

-- name: GetLatestPendingFailure :one
SELECT a.error_summary, a.updated_at
FROM inbox_items i
JOIN inbox_processing_attempts a ON a.inbox_item_id = i.id
WHERE i.state IN ('raw', 'processing', 'failed') AND a.status IN ('retryable', 'failed')
ORDER BY a.updated_at DESC
LIMIT 1;

-- name: RecordAttemptRuntime :one
UPDATE inbox_processing_attempts
SET runtime_state = sqlc.arg(runtime_state), runtime_owner = sqlc.arg(runtime_owner),
    runtime_session_id = COALESCE(sqlc.narg(runtime_session_id), runtime_session_id),
    runtime_heartbeat_at = now(), updated_at = now()
WHERE id = sqlc.arg(id) AND inbox_item_id = sqlc.arg(inbox_item_id) AND status IN ('pending', 'processing')
RETURNING *;

-- name: ListStaleProcessingAttempts :many
-- Unfinished attempts whose runtime stopped reporting (or never did), for
-- captures that are still waiting for processing. An open attempt of a capture
-- that has since been resolved, discarded, or sent to review is history.
SELECT a.* FROM inbox_processing_attempts a
JOIN inbox_items i ON i.id = a.inbox_item_id
WHERE a.status IN ('pending', 'processing')
  AND i.state IN ('raw', 'processing')
  AND COALESCE(a.runtime_heartbeat_at, a.updated_at) < now() - make_interval(secs => sqlc.arg(stale_after_seconds)::int)
ORDER BY a.created_at
LIMIT 50;

-- name: ListRecentCaptures :many
-- The captures made in a window before one capture (not that one), newest
-- first, that processing has settled: resolved, waiting for Human Review, or
-- failed. Each comes with its images and its latest finished attempt, if
-- any.
SELECT i.id, i.content, i.source, i.state, i.correlation_id, i.media_references, i.created_at, i.resolved_at,
       a.id AS attempt_id,
       COALESCE(a.status, '')::text AS attempt_status,
       COALESCE(a.actions, '[]'::jsonb)::jsonb AS attempt_actions,
       a.error_summary AS attempt_error_summary,
       a.completed_at AS attempt_completed_at
FROM inbox_items i
LEFT JOIN LATERAL (
    SELECT p.id, p.status, p.actions, p.error_summary, p.completed_at
    FROM inbox_processing_attempts p
    WHERE p.inbox_item_id = i.id AND p.status IN ('processed', 'human_review', 'failed')
    ORDER BY p.completed_at DESC NULLS LAST, p.created_at DESC
    LIMIT 1
) a ON true
WHERE i.id <> sqlc.arg(before_id)
  AND i.created_at >= sqlc.arg(window_start)
  AND i.created_at < sqlc.arg(window_end)
  AND i.state IN ('resolved', 'human_review', 'failed')
ORDER BY i.created_at DESC
LIMIT sqlc.arg(page_limit);

-- name: ListUnfinishedCaptures :many
-- The captures made in a window before one capture (not that one) that
-- processing has not finished yet: raw (new, or waiting for a retry) or
-- processing, oldest first. The processor plans a capture after them.
SELECT i.id, i.content, i.source, i.state, i.created_at, i.updated_at, i.next_retry_at
FROM inbox_items i
WHERE i.id <> sqlc.arg(before_id)
  AND i.created_at >= sqlc.arg(window_start)
  AND i.created_at < sqlc.arg(window_end)
  AND i.state IN ('raw', 'processing')
ORDER BY i.created_at
LIMIT sqlc.arg(page_limit);
