CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE tasks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title TEXT NOT NULL CHECK (btrim(title) <> ''),
    notes TEXT,
    due_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    recurrence_frequency TEXT CHECK (recurrence_frequency IN ('daily', 'weekly', 'monthly')),
    recurrence_interval INTEGER CHECK (recurrence_interval >= 1),
    recurrence_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((recurrence_frequency IS NULL) = (recurrence_interval IS NULL))
);

CREATE INDEX tasks_due_at_open_idx ON tasks (due_at) WHERE completed_at IS NULL;
CREATE INDEX tasks_completed_at_idx ON tasks (completed_at);
