-- Exported events stay in the owner's Life OS Google calendar; Life OS
-- forgets the links and stops exporting.
DROP TABLE IF EXISTS google_exports;

ALTER TABLE google_connections
    DROP COLUMN IF EXISTS export_enabled,
    DROP COLUMN IF EXISTS lifeos_calendar_id,
    DROP COLUMN IF EXISTS lifeos_calendar_created_at,
    DROP COLUMN IF EXISTS lifeos_calendar_note;
