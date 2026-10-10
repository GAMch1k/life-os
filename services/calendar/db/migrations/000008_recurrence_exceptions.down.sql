-- The earlier model has no per-occurrence changes, no weekdays, and no
-- count: occurrence changes and skips are dropped (every occurrence follows
-- its series again), a weekly series on several days repeats on its start's
-- weekday only, and a series that ended after a number of occurrences
-- repeats without end. Events themselves are kept.
DROP TABLE IF EXISTS event_exceptions;
ALTER TABLE events
    DROP CONSTRAINT IF EXISTS events_recurrence_count_check,
    DROP CONSTRAINT IF EXISTS events_recurrence_by_weekday_check,
    DROP COLUMN IF EXISTS recurrence_count,
    DROP COLUMN IF EXISTS recurrence_by_weekday;
