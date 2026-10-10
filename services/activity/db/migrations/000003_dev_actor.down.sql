-- Restores the original actor kinds. Events are append-only audit records and
-- are never rewritten, so this fails while any 'dev' event exists; remove or
-- archive those deliberately before migrating down.
ALTER TABLE activity_events DROP CONSTRAINT activity_events_actor_kind_check;
ALTER TABLE activity_events ADD CONSTRAINT activity_events_actor_kind_check CHECK (actor_kind IN ('user', 'agent', 'system'));
