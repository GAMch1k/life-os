DROP INDEX IF EXISTS events_automation_idempotency_key_idx;
ALTER TABLE events DROP COLUMN IF EXISTS automation_payload_hash;
ALTER TABLE events DROP COLUMN IF EXISTS automation_idempotency_key;
