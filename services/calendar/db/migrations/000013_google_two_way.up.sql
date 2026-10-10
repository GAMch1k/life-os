-- Google Calendar two-way sync (P11-03).
--
-- Events created in Life OS (source.kind manual) are exported to a
-- dedicated "Life OS" calendar that Calendar creates in the owner's Google
-- account. The connection remembers that calendar's Google id and whether
-- exporting is on (the owner's "Export events created in Life OS").
-- lifeos_calendar_note says what happened to the calendar last, such as
-- that it was deleted in Google and created again; null when nothing needs
-- saying. export_backfilled_at is when the Life OS events still ahead were
-- last queued for export as a whole: null (as for a connection made before
-- this release) queues them once, so upcoming events, series included, are
-- exported and not only those created afterwards.
ALTER TABLE google_connections
    ADD COLUMN export_enabled BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN export_backfilled_at TIMESTAMPTZ,
    ADD COLUMN lifeos_calendar_id TEXT CHECK (lifeos_calendar_id IS NULL OR lifeos_calendar_id <> ''),
    ADD COLUMN lifeos_calendar_created_at TIMESTAMPTZ,
    ADD COLUMN lifeos_calendar_note TEXT;

-- The export outbox and the link of each exported Life OS event to its copy
-- in the Life OS calendar. A Life OS event stays the owner's event in Life
-- OS: every change is stored locally first and queued here (requested_at),
-- then written to Google by the export worker with retries and backoff.
-- The row outlives a deleted event (no foreign key) until the deletion has
-- been exported; deleted says so. etag and google_updated are what Google
-- returned for the last write, so the inbound sync of the Life OS calendar
-- recognizes its own writes. exported_exceptions are the recurrence ids of
-- the occurrence changes written to Google as changed instances.
CREATE TABLE google_exports (
    event_id UUID PRIMARY KEY,
    connection_id UUID NOT NULL REFERENCES google_connections (id) ON DELETE CASCADE,
    google_calendar_id TEXT CHECK (google_calendar_id IS NULL OR google_calendar_id <> ''),
    google_event_id TEXT CHECK (google_event_id IS NULL OR google_event_id <> ''),
    etag TEXT,
    google_updated TIMESTAMPTZ,
    exported_exceptions TEXT[] NOT NULL DEFAULT '{}',
    -- pending: a change waits to be written; synced: Google has the latest
    -- change; failed: the last attempt failed (last_error) and is retried at
    -- next_attempt_at.
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
