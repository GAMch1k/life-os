DROP INDEX IF EXISTS inbox_processing_attempts_active_idx;
ALTER TABLE inbox_processing_attempts DROP COLUMN runtime_heartbeat_at;
ALTER TABLE inbox_processing_attempts DROP COLUMN runtime_session_id;
ALTER TABLE inbox_processing_attempts DROP COLUMN runtime_owner;
ALTER TABLE inbox_processing_attempts DROP COLUMN runtime_state;
