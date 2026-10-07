CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE TABLE audio_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source TEXT NOT NULL CHECK (source IN ('web','watch','manual')),
    source_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    object_key TEXT NOT NULL UNIQUE,
    content_type TEXT NOT NULL,
    byte_size BIGINT NOT NULL CHECK (byte_size > 0),
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','processing','succeeded','retryable','failed','expired')),
    transcript TEXT, inbox_item_id UUID,
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    error_summary TEXT, available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ, expires_at TIMESTAMPTZ
);
CREATE INDEX audio_jobs_available_idx ON audio_jobs (available_at, created_at) WHERE status IN ('queued','retryable');
CREATE UNIQUE INDEX audio_jobs_inbox_item_id_key ON audio_jobs (inbox_item_id) WHERE inbox_item_id IS NOT NULL;
