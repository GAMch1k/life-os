-- Runtime tracking for the agent run behind a processing attempt. The attempt
-- is created before the Codex process is spawned; the processor then records
-- its runtime state, owner, and heartbeat (and the Codex session id once the
-- CLI reports it). An attempt whose heartbeat stops is abandoned and can be
-- reconciled by a later processor. Existing rows keep NULLs.
ALTER TABLE inbox_processing_attempts ADD COLUMN runtime_state TEXT
    CHECK (runtime_state IN ('spawning', 'running', 'completed', 'failed', 'timed_out', 'cancelled'));
ALTER TABLE inbox_processing_attempts ADD COLUMN runtime_owner TEXT;
ALTER TABLE inbox_processing_attempts ADD COLUMN runtime_session_id TEXT;
ALTER TABLE inbox_processing_attempts ADD COLUMN runtime_heartbeat_at TIMESTAMPTZ;
CREATE INDEX inbox_processing_attempts_active_idx ON inbox_processing_attempts (updated_at) WHERE status IN ('pending', 'processing');
