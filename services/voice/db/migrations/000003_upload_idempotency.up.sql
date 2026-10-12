-- An upload's Idempotency-Key and the hash of what it asked for (audio
-- content, source, and metadata) live on the job they created, so a retried
-- upload finds that job instead of creating another.
ALTER TABLE audio_jobs ADD COLUMN idempotency_key TEXT;
ALTER TABLE audio_jobs ADD COLUMN request_hash TEXT;
ALTER TABLE audio_jobs ADD CONSTRAINT audio_jobs_idempotency_key_check CHECK (
    (idempotency_key IS NULL) = (request_hash IS NULL)
    AND (idempotency_key IS NULL OR char_length(idempotency_key) BETWEEN 1 AND 200)
);
CREATE UNIQUE INDEX audio_jobs_idempotency_key_idx ON audio_jobs (idempotency_key) WHERE idempotency_key IS NOT NULL;
