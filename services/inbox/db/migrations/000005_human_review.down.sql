ALTER TABLE inbox_processing_attempts DROP CONSTRAINT inbox_processing_attempts_status_check;
ALTER TABLE inbox_processing_attempts ADD CONSTRAINT inbox_processing_attempts_status_check CHECK (status IN ('pending', 'processing', 'processed', 'failed', 'retryable'));

DROP INDEX IF EXISTS inbox_items_human_review_idx;
ALTER TABLE inbox_items DROP CONSTRAINT inbox_items_state_check;
ALTER TABLE inbox_items ADD CONSTRAINT inbox_items_state_check CHECK (state IN ('raw', 'processing', 'resolved', 'discarded'));
