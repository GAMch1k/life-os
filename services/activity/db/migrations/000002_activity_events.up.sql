-- Activity events are append-only product/audit records. Owning services emit
-- them at operation boundaries; this service never reads another service's data.
CREATE TABLE activity_events (
    id UUID PRIMARY KEY,
    type TEXT NOT NULL CHECK (type ~ '^[a-z][a-z_]*(\.[a-z][a-z_]*)+$'),
    source TEXT NOT NULL CHECK (btrim(source) <> ''),
    subject_type TEXT NOT NULL CHECK (btrim(subject_type) <> ''),
    subject_id TEXT NOT NULL CHECK (btrim(subject_id) <> ''),
    correlation_id TEXT NOT NULL CHECK (btrim(correlation_id) <> ''),
    causation_id UUID,
    actor_kind TEXT NOT NULL CHECK (actor_kind IN ('user', 'agent', 'system')),
    actor_name TEXT,
    outcome TEXT NOT NULL CHECK (outcome IN ('succeeded', 'pending', 'failed')),
    summary TEXT NOT NULL CHECK (btrim(summary) <> ''),
    data JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX activity_events_occurred_idx ON activity_events (occurred_at DESC, id DESC);
CREATE INDEX activity_events_correlation_idx ON activity_events (correlation_id, occurred_at DESC);
CREATE INDEX activity_events_subject_idx ON activity_events (subject_type, subject_id, occurred_at DESC);
CREATE INDEX activity_events_causation_idx ON activity_events (causation_id) WHERE causation_id IS NOT NULL;
