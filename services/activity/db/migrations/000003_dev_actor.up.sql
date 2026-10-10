-- Development tools and agents working on Life OS (for example Claude Code)
-- get their own actor kind, so their writes to live data are told apart from
-- the owner ('user') and the Life OS AI ('agent'). The new constraint only
-- widens the old one; existing rows are unchanged.
ALTER TABLE activity_events DROP CONSTRAINT activity_events_actor_kind_check;
ALTER TABLE activity_events ADD CONSTRAINT activity_events_actor_kind_check CHECK (actor_kind IN ('user', 'agent', 'system', 'dev'));
