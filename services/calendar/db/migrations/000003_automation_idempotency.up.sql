ALTER TABLE events ADD COLUMN automation_idempotency_key TEXT;
ALTER TABLE events ADD COLUMN automation_payload_hash TEXT;
CREATE UNIQUE INDEX events_automation_idempotency_key_idx ON events (automation_idempotency_key) WHERE automation_idempotency_key IS NOT NULL;
