DROP TABLE IF EXISTS calendar_settings;
ALTER TABLE events
    ALTER COLUMN reminders DROP NOT NULL,
    ALTER COLUMN reminders DROP DEFAULT;
-- An event that used only its defaults follows them again. One that used
-- both keeps only its own reminders: the old model cannot express both.
UPDATE events SET reminders = NULL WHERE use_default_reminders AND reminders = '[]'::jsonb;
ALTER TABLE events DROP COLUMN IF EXISTS use_default_reminders;
