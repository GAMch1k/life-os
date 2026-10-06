CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE TABLE events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(), title TEXT NOT NULL CHECK (btrim(title) <> ''), description TEXT,
    all_day BOOLEAN NOT NULL, start_at TIMESTAMPTZ, end_at TIMESTAMPTZ, start_date DATE, end_date DATE,
    recurrence_frequency TEXT CHECK (recurrence_frequency IN ('daily', 'weekly', 'monthly', 'yearly')),
    recurrence_interval INTEGER CHECK (recurrence_interval >= 1), recurrence_until TIMESTAMPTZ,
    external_provider TEXT, external_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((all_day AND start_at IS NULL AND end_at IS NULL AND start_date IS NOT NULL AND end_date IS NOT NULL) OR (NOT all_day AND start_at IS NOT NULL AND end_at IS NOT NULL AND start_date IS NULL AND end_date IS NULL)),
    CHECK ((recurrence_frequency IS NULL) = (recurrence_interval IS NULL))
);
CREATE UNIQUE INDEX events_external_identity_key ON events (external_provider, external_id) WHERE external_provider IS NOT NULL;
CREATE INDEX events_timed_range_idx ON events (start_at, end_at) WHERE NOT all_day;
CREATE INDEX events_all_day_range_idx ON events (start_date, end_date) WHERE all_day;
