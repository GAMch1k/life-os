-- Without priorities every event is as important as any other: own
-- priorities, source defaults, and occurrences' own priorities are dropped.
-- An occurrence whose only change was its priority follows its series again.
DELETE FROM event_exceptions WHERE changes - 'priority' = '{}'::jsonb AND cancelled_at IS NULL;
UPDATE event_exceptions SET changes = changes - 'priority' WHERE changes ? 'priority';
ALTER TABLE calendar_sources DROP COLUMN IF EXISTS default_priority;
ALTER TABLE events DROP COLUMN IF EXISTS priority;
