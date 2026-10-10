-- Calendar sources: external feeds (ICS today) whose events Calendar imports
-- and keeps refreshed. A source holds its configuration (name, URL, type
-- filter, default reminders) and its refresh state, so the schedule, the
-- conditional-request validators, and the last outcome survive restarts.
-- Imported events reference their source; deleting a source deletes the
-- events imported from it (disable it instead to keep them).
CREATE TABLE calendar_sources (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind TEXT NOT NULL CHECK (kind IN ('ics')),
    name TEXT NOT NULL CHECK (btrim(name) <> ''),
    url TEXT NOT NULL CHECK (url <> ''),
    enabled BOOLEAN NOT NULL DEFAULT true,
    -- Type filter: keep only events matching an include term (all when
    -- empty), then drop those matching an exclude term.
    filter_include TEXT[] NOT NULL DEFAULT '{}',
    filter_exclude TEXT[] NOT NULL DEFAULT '{}',
    -- Reminder intent for imported events whose own reminders are null.
    default_reminders JSONB CHECK (default_reminders IS NULL OR jsonb_typeof(default_reminders) = 'array'),
    -- Refresh state.
    next_refresh_at TIMESTAMPTZ,
    refreshing_until TIMESTAMPTZ,
    last_attempt_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    last_outcome TEXT CHECK (last_outcome IN ('imported', 'not_modified', 'failed')),
    last_error TEXT,
    last_counts JSONB,
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    etag TEXT,
    last_modified TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX calendar_sources_due_idx ON calendar_sources (next_refresh_at) WHERE enabled;

-- Until now no event had a source_id (the API creates only manual events), so
-- the key can be added to existing data as is.
ALTER TABLE events
    ADD CONSTRAINT events_source_id_fkey FOREIGN KEY (source_id) REFERENCES calendar_sources (id) ON DELETE CASCADE;
