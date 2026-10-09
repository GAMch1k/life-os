-- Durable retry scheduling. A retryable attempt records when the item may be
-- retried and why; the processor only picks raw items whose time has come.
-- 'failed' marks items whose automatic recovery is unlikely (operator
-- attention); they are no longer picked up automatically. Existing rows keep
-- NULL schedules and are eligible immediately, as before.
ALTER TABLE inbox_items ADD COLUMN next_retry_at TIMESTAMPTZ;
ALTER TABLE inbox_items ADD COLUMN retry_reason TEXT;
ALTER TABLE inbox_processing_attempts ADD COLUMN next_retry_at TIMESTAMPTZ;
ALTER TABLE inbox_items DROP CONSTRAINT inbox_items_state_check;
ALTER TABLE inbox_items ADD CONSTRAINT inbox_items_state_check CHECK (state IN ('raw', 'processing', 'human_review', 'failed', 'resolved', 'discarded'));
CREATE INDEX inbox_items_due_idx ON inbox_items (next_retry_at, created_at) WHERE state = 'raw';
