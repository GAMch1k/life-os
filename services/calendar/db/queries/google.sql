-- name: GetGoogleConnection :one
SELECT * FROM google_connections WHERE singleton;

-- name: CreateGoogleConnection :one
-- The one connection (a second insert violates the singleton key).
INSERT INTO google_connections (account_email, scopes, refresh_token_ciphertext, refresh_token_nonce, access_token_expires_at)
VALUES (sqlc.arg(account_email), sqlc.arg(scopes)::text[], sqlc.arg(refresh_token_ciphertext), sqlc.arg(refresh_token_nonce), sqlc.narg(access_token_expires_at))
RETURNING *;

-- name: ReconnectGoogleConnection :one
-- A new authorization of the same account: new token and scopes, usable again.
UPDATE google_connections
SET scopes = sqlc.arg(scopes)::text[], refresh_token_ciphertext = sqlc.arg(refresh_token_ciphertext),
    refresh_token_nonce = sqlc.arg(refresh_token_nonce), access_token_expires_at = sqlc.narg(access_token_expires_at),
    status = 'connected', last_error = NULL, connected_at = now(), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: RecordGoogleTokenRefresh :one
-- A refresh succeeded: the connection is usable. Google may rotate the
-- refresh token; a NULL ciphertext keeps the stored one.
UPDATE google_connections
SET access_token_expires_at = sqlc.arg(access_token_expires_at),
    refresh_token_ciphertext = COALESCE(sqlc.narg(refresh_token_ciphertext), refresh_token_ciphertext),
    refresh_token_nonce = COALESCE(sqlc.narg(refresh_token_nonce), refresh_token_nonce),
    status = 'connected', last_error = NULL, updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetGoogleConnectionStatus :one
UPDATE google_connections
SET status = sqlc.arg(status), last_error = sqlc.narg(last_error), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteGoogleConnection :execrows
-- Also deletes its calendar list (ON DELETE CASCADE).
DELETE FROM google_connections WHERE id = $1;

-- name: ListGoogleCalendars :many
-- The connection's calendars: the primary first, then by name.
SELECT * FROM google_calendars
WHERE connection_id = $1
ORDER BY is_primary DESC, lower(summary), google_calendar_id;

-- name: GetGoogleCalendar :one
SELECT * FROM google_calendars WHERE id = $1;

-- name: UpsertGoogleCalendar :exec
-- Records a calendar as Google lists it. A new one starts with
-- initial_sync_enabled; a known one keeps the owner's choice, and its
-- updated_at moves only when Google's details changed.
INSERT INTO google_calendars (connection_id, google_calendar_id, summary, color, access_role, is_primary, sync_enabled)
VALUES (sqlc.arg(connection_id), sqlc.arg(google_calendar_id), sqlc.arg(summary), sqlc.narg(color), sqlc.arg(access_role), sqlc.arg(is_primary), sqlc.arg(initial_sync_enabled))
ON CONFLICT (connection_id, google_calendar_id) DO UPDATE
SET summary = EXCLUDED.summary, color = EXCLUDED.color, access_role = EXCLUDED.access_role,
    is_primary = EXCLUDED.is_primary, updated_at = now()
WHERE (google_calendars.summary, google_calendars.color, google_calendars.access_role, google_calendars.is_primary)
    IS DISTINCT FROM (EXCLUDED.summary, EXCLUDED.color, EXCLUDED.access_role, EXCLUDED.is_primary);

-- name: DeleteGoogleCalendarsNotListed :execrows
-- Drops the calendars Google no longer lists.
DELETE FROM google_calendars
WHERE connection_id = sqlc.arg(connection_id) AND NOT (google_calendar_id = ANY (sqlc.arg(listed)::text[]));

-- name: SetGoogleCalendarSync :one
UPDATE google_calendars
SET sync_enabled = sqlc.arg(sync_enabled),
    updated_at = CASE WHEN sync_enabled = sqlc.arg(sync_enabled) THEN updated_at ELSE now() END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: CreateGoogleOAuthState :exec
INSERT INTO google_oauth_states (state_hash, code_verifier, expires_at)
VALUES ($1, $2, $3);

-- name: ConsumeGoogleOAuthState :one
-- A state is used once: reading it deletes it.
DELETE FROM google_oauth_states WHERE state_hash = $1
RETURNING code_verifier, expires_at;

-- name: DeleteExpiredGoogleOAuthStates :exec
DELETE FROM google_oauth_states WHERE expires_at <= sqlc.arg(now)::timestamptz;
