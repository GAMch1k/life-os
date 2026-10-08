ALTER TABLE audio_jobs ADD COLUMN lease_owner TEXT;
ALTER TABLE audio_jobs ADD COLUMN lease_expires_at TIMESTAMPTZ;
ALTER TABLE audio_jobs ADD COLUMN last_heartbeat_at TIMESTAMPTZ;
CREATE INDEX audio_jobs_reclaim_idx ON audio_jobs (lease_expires_at) WHERE status = 'processing';
