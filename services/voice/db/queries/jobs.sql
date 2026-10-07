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
UPDATE audio_jobs SET status='processing', attempt_count=attempt_count+1, updated_at=now()
WHERE id=(SELECT id FROM candidate) RETURNING *;
-- name: CompleteAudioJob :one
UPDATE audio_jobs SET status='succeeded', transcript=$2, inbox_item_id=$3, error_summary=NULL, completed_at=now(), updated_at=now()
WHERE id=$1 AND status='processing' AND inbox_item_id IS NULL RETURNING *;
-- name: RetryAudioJob :one
UPDATE audio_jobs SET status=$2, error_summary=$3, available_at=$4, updated_at=now()
WHERE id=$1 AND status='processing' RETURNING *;
-- name: ListExpiredAudioJobs :many
SELECT * FROM audio_jobs WHERE expires_at <= now() AND status <> 'expired' ORDER BY expires_at LIMIT $1;
-- name: ExpireAudioJob :one
UPDATE audio_jobs SET status='expired', transcript=NULL, updated_at=now() WHERE id=$1 RETURNING *;
