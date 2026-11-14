DROP TABLE IF EXISTS diagnostic_events;
DROP INDEX IF EXISTS activity_events_actor_user_idx;
ALTER TABLE activity_events DROP COLUMN IF EXISTS actor_user_id;
