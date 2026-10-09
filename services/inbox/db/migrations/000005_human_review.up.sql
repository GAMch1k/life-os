ALTER TABLE inbox_items DROP CONSTRAINT inbox_items_state_check;
ALTER TABLE inbox_items ADD CONSTRAINT inbox_items_state_check CHECK (state IN ('raw', 'processing', 'human_review', 'resolved', 'discarded'));
CREATE INDEX inbox_items_human_review_idx ON inbox_items (created_at DESC) WHERE state = 'human_review';

ALTER TABLE inbox_processing_attempts DROP CONSTRAINT inbox_processing_attempts_status_check;
ALTER TABLE inbox_processing_attempts ADD CONSTRAINT inbox_processing_attempts_status_check CHECK (status IN ('pending', 'processing', 'human_review', 'processed', 'failed', 'retryable'));
