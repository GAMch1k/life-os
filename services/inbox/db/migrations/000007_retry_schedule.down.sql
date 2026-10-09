DROP INDEX IF EXISTS inbox_items_due_idx;
UPDATE inbox_items SET state = 'raw' WHERE state = 'failed';
ALTER TABLE inbox_items DROP CONSTRAINT inbox_items_state_check;
ALTER TABLE inbox_items ADD CONSTRAINT inbox_items_state_check CHECK (state IN ('raw', 'processing', 'human_review', 'resolved', 'discarded'));
ALTER TABLE inbox_processing_attempts DROP COLUMN next_retry_at;
ALTER TABLE inbox_items DROP COLUMN retry_reason;
ALTER TABLE inbox_items DROP COLUMN next_retry_at;
