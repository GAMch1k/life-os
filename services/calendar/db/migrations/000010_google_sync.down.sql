-- Synced Google events leave with their sources; ICS sources and events
-- created in Life OS stay as they are.
DELETE FROM calendar_sources WHERE kind = 'google';

ALTER TABLE events
    DROP CONSTRAINT IF EXISTS events_source_reminders_check,
    DROP COLUMN IF EXISTS source_reminders;

DROP TABLE IF EXISTS google_events;
DROP INDEX IF EXISTS google_calendars_due_idx;

ALTER TABLE google_calendars
    DROP COLUMN IF EXISTS time_zone,
    DROP COLUMN IF EXISTS sync_token,
    DROP COLUMN IF EXISTS sync_window_start,
    DROP COLUMN IF EXISTS next_sync_at,
    DROP COLUMN IF EXISTS syncing_until,
    DROP COLUMN IF EXISTS last_sync_attempt_at,
    DROP COLUMN IF EXISTS last_synced_at,
    DROP COLUMN IF EXISTS last_sync_error,
    DROP COLUMN IF EXISTS sync_failures,
    DROP COLUMN IF EXISTS last_sync_counts,
    DROP COLUMN IF EXISTS derived_at;

ALTER TABLE calendar_sources
    DROP CONSTRAINT IF EXISTS calendar_sources_google_calendar_check,
    DROP COLUMN IF EXISTS google_calendar_id,
    DROP CONSTRAINT calendar_sources_kind_check;
ALTER TABLE calendar_sources ADD CONSTRAINT calendar_sources_kind_check CHECK (kind IN ('ics'));
