CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE TABLE inbox_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(), content TEXT NOT NULL CHECK (btrim(content) <> ''),
    source TEXT NOT NULL CHECK (source IN ('manual', 'web', 'voice', 'watch', 'email')),
    source_metadata JSONB NOT NULL DEFAULT '{}'::jsonb, media_references JSONB NOT NULL DEFAULT '[]'::jsonb,
    state TEXT NOT NULL DEFAULT 'raw' CHECK (state IN ('raw', 'processing', 'resolved', 'discarded')),
    processing_metadata JSONB NOT NULL DEFAULT '{}'::jsonb, resolved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX inbox_items_unresolved_idx ON inbox_items (created_at DESC) WHERE state IN ('raw', 'processing');
