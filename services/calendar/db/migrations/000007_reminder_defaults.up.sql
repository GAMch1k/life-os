-- Reminders work like Google Calendar's notifications: an event follows its
-- calendar's default reminders (use_default_reminders) and adds its own
-- (reminders) on top. Until now an event's reminders replaced the defaults:
-- NULL followed them and a list, even an empty one, applied instead. Every
-- event keeps the reminders that apply to it.
ALTER TABLE events ADD COLUMN use_default_reminders BOOLEAN NOT NULL DEFAULT true;
UPDATE events SET use_default_reminders = false WHERE reminders IS NOT NULL;
UPDATE events SET reminders = '[]'::jsonb WHERE reminders IS NULL;
ALTER TABLE events
    ALTER COLUMN reminders SET DEFAULT '[]'::jsonb,
    ALTER COLUMN reminders SET NOT NULL;

-- The owner's default reminders for the events they create in Life OS, one
-- list for timed and one for all-day events. A single row, written on the
-- first change; until then there are none.
CREATE TABLE calendar_settings (
    id BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
    timed_default_reminders JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(timed_default_reminders) = 'array'),
    all_day_default_reminders JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(all_day_default_reminders) = 'array'),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
