CREATE TABLE inbox_processing_attempts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    inbox_item_id UUID NOT NULL REFERENCES inbox_items(id) ON DELETE RESTRICT,
    idempotency_key TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'processing', 'processed', 'failed', 'retryable')),
    agent_name TEXT NOT NULL, model_profile TEXT NOT NULL,
    actions JSONB NOT NULL DEFAULT '[]'::jsonb,
    error_summary TEXT, started_at TIMESTAMPTZ, completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (inbox_item_id, idempotency_key)
);
CREATE INDEX inbox_processing_attempts_item_idx ON inbox_processing_attempts (inbox_item_id, created_at DESC);
