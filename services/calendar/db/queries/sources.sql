-- name: GetSource :one
SELECT * FROM calendar_sources WHERE id = $1;

-- name: ListSources :many
-- The feeds (ICS sources) and the sources integrations sync (external).
-- Legacy sources of Calendar's former in-process Google sync (kind google,
-- handed over to the google-calendar integration by migration 000016) are
-- not listed.
SELECT * FROM calendar_sources WHERE kind IN ('ics', 'external') ORDER BY created_at, id;

-- name: UpdateSource :one
-- Writes the owner's settings of a source (name, enabled, default
-- reminders, default priority), guarded by the updated_at they were read
-- with; a null default_priority keeps the source's. A feed's URL and filter
-- belong to the calendar-feeds integration.
UPDATE calendar_sources
SET name = sqlc.arg(name), enabled = sqlc.arg(enabled),
    default_reminders = sqlc.narg(default_reminders), default_priority = COALESCE(sqlc.narg(default_priority)::text, default_priority),
    updated_at = now()
WHERE id = sqlc.arg(id) AND updated_at = sqlc.arg(expected_updated_at)
RETURNING *;

-- name: DeleteSource :execrows
-- Deletes a feed or an integration's source and its imported events (ON
-- DELETE CASCADE).
DELETE FROM calendar_sources WHERE id = $1 AND kind IN ('ics', 'external');

-- name: ListSourceSummaries :many
-- What an imported event shows of its source: its name, default reminders,
-- default priority, integration, provider label, and whether it accepts
-- changes.
SELECT id, name, default_reminders, default_priority, integration, provider, read_only FROM calendar_sources ORDER BY id;

-- Imported events are written only by the reconciliation (an integration's
-- sync, or Calendar's in-process Google adapter), inside the transaction
-- that holds the source row.

-- name: ListSourceEvents :many
SELECT * FROM events WHERE source_id = $1 ORDER BY id FOR UPDATE;

-- name: InsertImportedEvent :one
-- A source's new event. A series (Google) has its rule; reminders and
-- use_default_reminders are the source's (an ICS feed gives the column
-- defaults: none of its own, following the source's defaults), and
-- source_reminders records them (null for a feed). priority is the event's
-- own priority as its source gives it (Google, through a Life OS extended
-- property); null follows the source's default. read_only_reason is why an
-- integration's event cannot be changed although its source accepts changes.
INSERT INTO events (
    title, description, location, all_day, start_at, end_at, start_date, end_date, time_zone,
    recurrence_frequency, recurrence_interval, recurrence_until, recurrence_by_weekday, recurrence_count,
    reminders, use_default_reminders, source_reminders,
    status, cancelled_at, source_kind, source_id, external_uid, priority, read_only_reason
)
VALUES (
    sqlc.arg(title), sqlc.narg(description), sqlc.narg(location), sqlc.arg(all_day), sqlc.narg(start_at), sqlc.narg(end_at),
    sqlc.narg(start_date), sqlc.narg(end_date), sqlc.narg(time_zone),
    sqlc.narg(recurrence_frequency), sqlc.narg(recurrence_interval), sqlc.narg(recurrence_until),
    sqlc.narg(recurrence_by_weekday)::text[], sqlc.narg(recurrence_count),
    sqlc.arg(reminders), sqlc.arg(use_default_reminders), sqlc.narg(source_reminders),
    sqlc.arg(status), sqlc.narg(cancelled_at), sqlc.arg(source_kind), sqlc.arg(source_id), sqlc.arg(external_uid), sqlc.narg(priority),
    sqlc.narg(read_only_reason)
)
RETURNING id;

-- name: UpdateImportedEvent :exec
-- Rewrites an imported event from its source (also restoring one the source
-- had removed). The source identity and the owner's priority stay. reminders, use_default_reminders,
-- and source_reminders change only when given: a feed never gives them (the
-- owner's reminders stay), a Google sync gives the source's new reminders
-- unless the owner changed the event's own. priority changes only with
-- set_priority: a Google event that carries Life OS's priority property.
UPDATE events
SET title = sqlc.arg(title), description = sqlc.narg(description), location = sqlc.narg(location),
    all_day = sqlc.arg(all_day), start_at = sqlc.narg(start_at), end_at = sqlc.narg(end_at),
    start_date = sqlc.narg(start_date), end_date = sqlc.narg(end_date), time_zone = sqlc.narg(time_zone),
    recurrence_frequency = sqlc.narg(recurrence_frequency), recurrence_interval = sqlc.narg(recurrence_interval),
    recurrence_until = sqlc.narg(recurrence_until), recurrence_by_weekday = sqlc.narg(recurrence_by_weekday)::text[],
    recurrence_count = sqlc.narg(recurrence_count),
    reminders = COALESCE(sqlc.narg(reminders), reminders),
    use_default_reminders = COALESCE(sqlc.narg(use_default_reminders), use_default_reminders),
    source_reminders = COALESCE(sqlc.narg(source_reminders), source_reminders),
    priority = CASE WHEN sqlc.arg(set_priority)::boolean THEN sqlc.narg(priority) ELSE priority END,
    read_only_reason = sqlc.narg(read_only_reason),
    status = sqlc.arg(status), cancelled_at = sqlc.narg(cancelled_at), removed_from_source_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id) AND source_id = sqlc.arg(source_id);

-- name: MarkImportedEventRemoved :exec
-- The event left its source: cancelled, kept as history, and marked removed.
UPDATE events
SET status = 'cancelled', cancelled_at = COALESCE(cancelled_at, sqlc.arg(now)::timestamptz),
    removed_from_source_at = sqlc.arg(now)::timestamptz, updated_at = now()
WHERE id = sqlc.arg(id) AND source_id = sqlc.arg(source_id);
