-- Workflow correlation supplied by an upstream caller (for example the Voice
-- job that produced this capture). NULL means the item starts its own workflow
-- and its id is the correlation id. Existing rows keep NULL.
ALTER TABLE inbox_items ADD COLUMN correlation_id TEXT;
