-- name: CreateAudioJob :one
-- With an idempotency key already on a job (a repeated or concurrent upload),
-- nothing is inserted and that job is returned: its object_key is then not
-- the one given.
INSERT INTO audio_jobs (source, source_metadata, object_key, content_type, byte_size, expires_at, idempotency_key, request_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL DO UPDATE SET idempotency_key = EXCLUDED.idempotency_key
RETURNING *;
-- name: GetAudioJob :one
SELECT * FROM audio_jobs WHERE id = $1;
-- name: GetAudioJobByIdempotencyKey :one
SELECT * FROM audio_jobs WHERE idempotency_key = sqlc.arg(idempotency_key)::text;
-- name: ClaimAudioJob :one
WITH candidate AS (
  SELECT id FROM audio_jobs WHERE status IN ('queued','retryable') AND available_at <= now()
  ORDER BY available_at, created_at FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE audio_jobs SET status='processing', attempt_count=attempt_count+1, lease_owner=sqlc.arg(lease_owner),
  lease_expires_at=now() + (sqlc.arg(lease_seconds)::integer * INTERVAL '1 second'), last_heartbeat_at=now(), updated_at=now()
WHERE id=(SELECT id FROM candidate) RETURNING *;
-- name: CompleteAudioJob :one
UPDATE audio_jobs SET status='succeeded', transcript=$2, inbox_item_id=$3, error_summary=NULL, completed_at=now(),
  lease_owner=NULL, lease_expires_at=NULL, last_heartbeat_at=now(), updated_at=now()
WHERE id=$1 AND status='processing' AND inbox_item_id IS NULL AND lease_owner=$4 AND lease_expires_at > now() RETURNING *;
-- name: RenewAudioJobLease :one
UPDATE audio_jobs SET lease_expires_at=now() + (sqlc.arg(lease_seconds)::integer * INTERVAL '1 second'),
  last_heartbeat_at=now(), updated_at=now()
WHERE id=sqlc.arg(id) AND status='processing' AND lease_owner=sqlc.arg(lease_owner) AND lease_expires_at > now()
RETURNING *;
-- name: RetryAudioJob :one
UPDATE audio_jobs SET status=$2, error_summary=$3, available_at=$4, lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
WHERE id=$1 AND status='processing' AND lease_owner=$5 AND lease_expires_at > now() RETURNING *;
-- name: ReclaimExpiredAudioJobs :many
WITH abandoned AS (
  SELECT id FROM audio_jobs
  WHERE status='processing' AND lease_expires_at <= now()
  ORDER BY lease_expires_at FOR UPDATE SKIP LOCKED LIMIT 100
)
UPDATE audio_jobs AS job SET
  status=CASE WHEN job.attempt_count >= $1 THEN 'failed' ELSE 'retryable' END,
  error_summary=$2,
  available_at=CASE WHEN job.attempt_count >= $1 THEN job.available_at ELSE $3 END,
  lease_owner=NULL, lease_expires_at=NULL, updated_at=now()
WHERE job.id IN (SELECT id FROM abandoned) RETURNING job.*;
-- name: ListExpiredAudioJobs :many
SELECT * FROM audio_jobs
WHERE expires_at <= now() AND status <> 'expired'
  AND (status <> 'processing' OR lease_expires_at <= now())
ORDER BY expires_at LIMIT $1;
-- name: ExpireAudioJob :one
UPDATE audio_jobs SET status='expired', transcript=NULL, updated_at=now() WHERE id=$1 RETURNING *;
-- name: GetAudioBacklog :one
SELECT
  count(*) FILTER (WHERE status = 'queued')::int AS queued_count,
  count(*) FILTER (WHERE status = 'processing')::int AS processing_count,
  count(*) FILTER (WHERE status = 'retryable')::int AS retryable_count,
  count(*) FILTER (WHERE status = 'processing' AND lease_expires_at < now())::int AS expired_lease_count,
  count(*) FILTER (WHERE status IN ('queued','retryable') AND available_at < now() - INTERVAL '2 minutes')::int AS overdue_count,
  (min(created_at))::timestamptz AS oldest_waiting_at,
  (min(available_at) FILTER (WHERE status = 'retryable'))::timestamptz AS next_retry_at
FROM audio_jobs WHERE status IN ('queued','processing','retryable');
-- name: GetLatestAudioFailure :one
SELECT error_summary, updated_at FROM audio_jobs
WHERE status IN ('retryable','failed') AND error_summary IS NOT NULL
ORDER BY updated_at DESC LIMIT 1;
