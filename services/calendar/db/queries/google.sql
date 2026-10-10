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

-- name: LockGoogleCalendar :one
SELECT * FROM google_calendars WHERE id = $1 FOR UPDATE;

-- name: SetGoogleCalendarSync :one
-- Records the owner's choice. A change starts over: switched on, the
-- calendar is due for a full sync now; switched off, its sync state is
-- cleared (the caller deletes its source, events, and stored Google events).
UPDATE google_calendars
SET sync_enabled = sqlc.arg(sync_enabled),
    sync_token = CASE WHEN sync_enabled = sqlc.arg(sync_enabled) THEN sync_token END,
    sync_window_start = CASE WHEN sync_enabled = sqlc.arg(sync_enabled) THEN sync_window_start END,
    next_sync_at = CASE WHEN sync_enabled = sqlc.arg(sync_enabled) THEN next_sync_at
                        WHEN sqlc.arg(sync_enabled)::boolean THEN sqlc.arg(now)::timestamptz END,
    last_sync_error = CASE WHEN sync_enabled = sqlc.arg(sync_enabled) THEN last_sync_error END,
    sync_failures = CASE WHEN sync_enabled = sqlc.arg(sync_enabled) THEN sync_failures ELSE 0 END,
    last_sync_counts = CASE WHEN sync_enabled = sqlc.arg(sync_enabled) THEN last_sync_counts END,
    last_synced_at = CASE WHEN sync_enabled = sqlc.arg(sync_enabled) THEN last_synced_at END,
    last_sync_attempt_at = CASE WHEN sync_enabled = sqlc.arg(sync_enabled) THEN last_sync_attempt_at END,
    derived_at = CASE WHEN sync_enabled = sqlc.arg(sync_enabled) THEN derived_at END,
    updated_at = CASE WHEN sync_enabled = sqlc.arg(sync_enabled) THEN updated_at ELSE now() END
WHERE id = sqlc.arg(id)
RETURNING *;

-- Syncing (internal/google/sync.go). A sync takes the calendar's lease, reads
-- Google, then writes the stored Google events, the derived Calendar events,
-- and its outcome in one transaction holding the calendar row.

-- name: ListDueGoogleCalendars :many
-- The connection's calendars chosen to sync whose next sync is due and that
-- nobody is syncing.
SELECT id FROM google_calendars
WHERE connection_id = sqlc.arg(connection_id)
  AND sync_enabled
  AND next_sync_at <= sqlc.arg(now)::timestamptz
  AND (syncing_until IS NULL OR syncing_until <= sqlc.arg(now)::timestamptz)
ORDER BY next_sync_at, id;

-- name: ClaimGoogleCalendarSync :one
-- Takes the calendar's sync lease: one sync per calendar at a time, across
-- processes too. A lease left by a crashed sync expires and is taken over.
UPDATE google_calendars
SET syncing_until = sqlc.arg(lease_until)::timestamptz, last_sync_attempt_at = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id)
  AND sync_enabled
  AND (syncing_until IS NULL OR syncing_until <= sqlc.arg(now)::timestamptz)
RETURNING *;

-- name: ReleaseGoogleCalendarSync :exec
-- Gives the lease up after a sync that could not record an outcome; due_at,
-- when given, reschedules it.
UPDATE google_calendars
SET syncing_until = NULL, next_sync_at = COALESCE(sqlc.narg(due_at)::timestamptz, next_sync_at)
WHERE id = sqlc.arg(id);

-- name: FinishGoogleCalendarSync :one
-- Records a successful sync and releases the lease.
UPDATE google_calendars
SET syncing_until = NULL, sync_token = sqlc.arg(sync_token), sync_window_start = sqlc.narg(sync_window_start),
    time_zone = sqlc.narg(time_zone), next_sync_at = sqlc.arg(next_sync_at), last_synced_at = sqlc.arg(now)::timestamptz,
    last_sync_error = NULL, sync_failures = 0, last_sync_counts = COALESCE(sqlc.narg(last_sync_counts), last_sync_counts),
    derived_at = COALESCE(sqlc.narg(derived_at)::timestamptz, derived_at)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: FailGoogleCalendarSync :one
-- Records a failed sync (the stored events stay) and releases the lease.
-- drop_token forgets the sync token, so the next sync lists everything.
UPDATE google_calendars
SET syncing_until = NULL, last_sync_error = sqlc.arg(last_sync_error), sync_failures = sync_failures + 1,
    next_sync_at = sqlc.arg(next_sync_at),
    sync_token = CASE WHEN sqlc.arg(drop_token)::boolean THEN NULL ELSE sync_token END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ResyncGoogleCalendar :one
