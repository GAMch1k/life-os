-- The Google Calendar integration (P11-07). These tables moved here from
-- Calendar (its migrations 000009, 000010, and 000013), with the same
-- columns, so the one-time handoff copies them over as they are: the
-- owner's one connection (with the same sealed refresh token), its calendar
-- list and sync state, the pending authorizations, the raw Google events,
-- and the export links of Life OS events. What is new is how this service
-- talks to Calendar: only through Calendar's integration API, so it keeps
-- what it last told Calendar (google_sent_events), the change feed's
-- position (on the connection), and the owner's edits waiting to be
-- written to Google (google_pushes).

-- The owner's one Google account. Only the refresh token is persisted,
-- encrypted with LIFE_OS_GOOGLE_TOKEN_KEY (AES-256-GCM; the nonce is stored
-- beside it). Access tokens live in memory only.
--
-- feed_cursor is the position in Calendar's change feed handled so far
-- (Calendar's opaque nextCursor; null: start with a snapshot);
-- feed_snapshot_started_at is when the snapshot being read started (null
-- outside one); feed_read_at, feed_error, and feed_error_at say how reading
-- the feed goes.
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
    lifeos_calendar_note TEXT,
    feed_cursor TEXT CHECK (feed_cursor IS NULL OR feed_cursor <> ''),
    feed_snapshot_started_at TIMESTAMPTZ,
    feed_read_at TIMESTAMPTZ,
    feed_error TEXT,
    feed_error_at TIMESTAMPTZ
);

-- The account's calendar list with the owner's choice of the calendars to
-- sync and each one's sync state (the sync token of the last complete
-- listing, the start of the time window the last full listing covered, the
-- schedule with its single-sync lease, and the last outcome). source_id is
-- the calendar's source in Calendar (registered under the integration
-- google-calendar with the calendar's Google id), and registration what it
-- was last registered with ({"name", "readOnly", "defaultReminders"}), so
-- it is registered again only when that changes.
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
    source_id UUID,
    registration JSONB CHECK (registration IS NULL OR jsonb_typeof(registration) = 'object'),
    UNIQUE (connection_id, google_calendar_id)
);

CREATE INDEX google_calendars_due_idx ON google_calendars (next_sync_at) WHERE sync_enabled;

-- Pending authorizations: the OAuth state (stored as its SHA-256, so the
-- table cannot be replayed) and the PKCE verifier, single use and
-- short-lived.
CREATE TABLE google_oauth_states (
    state_hash BYTEA PRIMARY KEY,
    code_verifier TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A calendar's events as Google last sent them (the fields the mapping
-- reads): a single event, a recurring event's master, or one changed or
-- cancelled occurrence of it (recurring_event_id set). Incremental syncs
-- change only what Google reports; the events sent to Calendar are derived
-- from all of them.
CREATE TABLE google_events (
    calendar_id UUID NOT NULL REFERENCES google_calendars (id) ON DELETE CASCADE,
    event_id TEXT NOT NULL CHECK (event_id <> ''),
    recurring_event_id TEXT CHECK (recurring_event_id IS NULL OR recurring_event_id <> ''),
    data JSONB NOT NULL CHECK (jsonb_typeof(data) = 'object'),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (calendar_id, event_id)
);

CREATE INDEX google_events_instances_idx ON google_events (calendar_id, recurring_event_id) WHERE recurring_event_id IS NOT NULL;

-- What this service last told Calendar of each event of a calendar's source
-- (the SourceEvent, by external uid), so a sync sends only what changed and
-- names the events Google no longer has. item is null when it is not known
-- what Calendar holds (such as the events handed over from Calendar's own
-- adapter): the next sync sends the event again.
CREATE TABLE google_sent_events (
    calendar_id UUID NOT NULL REFERENCES google_calendars (id) ON DELETE CASCADE,
    external_uid TEXT NOT NULL CHECK (external_uid <> ''),
    item JSONB CHECK (item IS NULL OR jsonb_typeof(item) = 'object'),
    PRIMARY KEY (calendar_id, external_uid)
);

-- A series Calendar created in a source (the new series of a "this and
-- following" change, under a lifeos:<id> uid) that this service then
-- created in Google: the next sync gives Calendar's event its Google id
-- (previousExternalUid).
CREATE TABLE google_rekeys (
    calendar_id UUID NOT NULL REFERENCES google_calendars (id) ON DELETE CASCADE,
    google_event_id TEXT NOT NULL CHECK (google_event_id <> ''),
    previous_uid TEXT NOT NULL CHECK (previous_uid <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (calendar_id, google_event_id)
);

-- The export of each Life OS event to the Life OS calendar: the link to
-- its copy (google_calendar_id, google_event_id) with what Google returned
-- for the last write (etag, google_updated, the occurrence changes written
-- as changed instances), and the outbox state. payload is the event as
-- Calendar's change feed last gave it ({"event", "exceptions"}), and
-- payload_at when; requested_at moves with every change to export. The row
-- outlives a deleted event until the deletion has been exported (deleted).
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
    payload JSONB CHECK (payload IS NULL OR jsonb_typeof(payload) = 'object'),
    payload_at TIMESTAMPTZ,
    CONSTRAINT google_exports_link_check CHECK ((google_event_id IS NULL) = (google_calendar_id IS NULL))
);

CREATE INDEX google_exports_due_idx ON google_exports (next_attempt_at) WHERE next_attempt_at IS NOT NULL;
CREATE UNIQUE INDEX google_exports_google_event_key ON google_exports (google_calendar_id, google_event_id) WHERE google_event_id IS NOT NULL;

-- The owner's edits, made in Life OS, of events of Google calendars the
-- account may edit, waiting to be written to Google: the event as the
-- change feed last gave it ({"event", "exceptions"}; null with deleted),
-- by Calendar event id. A calendar with a waiting edit is not synced from
-- Google until the edit is written (or given up), so a sync never
-- overwrites it.
CREATE TABLE google_pushes (
    event_id UUID PRIMARY KEY,
    calendar_id UUID NOT NULL REFERENCES google_calendars (id) ON DELETE CASCADE,
    external_uid TEXT NOT NULL CHECK (external_uid <> ''),
    payload JSONB CHECK (payload IS NULL OR jsonb_typeof(payload) = 'object'),
    deleted BOOLEAN NOT NULL DEFAULT false,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    pushing_until TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT google_pushes_payload_check CHECK (deleted OR payload IS NOT NULL)
);

CREATE INDEX google_pushes_calendar_idx ON google_pushes (calendar_id);
