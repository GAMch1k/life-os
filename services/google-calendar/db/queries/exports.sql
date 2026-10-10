-- Two-way sync: the Life OS calendar, the export of Life OS events, the
-- owner's edits of Google events waiting to be written to Google, and the
-- position in Calendar's change feed that brings both.

-- name: SetGoogleExportEnabled :one
UPDATE google_connections
SET export_enabled = sqlc.arg(export_enabled), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetLifeOSCalendar :one
-- Records the Life OS calendar created in Google (and why, in note, when it
-- replaced one the owner deleted).
UPDATE google_connections
SET lifeos_calendar_id = sqlc.arg(lifeos_calendar_id), lifeos_calendar_created_at = sqlc.arg(created_at)::timestamptz,
    lifeos_calendar_note = sqlc.narg(note), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetLifeOSCalendarNote :exec
-- Says why the Life OS calendar could not be created (null: nothing to say).
UPDATE google_connections SET lifeos_calendar_note = sqlc.narg(note), updated_at = now() WHERE id = sqlc.arg(id);

-- name: MarkGoogleExportBackfilled :exec
UPDATE google_connections SET export_backfilled_at = sqlc.arg(now)::timestamptz, updated_at = now() WHERE id = sqlc.arg(id);

-- The change feed's position.

-- name: SetFeedPosition :exec
-- The feed was read up to cursor (Calendar's nextCursor); snapshot_started_at
-- is set while a snapshot is being read, null outside one.
UPDATE google_connections
SET feed_cursor = sqlc.narg(cursor), feed_snapshot_started_at = sqlc.narg(snapshot_started_at)::timestamptz,
    feed_read_at = sqlc.arg(now)::timestamptz, feed_error = NULL
WHERE id = sqlc.arg(id);

-- name: RestartFeed :exec
-- Reads the feed again from a snapshot (its cursor expired, or the Life OS
-- events to export are to be looked at again as a whole).
UPDATE google_connections SET feed_cursor = NULL, feed_snapshot_started_at = NULL WHERE id = sqlc.arg(id);

-- name: SetFeedError :exec
UPDATE google_connections SET feed_error = sqlc.arg(error), feed_error_at = sqlc.arg(now)::timestamptz WHERE id = sqlc.arg(id);

-- Exports of Life OS events.

-- name: GetGoogleExport :one
SELECT * FROM google_exports WHERE event_id = $1;

-- name: QueueGoogleExport :exec
-- Queues the export of a Life OS event's latest state (payload), due now.
INSERT INTO google_exports (event_id, connection_id, requested_at, next_attempt_at, payload, payload_at)
VALUES (sqlc.arg(event_id), sqlc.arg(connection_id), sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz, sqlc.arg(payload), sqlc.arg(now)::timestamptz)
ON CONFLICT (event_id) DO UPDATE
SET requested_at = EXCLUDED.requested_at, next_attempt_at = EXCLUDED.next_attempt_at, deleted = false,
    connection_id = EXCLUDED.connection_id, payload = EXCLUDED.payload, payload_at = EXCLUDED.payload_at,
    state = CASE WHEN google_exports.state = 'failed' THEN 'failed' ELSE 'pending' END, updated_at = now();

-- name: SetGoogleExportPayload :execrows
-- Records the event as the feed gives it without queuing an export (a
-- snapshot of an event already exported, or export off). A waiting export
-- that had no payload yet becomes due.
UPDATE google_exports
SET payload = sqlc.arg(payload), payload_at = sqlc.arg(now)::timestamptz, deleted = false,
    next_attempt_at = CASE WHEN state <> 'synced' AND next_attempt_at IS NULL THEN sqlc.arg(now)::timestamptz ELSE next_attempt_at END,
    updated_at = now()
WHERE event_id = sqlc.arg(event_id);

-- name: DeleteUnexportedGoogleExport :execrows
-- Forgets the export of a deleted event that never reached Google.
DELETE FROM google_exports WHERE event_id = $1 AND google_event_id IS NULL;

-- name: MarkGoogleExportDeleted :exec
-- The event was deleted in Life OS: its copy in Google is deleted next.
UPDATE google_exports
SET deleted = true, payload = NULL, requested_at = sqlc.arg(now)::timestamptz, next_attempt_at = sqlc.arg(now)::timestamptz,
    state = CASE WHEN state = 'failed' THEN 'failed' ELSE 'pending' END, updated_at = now()
WHERE event_id = sqlc.arg(event_id);

-- name: ListUnseenGoogleExports :many
-- The exports a complete snapshot of the feed did not list (it started at
-- started_at): their events no longer exist.
SELECT * FROM google_exports
WHERE connection_id = sqlc.arg(connection_id) AND NOT deleted
  AND (payload_at IS NULL OR payload_at < sqlc.arg(started_at)::timestamptz);

-- name: ClaimDueGoogleExport :one
-- Takes the lease of the export due first; a lease left by a crashed
-- attempt expires.
UPDATE google_exports
SET exporting_until = sqlc.arg(lease_until)::timestamptz
WHERE event_id = (
    SELECT due.event_id FROM google_exports AS due
    WHERE due.next_attempt_at <= sqlc.arg(now)::timestamptz
      AND (due.exporting_until IS NULL OR due.exporting_until <= sqlc.arg(now)::timestamptz)
    ORDER BY due.next_attempt_at, due.event_id
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: FinishGoogleExport :one
-- Records a successful export. A change queued while it ran (requested_at
-- moved) keeps the export pending and due now.
UPDATE google_exports
SET exporting_until = NULL, google_calendar_id = sqlc.narg(google_calendar_id), google_event_id = sqlc.narg(google_event_id),
    etag = sqlc.narg(etag), google_updated = sqlc.narg(google_updated), exported_exceptions = sqlc.arg(exported_exceptions)::text[],
    attempts = 0, last_error = NULL, exported_at = sqlc.arg(now)::timestamptz,
    state = CASE WHEN requested_at = sqlc.arg(claimed_requested_at)::timestamptz THEN 'synced' ELSE 'pending' END,
    next_attempt_at = CASE WHEN requested_at = sqlc.arg(claimed_requested_at)::timestamptz THEN NULL ELSE sqlc.arg(now)::timestamptz END,
    updated_at = now()
WHERE event_id = sqlc.arg(event_id)
RETURNING *;

-- name: FailGoogleExport :one
-- Records a failed attempt; it is retried at next_attempt_at.
UPDATE google_exports
SET exporting_until = NULL, attempts = attempts + 1, state = 'failed', last_error = sqlc.arg(last_error),
    next_attempt_at = sqlc.arg(next_attempt_at)::timestamptz, updated_at = now()
WHERE event_id = sqlc.arg(event_id)
RETURNING *;

-- name: ParkGoogleExport :exec
-- Gives the lease up without an attempt (export switched off, Google not
-- usable, or the event not known yet); next_attempt_at, when given,
-- retries then.
UPDATE google_exports
SET exporting_until = NULL, next_attempt_at = sqlc.narg(next_attempt_at)::timestamptz, updated_at = now()
WHERE event_id = sqlc.arg(event_id);

-- name: DeleteGoogleExport :exec
DELETE FROM google_exports WHERE event_id = $1;

-- name: LinkGoogleExport :exec
-- Records Google's current version of an exported event (after its change
-- in Google was applied in Life OS, or a conflict kept Google's version):
-- synced, unless a later change is queued. payload, when given, is the
-- event as Calendar now has it.
UPDATE google_exports
SET etag = sqlc.narg(etag), google_updated = sqlc.narg(google_updated),
    payload = COALESCE(sqlc.narg(payload), payload),
    state = CASE WHEN next_attempt_at IS NULL OR sqlc.arg(settle)::boolean THEN 'synced' ELSE state END,
    next_attempt_at = CASE WHEN sqlc.arg(settle)::boolean THEN NULL ELSE next_attempt_at END,
    last_error = CASE WHEN sqlc.arg(settle)::boolean THEN NULL ELSE last_error END,
    attempts = CASE WHEN sqlc.arg(settle)::boolean THEN 0 ELSE attempts END,
    updated_at = now()
WHERE event_id = sqlc.arg(event_id);

-- name: UnlinkGoogleExport :exec
-- The copy in Google is gone (deleted there): the event is no longer linked.
UPDATE google_exports
SET google_calendar_id = NULL, google_event_id = NULL, etag = NULL, google_updated = NULL, exported_exceptions = '{}',
    state = 'synced', next_attempt_at = NULL, attempts = 0, last_error = NULL, updated_at = now()
WHERE event_id = $1;

-- name: ResetGoogleExports :exec
-- The Life OS calendar was created anew: every exported event is written
-- to it again, and deletions have nothing left to delete.
WITH dropped AS (
    DELETE FROM google_exports WHERE google_exports.connection_id = sqlc.arg(connection_id) AND deleted
)
UPDATE google_exports
SET google_calendar_id = NULL, google_event_id = NULL, etag = NULL, google_updated = NULL, exported_exceptions = '{}',
    state = 'pending', next_attempt_at = sqlc.arg(now)::timestamptz, requested_at = sqlc.arg(now)::timestamptz, updated_at = now()
WHERE google_exports.connection_id = sqlc.arg(connection_id) AND NOT deleted;

-- name: MarkGoogleExportsDue :exec
-- Makes every waiting export due now (after reconnecting or switching
-- export on).
UPDATE google_exports
SET next_attempt_at = sqlc.arg(now)::timestamptz, updated_at = now()
WHERE connection_id = sqlc.arg(connection_id) AND state <> 'synced';

-- name: ListGoogleExports :many
-- The exports of the given events, for showing their sync state.
SELECT * FROM google_exports WHERE event_id = ANY (sqlc.arg(event_ids)::uuid[]);

-- name: ListLinkedGoogleExports :many
-- The exported events linked to a Life OS calendar, by Google event id.
SELECT * FROM google_exports
WHERE google_calendar_id = sqlc.arg(google_calendar_id) AND google_event_id IS NOT NULL;

-- name: GoogleExportSummary :one
-- How the export is going: waiting and failing exports, and the latest
-- failure.
SELECT count(*) FILTER (WHERE state = 'pending')::integer AS pending,
       count(*) FILTER (WHERE state = 'failed')::integer AS failed,
       (SELECT last_error FROM google_exports AS latest
        WHERE latest.connection_id = sqlc.arg(connection_id) AND latest.state = 'failed'
        ORDER BY latest.updated_at DESC LIMIT 1) AS last_error,
       max(exported_at)::timestamptz AS last_exported_at
FROM google_exports
WHERE google_exports.connection_id = sqlc.arg(connection_id);

-- name: GetGoogleCalendarByGoogleID :one
SELECT * FROM google_calendars
WHERE connection_id = sqlc.arg(connection_id) AND google_calendar_id = sqlc.arg(google_calendar_id);

-- name: DeleteGoogleCalendar :exec
-- Drops one calendar row (with its stored events): the Life OS calendar
-- deleted in Google.
DELETE FROM google_calendars WHERE id = $1;

-- name: FindLifeOSCalendarCandidate :one
-- A calendar the account owns named like the Life OS calendar, adopted
-- instead of creating another one (after Google was disconnected and
-- connected again).
SELECT * FROM google_calendars
WHERE connection_id = sqlc.arg(connection_id) AND summary = sqlc.arg(summary) AND access_role = 'owner' AND NOT is_primary
ORDER BY created_at, id
LIMIT 1;

-- The owner's edits of Google events waiting to be written to Google.

-- name: QueuePush :exec
-- Queues writing an event's latest state (payload; null with deleted) to
-- Google, due now.
INSERT INTO google_pushes (event_id, calendar_id, external_uid, payload, deleted, requested_at, next_attempt_at)
VALUES (sqlc.arg(event_id), sqlc.arg(calendar_id), sqlc.arg(external_uid), sqlc.narg(payload), sqlc.arg(deleted), sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz)
ON CONFLICT (event_id) DO UPDATE
SET calendar_id = EXCLUDED.calendar_id, external_uid = EXCLUDED.external_uid, payload = EXCLUDED.payload, deleted = EXCLUDED.deleted,
    requested_at = EXCLUDED.requested_at, next_attempt_at = LEAST(google_pushes.next_attempt_at, EXCLUDED.next_attempt_at), updated_at = now();

-- name: ClaimDuePush :one
UPDATE google_pushes
SET pushing_until = sqlc.arg(lease_until)::timestamptz
WHERE event_id = (
    SELECT due.event_id FROM google_pushes AS due
    WHERE due.next_attempt_at <= sqlc.arg(now)::timestamptz
      AND (due.pushing_until IS NULL OR due.pushing_until <= sqlc.arg(now)::timestamptz)
    ORDER BY due.next_attempt_at, due.event_id
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: FinishPush :exec
-- The edit was written (or given up): forgotten, unless a later one was
-- queued while it ran.
DELETE FROM google_pushes WHERE event_id = sqlc.arg(event_id) AND requested_at = sqlc.arg(claimed_requested_at)::timestamptz;

-- name: ReleasePush :exec
UPDATE google_pushes SET pushing_until = NULL WHERE event_id = $1;

-- name: FailPush :one
UPDATE google_pushes
SET pushing_until = NULL, attempts = attempts + 1, last_error = sqlc.arg(last_error),
    next_attempt_at = sqlc.arg(next_attempt_at)::timestamptz, updated_at = now()
WHERE event_id = sqlc.arg(event_id)
RETURNING *;

-- name: CountCalendarPushes :one
-- Edits of the calendar's events still to write to Google.
SELECT count(*)::integer FROM google_pushes WHERE calendar_id = $1;

-- name: MarkPushesDue :exec
-- Makes every waiting edit due now (after reconnecting).
UPDATE google_pushes SET next_attempt_at = sqlc.arg(now)::timestamptz
WHERE calendar_id IN (SELECT id FROM google_calendars WHERE connection_id = sqlc.arg(connection_id));

-- name: DeleteCalendarPushes :exec
DELETE FROM google_pushes WHERE calendar_id = $1;

-- name: PushSummary :one
-- How writing edits to Google goes, for the connection's status.
SELECT count(*)::integer AS pending,
       count(*) FILTER (WHERE attempts > 0)::integer AS failed,
       (SELECT last_error FROM google_pushes AS latest
        JOIN google_calendars AS latest_calendar ON latest_calendar.id = latest.calendar_id
        WHERE latest_calendar.connection_id = sqlc.arg(connection_id) AND latest.last_error IS NOT NULL
        ORDER BY latest.updated_at DESC LIMIT 1) AS last_error
FROM google_pushes
JOIN google_calendars ON google_calendars.id = google_pushes.calendar_id
WHERE google_calendars.connection_id = sqlc.arg(connection_id);
