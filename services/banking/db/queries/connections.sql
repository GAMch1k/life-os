-- name: ListConnections :many
-- Live connections first, then by when they were created (newest first).
SELECT * FROM bank_connections
ORDER BY (status = 'disconnected'), created_at DESC, id;

-- name: GetConnection :one
SELECT * FROM bank_connections WHERE id = $1;

-- name: GetLiveConnection :one
-- The bank's live (not disconnected) connection, if any.
SELECT * FROM bank_connections
WHERE provider = sqlc.arg(provider) AND institution = sqlc.arg(institution) AND country = sqlc.arg(country)
  AND status <> 'disconnected';

-- name: CreateConnection :one
INSERT INTO bank_connections (
    provider, institution, country, status, consent_valid_until, session_ciphertext, session_nonce,
    accounts, next_sync_at, connected_at, created_at, updated_at)
VALUES (
    sqlc.arg(provider), sqlc.arg(institution), sqlc.arg(country), 'pending', sqlc.narg(consent_valid_until),
    sqlc.arg(session_ciphertext), sqlc.arg(session_nonce), sqlc.arg(accounts), sqlc.arg(now), sqlc.arg(now),
    sqlc.arg(now), sqlc.arg(now))
RETURNING *;

-- name: ReconnectConnection :one
-- A new consent for a live connection (after expiry, an error, or while
-- still valid): a new session and account list, usable again. Its sync
-- history, cursor, and any import waiting for Finance are kept.
UPDATE bank_connections
SET status = CASE WHEN last_success_at IS NULL THEN 'pending' ELSE 'connected' END,
    consent_valid_until = sqlc.narg(consent_valid_until),
    session_ciphertext = sqlc.arg(session_ciphertext), session_nonce = sqlc.arg(session_nonce),
    accounts = sqlc.arg(accounts), next_sync_at = sqlc.arg(now), consecutive_failures = 0, last_error = NULL,
    consent_notice = NULL, consent_notice_for = NULL, connected_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND status <> 'disconnected'
RETURNING *;

-- name: RecordSyncSuccess :one
-- Finance accepted a sync: the connection is connected, its failures and
-- any waiting import are cleared. mapping_version is the mapping a full
-- read used (0 for a read of the days since the cursor only).
UPDATE bank_connections
SET status = CASE WHEN status = 'pending' THEN 'connected' ELSE status END,
    last_sync_at = sqlc.arg(now), last_success_at = sqlc.arg(now), next_sync_at = sqlc.narg(next_sync_at),
    consecutive_failures = 0, last_error = NULL, last_counts = sqlc.arg(last_counts),
    sync_cursor = sqlc.narg(sync_cursor), pending_import = NULL,
    mapping_version = GREATEST(mapping_version, sqlc.arg(mapping_version)::integer), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: RecordSyncFailure :one
-- A sync failed: one more failure in a row, the next attempt, and the
-- import still waiting for Finance (NULL when none).
UPDATE bank_connections
SET last_sync_at = sqlc.arg(now), next_sync_at = sqlc.narg(next_sync_at),
    consecutive_failures = consecutive_failures + 1, last_error = sqlc.arg(last_error),
    pending_import = sqlc.narg(pending_import), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetConnectionStatus :one
-- The provider ended the connection (expired) or refuses it (error): it is
-- not synced again until reconnected.
UPDATE bank_connections
SET status = sqlc.arg(status), last_error = sqlc.narg(last_error), next_sync_at = NULL, updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND status IN ('pending', 'connected', 'expired', 'error')
RETURNING *;

-- name: SetNextSync :exec
UPDATE bank_connections SET next_sync_at = sqlc.narg(next_sync_at), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id);

-- name: TakeUnattendedRead :one
-- Counts one read without the owner present on day, unless max were
-- already made that day (no row).
UPDATE bank_connections
SET unattended_reads_count = CASE WHEN unattended_reads_date = sqlc.arg(day)::date THEN unattended_reads_count + 1 ELSE 1 END,
    unattended_reads_date = sqlc.arg(day)::date
WHERE id = sqlc.arg(id)
  AND (unattended_reads_date IS DISTINCT FROM sqlc.arg(day)::date OR unattended_reads_count < sqlc.arg(max)::integer)
RETURNING unattended_reads_count;

-- name: ClaimManualSync :one
-- Records a sync the owner asked for, unless one was asked for after
-- not_before (no row: too soon).
UPDATE bank_connections
SET last_manual_sync_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND (last_manual_sync_at IS NULL OR last_manual_sync_at <= sqlc.arg(not_before))
RETURNING last_manual_sync_at;

-- name: ReleaseManualSync :exec
-- Gives back a claimed manual sync that never reached the bank.
UPDATE bank_connections SET last_manual_sync_at = sqlc.narg(previous)
WHERE id = sqlc.arg(id) AND last_manual_sync_at = sqlc.arg(claimed);

-- name: DisconnectConnection :one
-- The owner removed the connection: its session is wiped and it is never
-- synced again; Finance still has to archive its accounts.
UPDATE bank_connections
SET status = 'disconnected', session_ciphertext = NULL, session_nonce = NULL, next_sync_at = NULL,
    pending_import = NULL, archive_pending = true, disconnected_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ClearArchivePending :exec
UPDATE bank_connections SET archive_pending = false, updated_at = sqlc.arg(now) WHERE id = sqlc.arg(id);

-- name: SetConsentNotice :exec
UPDATE bank_connections
SET consent_notice = sqlc.arg(notice), consent_notice_for = sqlc.arg(notice_for), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id);
