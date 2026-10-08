DROP INDEX IF EXISTS transactions_automation_idempotency_key_idx;
ALTER TABLE transactions DROP COLUMN IF EXISTS automation_payload_hash;
ALTER TABLE transactions DROP COLUMN IF EXISTS automation_idempotency_key;
