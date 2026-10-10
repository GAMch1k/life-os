-- Calendar events become complete domain objects: location, the timezone a
-- timed event was scheduled in, an explicit cancelled state kept as history,
-- source identity for imported events, and stored reminder intent. Every new
-- column is nullable or defaulted, so existing events become active manual
-- events with nothing else changed. The legacy external_provider/external_id
-- columns stay as they are.
ALTER TABLE events
    ADD COLUMN location TEXT,
    ADD COLUMN time_zone TEXT,
    ADD COLUMN status TEXT NOT NULL DEFAULT 'active',
    ADD COLUMN cancelled_at TIMESTAMPTZ,
    ADD COLUMN source_kind TEXT NOT NULL DEFAULT 'manual',
    ADD COLUMN source_id UUID,
    ADD COLUMN external_uid TEXT,
    ADD COLUMN removed_from_source_at TIMESTAMPTZ,
    ADD COLUMN reminders JSONB;

ALTER TABLE events
    ADD CONSTRAINT events_status_check CHECK (status IN ('active', 'cancelled')),
    ADD CONSTRAINT events_cancelled_at_check CHECK ((status = 'cancelled') = (cancelled_at IS NOT NULL)),
    ADD CONSTRAINT events_source_kind_check CHECK (source_kind IN ('manual', 'ics', 'google')),
    -- Manual events have no source identity; imported ones always do.
    ADD CONSTRAINT events_source_identity_check CHECK (
        (source_kind = 'manual' AND source_id IS NULL AND external_uid IS NULL)
        OR (source_kind <> 'manual' AND source_id IS NOT NULL AND external_uid IS NOT NULL)
    ),
    -- Only an imported event can leave its source, and leaving cancels it.
    ADD CONSTRAINT events_removed_from_source_check CHECK (
        removed_from_source_at IS NULL OR (source_kind <> 'manual' AND status = 'cancelled')
    ),
    -- All-day dates are floating local dates; only timed events have a zone.
    ADD CONSTRAINT events_time_zone_check CHECK (time_zone IS NULL OR NOT all_day),
    ADD CONSTRAINT events_reminders_check CHECK (reminders IS NULL OR jsonb_typeof(reminders) = 'array');

-- One row per upstream event of a source, so refreshes update in place.
CREATE UNIQUE INDEX events_source_identity_key ON events (source_id, external_uid) WHERE source_id IS NOT NULL;
