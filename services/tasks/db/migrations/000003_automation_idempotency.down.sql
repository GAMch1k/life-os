DROP INDEX IF EXISTS tasks_automation_idempotency_key_idx;
ALTER TABLE tasks DROP COLUMN IF EXISTS automation_payload_hash;
ALTER TABLE tasks DROP COLUMN IF EXISTS automation_idempotency_key;
