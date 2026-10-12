DROP INDEX IF EXISTS audio_jobs_idempotency_key_idx;
ALTER TABLE audio_jobs DROP CONSTRAINT IF EXISTS audio_jobs_idempotency_key_check;
ALTER TABLE audio_jobs DROP COLUMN IF EXISTS request_hash;
ALTER TABLE audio_jobs DROP COLUMN IF EXISTS idempotency_key;
