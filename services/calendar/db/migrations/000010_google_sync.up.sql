-- Google Calendar inbound sync (P11-02). Every Google calendar chosen to
-- sync becomes a calendar source of kind google, so its events are imported
-- events like a feed's: source.kind google, read-only except reminders, and
-- reconciled by source. The source belongs to its Google calendar
-- (google_calendar_id): deleting the calendar (unticking it, disconnecting
-- Google, or Google no longer listing it) deletes the source and, through
-- events.source_id, its events.
ALTER TABLE calendar_sources DROP CONSTRAINT calendar_sources_kind_check;
ALTER TABLE calendar_sources
    ADD CONSTRAINT calendar_sources_kind_check CHECK (kind IN ('ics', 'google')),
    ADD COLUMN google_calendar_id UUID UNIQUE REFERENCES google_calendars (id) ON DELETE CASCADE,
    ADD CONSTRAINT calendar_sources_google_calendar_check CHECK ((kind = 'google') = (google_calendar_id IS NOT NULL));

-- The sync state stays with the Google calendar: the sync token of the last
-- complete listing, the start of the time window the last full listing
-- covered, the schedule with its single-sync lease, and the last outcome.
-- time_zone is the calendar's own zone as Google reports it.
ALTER TABLE google_calendars
    ADD COLUMN time_zone TEXT,
    ADD COLUMN sync_token TEXT,
    ADD COLUMN sync_window_start TIMESTAMPTZ,
    ADD COLUMN next_sync_at TIMESTAMPTZ,
    ADD COLUMN syncing_until TIMESTAMPTZ,
    ADD COLUMN last_sync_attempt_at TIMESTAMPTZ,
    ADD COLUMN last_synced_at TIMESTAMPTZ,
    ADD COLUMN last_sync_error TEXT,
    ADD COLUMN sync_failures INTEGER NOT NULL DEFAULT 0 CHECK (sync_failures >= 0),
    ADD COLUMN last_sync_counts JSONB CHECK (last_sync_counts IS NULL OR jsonb_typeof(last_sync_counts) = 'object'),
    ADD COLUMN derived_at TIMESTAMPTZ;

-- Calendars chosen before this release sync right away.
UPDATE google_calendars SET next_sync_at = now() WHERE sync_enabled;

CREATE INDEX google_calendars_due_idx ON google_calendars (next_sync_at) WHERE sync_enabled;

-- The calendar's events as Google last sent them (the fields Calendar maps,
-- see internal/google): a single event, a recurring event's master, or one
-- changed or cancelled occurrence of it (recurring_event_id set). Incremental
-- syncs change only what Google reports, and the calendar's events are
-- derived from all of them, so a series' occurrences and changes that arrive
-- in different syncs come together.
CREATE TABLE google_events (
    calendar_id UUID NOT NULL REFERENCES google_calendars (id) ON DELETE CASCADE,
    event_id TEXT NOT NULL CHECK (event_id <> ''),
    recurring_event_id TEXT CHECK (recurring_event_id IS NULL OR recurring_event_id <> ''),
    data JSONB NOT NULL CHECK (jsonb_typeof(data) = 'object'),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (calendar_id, event_id)
);

CREATE INDEX google_events_instances_idx ON google_events (calendar_id, recurring_event_id) WHERE recurring_event_id IS NOT NULL;

-- The reminders a source last gave an imported event ({"useDefault": bool,
-- "reminders": [...]}), so a sync can tell the owner's own reminder change
-- (kept) from the event still following its source (updated). Null for
-- events created in Life OS and for sources that carry no reminders (ICS).
ALTER TABLE events
    ADD COLUMN source_reminders JSONB,
    ADD CONSTRAINT events_source_reminders_check CHECK (
        source_reminders IS NULL OR (source_kind <> 'manual' AND jsonb_typeof(source_reminders) = 'object')
    );
