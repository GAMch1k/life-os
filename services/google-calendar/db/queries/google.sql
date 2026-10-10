-- The Google connection, its calendar list, and the inbound sync
-- (internal/google). Calendar is reached only through its API; these are
-- this service's own tables.

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
-- Also deletes its calendar list, exports, and waiting edits (ON DELETE
-- CASCADE).
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
-- initial_sync_enabled (and is then due for its first sync now); a known
-- one keeps the owner's choice, and its updated_at moves only when Google's
-- details changed.
INSERT INTO google_calendars (connection_id, google_calendar_id, summary, color, access_role, is_primary, sync_enabled, next_sync_at)
VALUES (sqlc.arg(connection_id), sqlc.arg(google_calendar_id), sqlc.arg(summary), sqlc.narg(color), sqlc.arg(access_role), sqlc.arg(is_primary),
        sqlc.arg(initial_sync_enabled), CASE WHEN sqlc.arg(initial_sync_enabled)::boolean THEN now() END)
ON CONFLICT (connection_id, google_calendar_id) DO UPDATE
SET summary = EXCLUDED.summary, color = EXCLUDED.color, access_role = EXCLUDED.access_role,
    is_primary = EXCLUDED.is_primary, updated_at = now()
WHERE (google_calendars.summary, google_calendars.color, google_calendars.access_role, google_calendars.is_primary)
    IS DISTINCT FROM (EXCLUDED.summary, EXCLUDED.color, EXCLUDED.access_role, EXCLUDED.is_primary);

-- name: ListGoogleCalendarsNotListed :many
-- The calendars Google no longer lists (DeleteGoogleCalendarsNotListed
-- drops them).
SELECT * FROM google_calendars
WHERE connection_id = sqlc.arg(connection_id) AND NOT (google_calendar_id = ANY (sqlc.arg(listed)::text[]));

-- name: DeleteGoogleCalendarsNotListed :execrows
-- Drops the calendars Google no longer lists.
DELETE FROM google_calendars
WHERE connection_id = sqlc.arg(connection_id) AND NOT (google_calendar_id = ANY (sqlc.arg(listed)::text[]));

-- name: LockGoogleCalendar :one
SELECT * FROM google_calendars WHERE id = $1 FOR UPDATE;

-- name: SetGoogleCalendarSync :one
-- Records the owner's choice. A change starts over: switched on, the
-- calendar is due for a full sync now; switched off, its sync state and
-- its Calendar source are cleared (the caller removes the source and
-- forgets the stored Google events).
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
    source_id = CASE WHEN sync_enabled = sqlc.arg(sync_enabled) OR sqlc.arg(sync_enabled)::boolean THEN source_id END,
    registration = CASE WHEN sync_enabled = sqlc.arg(sync_enabled) OR sqlc.arg(sync_enabled)::boolean THEN registration END,
    updated_at = CASE WHEN sync_enabled = sqlc.arg(sync_enabled) THEN updated_at ELSE now() END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetGoogleCalendarSource :one
-- Records the calendar's source in Calendar and what it was registered
-- with.
UPDATE google_calendars
SET source_id = sqlc.arg(source_id), registration = sqlc.arg(registration)
WHERE id = sqlc.arg(id)
RETURNING *;

-- Syncing (internal/google/sync.go). A sync takes the calendar's lease,
-- reads Google, then writes the stored Google events, sends Calendar what
-- changed, and records its outcome in one transaction holding the calendar
-- row.

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
-- Records a failed sync (the stored events stay) and releases the lease;
-- a calendar switched off meanwhile has nothing to record (no row).
UPDATE google_calendars
SET syncing_until = NULL, last_sync_error = sqlc.arg(last_sync_error), sync_failures = sync_failures + 1,
    next_sync_at = sqlc.arg(next_sync_at)
WHERE id = sqlc.arg(id) AND sync_enabled
RETURNING *;

-- name: ResyncGoogleCalendar :one
-- Forgets the sync token so the next sync lists every event again, due now.
UPDATE google_calendars
SET sync_token = NULL, next_sync_at = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id) AND sync_enabled
RETURNING *;

