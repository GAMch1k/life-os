-- Google Calendar two-way sync (P11-03): the Life OS calendar, the export
-- outbox of Life OS events, and what writes to synced Google events need.

-- name: SetGoogleExportEnabled :one
UPDATE google_connections
SET export_enabled = sqlc.arg(export_enabled), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetLifeOSCalendar :one
-- Records the Life OS calendar Calendar created in Google (and why, in
-- note, when it replaced one the owner deleted).
UPDATE google_connections
SET lifeos_calendar_id = sqlc.arg(lifeos_calendar_id), lifeos_calendar_created_at = sqlc.arg(created_at)::timestamptz,
    lifeos_calendar_note = sqlc.narg(note), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: LockGoogleConnection :one
SELECT * FROM google_connections WHERE id = $1 FOR UPDATE;

-- name: EnqueueGoogleExport :exec
-- Queues the export of a Life OS event's latest state, due now.
INSERT INTO google_exports (event_id, connection_id, requested_at, next_attempt_at)
VALUES (sqlc.arg(event_id), sqlc.arg(connection_id), sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz)
ON CONFLICT (event_id) DO UPDATE
SET requested_at = EXCLUDED.requested_at, next_attempt_at = EXCLUDED.next_attempt_at, deleted = false,
    connection_id = EXCLUDED.connection_id,
    state = CASE WHEN google_exports.state = 'failed' THEN 'failed' ELSE 'pending' END, updated_at = now();

-- name: EnqueueUpcomingGoogleExports :execrows
-- Queues every active Life OS event that is not over at now (a series that
-- has not ended) that has no export yet: switching export on, or connecting
-- Google, exports what is still ahead.
INSERT INTO google_exports (event_id, connection_id, requested_at, next_attempt_at)
SELECT events.id, sqlc.arg(connection_id), sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz
FROM events
WHERE events.source_kind = 'manual'
  AND events.status = 'active'
  AND CASE
        WHEN events.recurrence_frequency IS NOT NULL THEN events.recurrence_until IS NULL OR events.recurrence_until >= sqlc.arg(now)::timestamptz - interval '1 day'
        WHEN events.all_day THEN events.end_date >= (sqlc.arg(now)::timestamptz - interval '1 day')::date
        ELSE events.end_at > sqlc.arg(now)::timestamptz
      END
ON CONFLICT (event_id) DO NOTHING;

-- name: DeleteUnexportedGoogleExport :execrows
-- Forgets the export of a deleted event that never reached Google.
DELETE FROM google_exports WHERE event_id = $1 AND google_event_id IS NULL;

-- name: MarkGoogleExportDeleted :exec
-- The event was deleted in Life OS: its copy in Google is deleted next.
UPDATE google_exports
SET deleted = true, requested_at = sqlc.arg(now)::timestamptz, next_attempt_at = sqlc.arg(now)::timestamptz,
    state = CASE WHEN state = 'failed' THEN 'failed' ELSE 'pending' END, updated_at = now()
WHERE event_id = sqlc.arg(event_id);

-- name: GetGoogleExport :one
SELECT * FROM google_exports WHERE event_id = $1;

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
-- usable); next_attempt_at, when given, retries then.
UPDATE google_exports
SET exporting_until = NULL, next_attempt_at = sqlc.narg(next_attempt_at)::timestamptz, updated_at = now()
WHERE event_id = sqlc.arg(event_id);

-- name: DeleteGoogleExport :exec
DELETE FROM google_exports WHERE event_id = $1;

-- name: LinkGoogleExport :exec
-- Records Google's current version of an exported event (after its change
-- in Google was applied in Life OS, or a conflict kept Google's version):
-- synced, unless a later change is queued.
UPDATE google_exports
SET etag = sqlc.narg(etag), google_updated = sqlc.narg(google_updated),
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

-- name: GetGoogleCalendarBySource :one
-- The Google calendar a synced event's source belongs to.
SELECT google_calendars.* FROM google_calendars
JOIN calendar_sources ON calendar_sources.google_calendar_id = google_calendars.id
WHERE calendar_sources.id = $1;

-- name: GetGoogleCalendarByGoogleID :one
SELECT * FROM google_calendars
WHERE connection_id = sqlc.arg(connection_id) AND google_calendar_id = sqlc.arg(google_calendar_id);

-- name: GetStoredGoogleEvent :one
SELECT * FROM google_events WHERE calendar_id = sqlc.arg(calendar_id) AND event_id = sqlc.arg(event_id);

-- name: ListStoredGoogleInstances :many
-- A recurring event's stored changed or cancelled instances.
SELECT * FROM google_events
WHERE calendar_id = sqlc.arg(calendar_id) AND recurring_event_id = sqlc.arg(recurring_event_id)
ORDER BY event_id;

-- name: ListGoogleEventETags :many
-- The etags of stored Google events, to recognize Calendar's own writes in
-- a listing.
SELECT event_id, COALESCE(data ->> 'etag', '')::text AS etag
FROM google_events
WHERE calendar_id = sqlc.arg(calendar_id) AND event_id = ANY (sqlc.arg(event_ids)::text[]);

-- name: ListGoogleEventLinks :many
-- What the API shows of synced events: per source, its Google calendar
-- (name, access role, whether it is the Life OS calendar) and which of the
-- given external uids are a stored single event or a recurring event's
-- master (editable through Google), as opposed to an occurrence expanded
-- from a rule Calendar cannot hold or a lone instance.
SELECT calendar_sources.id AS source_id, google_calendars.id AS calendar_id, google_calendars.summary,
       google_calendars.access_role, (google_calendars.google_calendar_id = google_connections.lifeos_calendar_id)::boolean AS lifeos,
       COALESCE((SELECT array_agg(google_events.event_id ORDER BY google_events.event_id) FROM google_events
                 WHERE google_events.calendar_id = google_calendars.id AND google_events.recurring_event_id IS NULL
                   AND google_events.event_id = ANY (sqlc.arg(external_uids)::text[])), '{}')::text[] AS masters
FROM calendar_sources
JOIN google_calendars ON google_calendars.id = calendar_sources.google_calendar_id
JOIN google_connections ON google_connections.id = google_calendars.connection_id
WHERE calendar_sources.id = ANY (sqlc.arg(source_ids)::uuid[]);

-- name: GetEventBySourceUID :one
SELECT * FROM events WHERE source_id = sqlc.arg(source_id) AND external_uid = sqlc.arg(external_uid);

-- name: DeleteGoogleInstances :exec
-- Forgets stored instances by id (not their series).
DELETE FROM google_events
WHERE calendar_id = sqlc.arg(calendar_id) AND event_id = ANY (sqlc.arg(event_ids)::text[]);

-- name: DeleteGoogleCalendar :exec
-- Drops one calendar row (with its source and events): the Life OS
-- calendar deleted in Google.
DELETE FROM google_calendars WHERE id = $1;

-- name: FindLifeOSCalendarCandidate :one
-- A calendar the account owns named like the Life OS calendar, adopted
-- instead of creating another one (after Google was disconnected and
-- connected again).
SELECT * FROM google_calendars
WHERE connection_id = sqlc.arg(connection_id) AND summary = sqlc.arg(summary) AND access_role = 'owner' AND NOT is_primary
ORDER BY created_at, id
LIMIT 1;
