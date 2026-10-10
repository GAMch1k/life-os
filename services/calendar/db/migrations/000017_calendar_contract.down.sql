-- Gives back the structure of the in-process era (migrations 000005 to
-- 000016), not its data: the google_* tables come back empty, and the
-- legacy columns of every source empty (no URL, filter, preset, or refresh
-- state, out of process). Every source stays an integration's (kind
-- external), and so do their events, as migration 000016 left them.
-- private_location goes (that release knew it from the preset only).
SET CONSTRAINTS events_record_change, event_exceptions_record_change IMMEDIATE;

-- Calendar's former Google connection (000009, 000010, 000013).
CREATE TABLE google_connections (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    singleton BOOLEAN NOT NULL DEFAULT true UNIQUE CHECK (singleton),
    account_email TEXT NOT NULL CHECK (btrim(account_email) <> ''),
    scopes TEXT[] NOT NULL DEFAULT '{}',
    refresh_token_ciphertext BYTEA NOT NULL,
    refresh_token_nonce BYTEA NOT NULL,
    access_token_expires_at TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'connected' CHECK (status IN ('connected', 'expired', 'error')),
    last_error TEXT,
    connected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    export_enabled BOOLEAN NOT NULL DEFAULT true,
    export_backfilled_at TIMESTAMPTZ,
    lifeos_calendar_id TEXT CHECK (lifeos_calendar_id IS NULL OR lifeos_calendar_id <> ''),
    lifeos_calendar_created_at TIMESTAMPTZ,
    lifeos_calendar_note TEXT
);

CREATE TABLE google_calendars (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id UUID NOT NULL REFERENCES google_connections (id) ON DELETE CASCADE,
    google_calendar_id TEXT NOT NULL CHECK (google_calendar_id <> ''),
    summary TEXT NOT NULL,
    color TEXT,
    access_role TEXT NOT NULL CHECK (access_role IN ('owner', 'writer', 'reader', 'freeBusyReader')),
    is_primary BOOLEAN NOT NULL DEFAULT false,
    sync_enabled BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    time_zone TEXT,
    sync_token TEXT,
    sync_window_start TIMESTAMPTZ,
    next_sync_at TIMESTAMPTZ,
    syncing_until TIMESTAMPTZ,
    last_sync_attempt_at TIMESTAMPTZ,
    last_synced_at TIMESTAMPTZ,
    last_sync_error TEXT,
    sync_failures INTEGER NOT NULL DEFAULT 0 CHECK (sync_failures >= 0),
    last_sync_counts JSONB CHECK (last_sync_counts IS NULL OR jsonb_typeof(last_sync_counts) = 'object'),
    derived_at TIMESTAMPTZ,
    UNIQUE (connection_id, google_calendar_id)
);

CREATE INDEX google_calendars_due_idx ON google_calendars (next_sync_at) WHERE sync_enabled;

CREATE TABLE google_oauth_states (
    state_hash BYTEA PRIMARY KEY,
    code_verifier TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE google_events (
    calendar_id UUID NOT NULL REFERENCES google_calendars (id) ON DELETE CASCADE,
    event_id TEXT NOT NULL CHECK (event_id <> ''),
    recurring_event_id TEXT CHECK (recurring_event_id IS NULL OR recurring_event_id <> ''),
    data JSONB NOT NULL CHECK (jsonb_typeof(data) = 'object'),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (calendar_id, event_id)
);

CREATE INDEX google_events_instances_idx ON google_events (calendar_id, recurring_event_id) WHERE recurring_event_id IS NOT NULL;

CREATE TABLE google_exports (
    event_id UUID PRIMARY KEY,
    connection_id UUID NOT NULL REFERENCES google_connections (id) ON DELETE CASCADE,
    google_calendar_id TEXT CHECK (google_calendar_id IS NULL OR google_calendar_id <> ''),
    google_event_id TEXT CHECK (google_event_id IS NULL OR google_event_id <> ''),
    etag TEXT,
    google_updated TIMESTAMPTZ,
    exported_exceptions TEXT[] NOT NULL DEFAULT '{}',
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'synced', 'failed')),
    deleted BOOLEAN NOT NULL DEFAULT false,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    next_attempt_at TIMESTAMPTZ,
    exporting_until TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT,
    exported_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT google_exports_link_check CHECK ((google_event_id IS NULL) = (google_calendar_id IS NULL))
);