-- name: MarkGoogleCalendarDue :exec
-- Makes one calendar due now and its events derived again at that sync:
-- Google's answer to one of this service's writes is stored, and is to
-- reach Calendar right away (the listing then only echoes it).
UPDATE google_calendars
SET next_sync_at = LEAST(COALESCE(next_sync_at, sqlc.arg(now)::timestamptz), sqlc.arg(now)::timestamptz), derived_at = NULL
WHERE id = sqlc.arg(id) AND sync_enabled;

-- name: MarkGoogleCalendarsDue :exec
-- Makes the connection's synced calendars due now (after a reconnect).
UPDATE google_calendars SET next_sync_at = sqlc.arg(now)::timestamptz
WHERE connection_id = sqlc.arg(connection_id) AND sync_enabled;

-- name: ListGoogleEvents :many
SELECT * FROM google_events WHERE calendar_id = $1 ORDER BY event_id;

-- name: CountGoogleEvents :one
SELECT count(*)::integer FROM google_events WHERE calendar_id = $1;

-- name: DeleteGoogleEvents :exec
-- Forgets every stored Google event of the calendar.
DELETE FROM google_events WHERE calendar_id = $1;

-- name: UpsertGoogleEvents :exec
-- Stores Google events as sent: items is a JSON array of {"id",
-- "recurringEventId", "data"}, data being the event's JSON. A version older
-- than the stored one (by Google's updated time, when both have one) does
-- not replace it: a listing read before one of this service's own writes
-- cannot undo it.
INSERT INTO google_events (calendar_id, event_id, recurring_event_id, data)
SELECT sqlc.arg(calendar_id), item.id, NULLIF(item."recurringEventId", ''), item.data
FROM jsonb_to_recordset(sqlc.arg(items)::jsonb) AS item(id text, "recurringEventId" text, data jsonb)
ON CONFLICT (calendar_id, event_id) DO UPDATE
SET recurring_event_id = EXCLUDED.recurring_event_id, data = EXCLUDED.data, updated_at = now()
WHERE NOT (
    COALESCE(google_events.data ->> 'updated', '') ~ '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$'
    AND COALESCE(EXCLUDED.data ->> 'updated', '') ~ '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$'
    AND (EXCLUDED.data ->> 'updated')::timestamptz < (google_events.data ->> 'updated')::timestamptz
);

-- name: DeleteGoogleEventsByID :exec
-- Forgets Google events, and the occurrences of the recurring ones among them.
DELETE FROM google_events
WHERE calendar_id = sqlc.arg(calendar_id)
  AND (event_id = ANY (sqlc.arg(event_ids)::text[]) OR recurring_event_id = ANY (sqlc.arg(event_ids)::text[]));

-- name: DeleteGoogleInstances :exec
-- Forgets stored instances by id (not their series).
DELETE FROM google_events
WHERE calendar_id = sqlc.arg(calendar_id) AND event_id = ANY (sqlc.arg(event_ids)::text[]);

-- name: GetStoredGoogleEvent :one
SELECT * FROM google_events WHERE calendar_id = sqlc.arg(calendar_id) AND event_id = sqlc.arg(event_id);

-- name: ListStoredGoogleInstances :many
-- A recurring event's stored changed or cancelled instances.
SELECT * FROM google_events
WHERE calendar_id = sqlc.arg(calendar_id) AND recurring_event_id = sqlc.arg(recurring_event_id)
ORDER BY event_id;

-- name: ListGoogleEventETags :many
-- The etags of stored Google events, to recognize this service's own
-- writes in a listing.
SELECT event_id, COALESCE(data ->> 'etag', '')::text AS etag
FROM google_events
WHERE calendar_id = sqlc.arg(calendar_id) AND event_id = ANY (sqlc.arg(event_ids)::text[]);

-- name: CreateGoogleOAuthState :exec
INSERT INTO google_oauth_states (state_hash, code_verifier, expires_at)
VALUES ($1, $2, $3);

-- name: ConsumeGoogleOAuthState :one
-- A state is used once: reading it deletes it.
DELETE FROM google_oauth_states WHERE state_hash = $1
RETURNING code_verifier, expires_at;

-- name: DeleteExpiredGoogleOAuthStates :exec
DELETE FROM google_oauth_states WHERE expires_at <= sqlc.arg(now)::timestamptz;

-- What Calendar was last told of each event of a calendar.

-- name: ListSentEvents :many
SELECT * FROM google_sent_events WHERE calendar_id = $1 ORDER BY external_uid;

-- name: CountSentEvents :one
SELECT count(*)::integer FROM google_sent_events WHERE calendar_id = $1;

-- name: UpsertSentEvents :exec
-- Records what Calendar now holds: items is a JSON array of {"uid",
-- "item"}.
INSERT INTO google_sent_events (calendar_id, external_uid, item)
SELECT sqlc.arg(calendar_id), sent.uid, sent.item
FROM jsonb_to_recordset(sqlc.arg(items)::jsonb) AS sent(uid text, item jsonb)
ON CONFLICT (calendar_id, external_uid) DO UPDATE SET item = EXCLUDED.item;

-- name: DeleteSentEvents :exec
DELETE FROM google_sent_events
WHERE calendar_id = sqlc.arg(calendar_id) AND external_uid = ANY (sqlc.arg(uids)::text[]);

-- name: ForgetSentEvent :exec
-- Calendar's version of an event is not Google's (such as after Google
-- refused an edit made in Life OS): the next sync sends it again.
UPDATE google_sent_events SET item = NULL
WHERE calendar_id = sqlc.arg(calendar_id) AND external_uid = sqlc.arg(external_uid);

-- name: ClearSentEvents :exec
DELETE FROM google_sent_events WHERE calendar_id = $1;

-- Series Calendar created in a source that this service created in Google.

-- name: AddRekey :exec
INSERT INTO google_rekeys (calendar_id, google_event_id, previous_uid)
VALUES ($1, $2, $3)
ON CONFLICT (calendar_id, google_event_id) DO UPDATE SET previous_uid = EXCLUDED.previous_uid;

-- name: ListRekeys :many
SELECT * FROM google_rekeys WHERE calendar_id = $1 ORDER BY google_event_id;

-- name: GetRekeyByPreviousUID :one
SELECT * FROM google_rekeys WHERE calendar_id = sqlc.arg(calendar_id) AND previous_uid = sqlc.arg(previous_uid);

-- name: DeleteRekeys :exec
DELETE FROM google_rekeys
WHERE calendar_id = sqlc.arg(calendar_id) AND google_event_id = ANY (sqlc.arg(google_event_ids)::text[]);

-- Sources to remove from Calendar.

-- name: QueueSourceRemoval :exec
INSERT INTO google_source_removals (external_id, name, events, reason, requested_at, next_attempt_at)
VALUES (sqlc.arg(external_id), sqlc.arg(name), sqlc.arg(events), sqlc.arg(reason), sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz)
ON CONFLICT (external_id) DO UPDATE
SET name = EXCLUDED.name, events = GREATEST(google_source_removals.events, EXCLUDED.events), reason = EXCLUDED.reason,
    next_attempt_at = EXCLUDED.next_attempt_at;

-- name: GetSourceRemoval :one
SELECT * FROM google_source_removals WHERE external_id = $1;

-- name: ListDueSourceRemovals :many
SELECT * FROM google_source_removals WHERE next_attempt_at <= sqlc.arg(now)::timestamptz ORDER BY requested_at, external_id;

-- name: DeleteSourceRemoval :exec
DELETE FROM google_source_removals WHERE external_id = $1;

-- name: FailSourceRemoval :exec
UPDATE google_source_removals
SET attempts = attempts + 1, last_error = sqlc.arg(last_error), next_attempt_at = sqlc.arg(next_attempt_at)::timestamptz
WHERE external_id = sqlc.arg(external_id);
