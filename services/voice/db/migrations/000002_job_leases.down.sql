DROP INDEX audio_jobs_reclaim_idx;
ALTER TABLE audio_jobs DROP COLUMN last_heartbeat_at;
ALTER TABLE audio_jobs DROP COLUMN lease_expires_at;
ALTER TABLE audio_jobs DROP COLUMN lease_owner;