CREATE INDEX google_exports_due_idx ON google_exports (next_attempt_at) WHERE next_attempt_at IS NOT NULL;
CREATE UNIQUE INDEX google_exports_google_event_key ON google_exports (google_calendar_id, google_event_id) WHERE google_event_id IS NOT NULL;

-- The legacy source columns (000005, 000006, 000010, 000014), empty. The
-- existing sources are out of process; a new row defaults to in process,
-- as before.
ALTER TABLE calendar_sources
    DROP CONSTRAINT calendar_sources_kind_check,
    DROP COLUMN private_location,
    ALTER COLUMN integration DROP NOT NULL,
    ALTER COLUMN external_id DROP NOT NULL,
    ALTER COLUMN provider DROP NOT NULL,
    ADD COLUMN url TEXT CONSTRAINT calendar_sources_url_check CHECK (url <> ''),
    ADD COLUMN filter_include TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN filter_exclude TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN next_refresh_at TIMESTAMPTZ,
    ADD COLUMN refreshing_until TIMESTAMPTZ,
    ADD COLUMN last_attempt_at TIMESTAMPTZ,
    ADD COLUMN last_success_at TIMESTAMPTZ,
    ADD COLUMN last_outcome TEXT CONSTRAINT calendar_sources_last_outcome_check CHECK (last_outcome IN ('imported', 'not_modified', 'failed')),
    ADD COLUMN last_error TEXT,
    ADD COLUMN last_counts JSONB,
    ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0 CONSTRAINT calendar_sources_consecutive_failures_check CHECK (consecutive_failures >= 0),
    ADD COLUMN etag TEXT,
    ADD COLUMN last_modified TEXT,
    ADD COLUMN preset TEXT CONSTRAINT calendar_sources_preset_check CHECK (preset IS NULL OR btrim(preset) <> ''),
    ADD COLUMN preset_config JSONB CONSTRAINT calendar_sources_preset_config_check CHECK (preset_config IS NULL OR jsonb_typeof(preset_config) = 'object'),
    ADD COLUMN google_calendar_id UUID UNIQUE REFERENCES google_calendars (id) ON DELETE CASCADE,
    ADD COLUMN in_process BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE calendar_sources ALTER COLUMN in_process SET DEFAULT true;

CREATE INDEX calendar_sources_due_idx ON calendar_sources (next_refresh_at) WHERE enabled;
CREATE UNIQUE INDEX calendar_sources_preset_key ON calendar_sources (preset) WHERE preset IS NOT NULL;

ALTER TABLE calendar_sources
    ADD CONSTRAINT calendar_sources_kind_check CHECK (kind IN ('ics', 'google', 'external')),
    ADD CONSTRAINT calendar_sources_identity_check CHECK ((integration IS NULL) = (external_id IS NULL)),
    ADD CONSTRAINT calendar_sources_integration_identity_check CHECK (in_process OR integration IS NOT NULL),
    ADD CONSTRAINT calendar_sources_external_check CHECK ((kind = 'external') = (NOT in_process)),
    ADD CONSTRAINT calendar_sources_google_calendar_check CHECK (google_calendar_id IS NULL OR kind <> 'ics'),
    ADD CONSTRAINT calendar_sources_in_process_google_check CHECK (NOT in_process OR (kind = 'google') = (google_calendar_id IS NOT NULL)),
    ADD CONSTRAINT calendar_sources_in_process_url_check CHECK (NOT in_process OR url IS NOT NULL);

ALTER TABLE events
    DROP CONSTRAINT events_source_kind_check,
    ADD CONSTRAINT events_source_kind_check CHECK (source_kind IN ('manual', 'ics', 'google', 'external'));

CREATE OR REPLACE FUNCTION calendar_event_followed(kind TEXT, source UUID) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT kind = 'manual' OR (kind = 'external' AND EXISTS (
        SELECT 1 FROM calendar_sources WHERE id = source AND NOT in_process AND NOT read_only
    ))
$$;
