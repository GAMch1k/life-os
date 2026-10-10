DROP INDEX IF EXISTS events_source_identity_key;
ALTER TABLE events
    DROP CONSTRAINT IF EXISTS events_reminders_check,
    DROP CONSTRAINT IF EXISTS events_time_zone_check,
    DROP CONSTRAINT IF EXISTS events_removed_from_source_check,
    DROP CONSTRAINT IF EXISTS events_source_identity_check,
    DROP CONSTRAINT IF EXISTS events_source_kind_check,
    DROP CONSTRAINT IF EXISTS events_cancelled_at_check,
    DROP CONSTRAINT IF EXISTS events_status_check,
    DROP COLUMN IF EXISTS reminders,
    DROP COLUMN IF EXISTS removed_from_source_at,
    DROP COLUMN IF EXISTS external_uid,
    DROP COLUMN IF EXISTS source_id,
    DROP COLUMN IF EXISTS source_kind,
    DROP COLUMN IF EXISTS cancelled_at,
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS time_zone,
    DROP COLUMN IF EXISTS location;
