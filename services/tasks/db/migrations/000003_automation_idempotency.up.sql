ALTER TABLE tasks ADD COLUMN automation_idempotency_key TEXT;
ALTER TABLE tasks ADD COLUMN automation_payload_hash TEXT;
CREATE UNIQUE INDEX tasks_automation_idempotency_key_idx ON tasks (automation_idempotency_key) WHERE automation_idempotency_key IS NOT NULL;
