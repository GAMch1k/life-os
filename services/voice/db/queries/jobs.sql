-- name: CreateAudioJob :one
INSERT INTO audio_jobs (source, source_metadata, object_key, content_type, byte_size, expires_at)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING *;
-- name: GetAudioJob :one
SELECT * FROM audio_jobs WHERE id = $1;
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
