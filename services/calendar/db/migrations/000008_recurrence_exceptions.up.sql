-- Recurring events work like Google Calendar's. A weekly rule can repeat on
-- several days of the week (recurrence_by_weekday, iCalendar day codes) and a
-- rule can end after a number of occurrences (recurrence_count) instead of
-- at recurrence_until. One occurrence of a series can have its own changes
-- or be skipped (This event): event_exceptions keeps them per occurrence,
-- keyed by its recurrence id, the occurrence's original start (an RFC 3339
-- instant in UTC for a timed series, YYYY-MM-DD for an all-day one).
-- Existing series get neither and repeat exactly as before.
ALTER TABLE events
    ADD COLUMN recurrence_by_weekday TEXT[],
    ADD COLUMN recurrence_count INTEGER;

ALTER TABLE events
    ADD CONSTRAINT events_recurrence_by_weekday_check CHECK (
        recurrence_by_weekday IS NULL OR (
            recurrence_frequency = 'weekly'
            AND cardinality(recurrence_by_weekday) BETWEEN 1 AND 7
            AND recurrence_by_weekday <@ ARRAY['MO', 'TU', 'WE', 'TH', 'FR', 'SA', 'SU']::text[]
        )
    ),
    ADD CONSTRAINT events_recurrence_count_check CHECK (
        recurrence_count IS NULL OR (
            recurrence_frequency IS NOT NULL AND recurrence_count BETWEEN 1 AND 1000 AND recurrence_until IS NULL
        )
    );

-- changes holds only the fields the occurrence overrides, with the API's
-- names (title, description, location, startAt/endAt or startDate/endDate,
-- reminders, useDefaultReminders); an explicit null clears description or
-- location for that occurrence. cancelled_at is set while it is skipped. A
-- row always records something; deleting the series deletes its rows.
CREATE TABLE event_exceptions (
    event_id UUID NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    recurrence_id TEXT NOT NULL CHECK (btrim(recurrence_id) <> ''),
    changes JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(changes) = 'object'),
    cancelled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, recurrence_id),
    CONSTRAINT event_exceptions_records_something_check CHECK (changes <> '{}'::jsonb OR cancelled_at IS NOT NULL)
);