-- Forgets the sync token so the next sync lists every event again, due now.
UPDATE google_calendars
SET sync_token = NULL, next_sync_at = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id) AND sync_enabled
RETURNING *;

-- name: MarkGoogleCalendarsDue :exec
-- Makes the connection's synced calendars due now (after a reconnect).
UPDATE google_calendars SET next_sync_at = sqlc.arg(now)::timestamptz
WHERE connection_id = sqlc.arg(connection_id) AND sync_enabled;

-- name: GetGoogleSource :one
SELECT * FROM calendar_sources WHERE google_calendar_id = $1;

-- name: CreateGoogleSource :one
-- A synced calendar's source. Its url is the calendar's events endpoint at
-- Google, for reference; it is never fetched as a feed.
INSERT INTO calendar_sources (kind, name, url, enabled, default_reminders, google_calendar_id)
VALUES ('google', sqlc.arg(name), sqlc.arg(url), true, sqlc.narg(default_reminders), sqlc.arg(google_calendar_id))
RETURNING *;

-- name: UpdateGoogleSource :one
-- Follows the Google calendar's name and default reminders.
UPDATE calendar_sources
SET name = sqlc.arg(name), default_reminders = sqlc.narg(default_reminders), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteGoogleSource :exec
-- Also deletes the source's events (ON DELETE CASCADE).
DELETE FROM calendar_sources WHERE id = $1 AND kind = 'google';

-- name: ListGoogleSources :many
-- The sources of a connection's calendars with how many events each holds,
-- for recording what disconnecting or unlisting removes. calendar_ids, when
-- not null, narrows them to those calendars.
SELECT calendar_sources.id, calendar_sources.name, google_calendars.id AS calendar_id,
       (SELECT count(*) FROM events WHERE events.source_id = calendar_sources.id)::integer AS events
FROM calendar_sources
JOIN google_calendars ON google_calendars.id = calendar_sources.google_calendar_id
WHERE google_calendars.connection_id = sqlc.arg(connection_id)
  AND (sqlc.narg(calendar_ids)::uuid[] IS NULL OR google_calendars.id = ANY (sqlc.narg(calendar_ids)::uuid[]))
ORDER BY calendar_sources.name, calendar_sources.id;

-- name: ListGoogleCalendarsNotListed :many
-- The calendars Google no longer lists (DeleteGoogleCalendarsNotListed
-- drops them).
SELECT id FROM google_calendars
WHERE connection_id = sqlc.arg(connection_id) AND NOT (google_calendar_id = ANY (sqlc.arg(listed)::text[]));

-- name: CountSourceEvents :one
SELECT count(*)::integer FROM events WHERE source_id = $1;

-- name: ListGoogleEvents :many
SELECT * FROM google_events WHERE calendar_id = $1 ORDER BY event_id;

-- name: CountGoogleEvents :one
SELECT count(*)::integer FROM google_events WHERE calendar_id = $1;

-- name: DeleteGoogleEvents :exec
-- Forgets every stored Google event of the calendar.
DELETE FROM google_events WHERE calendar_id = $1;

-- name: UpsertGoogleEvents :exec
-- Stores Google events as sent: items is a JSON array of {"id",
-- "recurringEventId", "data"}, data being the event's JSON.
INSERT INTO google_events (calendar_id, event_id, recurring_event_id, data)
SELECT sqlc.arg(calendar_id), item.id, NULLIF(item."recurringEventId", ''), item.data
FROM jsonb_to_recordset(sqlc.arg(items)::jsonb) AS item(id text, "recurringEventId" text, data jsonb)
ON CONFLICT (calendar_id, event_id) DO UPDATE
SET recurring_event_id = EXCLUDED.recurring_event_id, data = EXCLUDED.data, updated_at = now();

-- name: DeleteGoogleEventsByID :exec
-- Forgets Google events, and the occurrences of the recurring ones among them.
DELETE FROM google_events
WHERE calendar_id = sqlc.arg(calendar_id)
  AND (event_id = ANY (sqlc.arg(event_ids)::text[]) OR recurring_event_id = ANY (sqlc.arg(event_ids)::text[]));

-- name: CreateGoogleOAuthState :exec
INSERT INTO google_oauth_states (state_hash, code_verifier, expires_at)
VALUES ($1, $2, $3);

-- name: ConsumeGoogleOAuthState :one
-- A state is used once: reading it deletes it.
DELETE FROM google_oauth_states WHERE state_hash = $1
RETURNING code_verifier, expires_at;

-- name: DeleteExpiredGoogleOAuthStates :exec
DELETE FROM google_oauth_states WHERE expires_at <= sqlc.arg(now)::timestamptz;
