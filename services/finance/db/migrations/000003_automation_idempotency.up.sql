ALTER TABLE transactions ADD COLUMN automation_idempotency_key TEXT;
ALTER TABLE transactions ADD COLUMN automation_payload_hash TEXT;
CREATE UNIQUE INDEX transactions_automation_idempotency_key_idx ON transactions (automation_idempotency_key) WHERE automation_idempotency_key IS NOT NULL;
