-- name: CreateSource :one
-- A null default_priority is medium. A feed belongs to the calendar-feeds
-- integration under its own id (see the integration API).
WITH new AS (SELECT gen_random_uuid() AS id)
INSERT INTO calendar_sources (id, kind, name, url, enabled, filter_include, filter_exclude, default_reminders, next_refresh_at, default_priority,
                              integration, external_id, provider)
VALUES ((SELECT id FROM new), sqlc.arg(kind), sqlc.arg(name), sqlc.arg(url), sqlc.arg(enabled), sqlc.arg(filter_include), sqlc.arg(filter_exclude),
        sqlc.narg(default_reminders), sqlc.arg(next_refresh_at), COALESCE(sqlc.narg(default_priority)::text, 'medium'),
        'calendar-feeds', (SELECT id FROM new)::text, 'ICS')
RETURNING *;

-- name: GetSource :one
SELECT * FROM calendar_sources WHERE id = $1;

-- name: ListSources :many
-- The feeds (ICS sources). Google calendars are sources too, managed through
-- the Google connection (google_calendars), and are not listed here.
SELECT * FROM calendar_sources WHERE kind = 'ics' ORDER BY created_at, id;

-- name: UpdateSource :one
-- Writes the whole merged configuration, guarded by the updated_at it was
-- read with. reset_validators drops the conditional-request validators (a
-- changed URL or filter needs a full download); next_refresh_at, when given,
-- reschedules the source; a null default_priority keeps the source's.
UPDATE calendar_sources
SET name = sqlc.arg(name), url = sqlc.arg(url), enabled = sqlc.arg(enabled),
    filter_include = sqlc.arg(filter_include)::text[], filter_exclude = sqlc.arg(filter_exclude)::text[],
    default_reminders = sqlc.narg(default_reminders), default_priority = COALESCE(sqlc.narg(default_priority)::text, default_priority),
    etag = CASE WHEN sqlc.arg(reset_validators)::boolean THEN NULL ELSE etag END,
    last_modified = CASE WHEN sqlc.arg(reset_validators)::boolean THEN NULL ELSE last_modified END,
    next_refresh_at = COALESCE(sqlc.narg(next_refresh_at)::timestamptz, next_refresh_at),
    updated_at = now()
WHERE id = sqlc.arg(id) AND updated_at = sqlc.arg(expected_updated_at)
RETURNING *;

-- name: DeleteSource :execrows
-- Deletes a feed and its imported events (ON DELETE CASCADE). A Google
-- calendar's source goes when its calendar stops syncing.
DELETE FROM calendar_sources WHERE id = $1 AND kind = 'ics';

-- name: ListDueSources :many
-- Enabled feeds whose next refresh is due and that nobody is refreshing
-- (only those Calendar refreshes itself, not an integration's).
SELECT id FROM calendar_sources
WHERE enabled
  AND kind = 'ics'
  AND in_process
  AND next_refresh_at <= sqlc.arg(now)::timestamptz
  AND (refreshing_until IS NULL OR refreshing_until <= sqlc.arg(now)::timestamptz)
ORDER BY next_refresh_at, id;

-- name: ClaimSourceRefresh :one
-- Takes the source's refresh lease: one refresh per source at a time, across
-- processes too. A lease left by a crashed refresh expires and is taken over.
UPDATE calendar_sources
SET refreshing_until = sqlc.arg(lease_until)::timestamptz, last_attempt_at = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id)
  AND enabled
  AND kind = 'ics'
  AND in_process
  AND (refreshing_until IS NULL OR refreshing_until <= sqlc.arg(now)::timestamptz)
RETURNING *;

-- name: ReleaseSourceRefresh :exec
-- Gives the lease up after a refresh that could not record an outcome.
UPDATE calendar_sources SET refreshing_until = NULL WHERE id = $1;

-- name: LockSource :one
SELECT * FROM calendar_sources WHERE id = $1 FOR UPDATE;

-- name: FinishSourceRefresh :one
-- Records a refresh outcome (the whole new state, computed by the caller
-- under LockSource) and releases the lease.
UPDATE calendar_sources
SET refreshing_until = NULL,
    last_outcome = sqlc.narg(last_outcome), last_error = sqlc.narg(last_error), last_counts = sqlc.narg(last_counts),
    last_success_at = sqlc.narg(last_success_at), consecutive_failures = sqlc.arg(consecutive_failures),
    etag = sqlc.narg(etag), last_modified = sqlc.narg(last_modified), next_refresh_at = sqlc.narg(next_refresh_at)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ListSourceSummaries :many
-- What an imported event shows of its source: its name, default reminders,
-- and default priority.
SELECT id, name, default_reminders, default_priority FROM calendar_sources ORDER BY id;

-- Preset sources are configured from Calendar's environment at startup
-- (EnsurePreset), inside one transaction holding LockPreset.

-- name: GetPresetSource :one
SELECT * FROM calendar_sources WHERE preset = sqlc.arg(preset)::text;

-- name: LockPreset :exec
-- Serializes ensuring a preset across processes until the transaction ends.
SELECT pg_advisory_xact_lock(hashtext('calendar_sources.preset:' || sqlc.arg(preset)::text));

-- name: LockPresetSource :one
SELECT * FROM calendar_sources WHERE preset = sqlc.arg(preset)::text FOR UPDATE;

-- name: LockAdoptableSource :one
-- A source added through the API for the preset's feed URL: the preset takes
-- it over rather than adding a second source for the same feed.
SELECT * FROM calendar_sources
WHERE preset IS NULL AND kind = 'ics' AND in_process AND url = sqlc.arg(url)
ORDER BY created_at, id
LIMIT 1
FOR UPDATE;

-- name: CreatePresetSource :one
WITH new AS (SELECT gen_random_uuid() AS id)
INSERT INTO calendar_sources (id, kind, name, url, enabled, filter_include, filter_exclude, default_reminders, next_refresh_at, preset, preset_config,
                              integration, external_id, provider)
VALUES ((SELECT id FROM new), sqlc.arg(kind), sqlc.arg(name), sqlc.arg(url), true, sqlc.arg(filter_include)::text[], sqlc.arg(filter_exclude)::text[],
        sqlc.narg(default_reminders), sqlc.arg(next_refresh_at), sqlc.arg(preset)::text, sqlc.arg(preset_config),
        'calendar-feeds', (SELECT id FROM new)::text, CASE WHEN sqlc.arg(preset)::text = 'zaw' THEN 'ZAW' ELSE 'ICS' END)
RETURNING *;

-- name: ApplySourcePreset :one
-- Writes a preset's configuration to its source and records it as applied.
-- reset_validators and next_refresh_at work as in UpdateSource; enabled and
-- the refresh state stay.
UPDATE calendar_sources
SET name = sqlc.arg(name), url = sqlc.arg(url),
    filter_include = sqlc.arg(filter_include)::text[], filter_exclude = sqlc.arg(filter_exclude)::text[],
    default_reminders = sqlc.narg(default_reminders),
    preset = sqlc.arg(preset)::text, preset_config = sqlc.arg(preset_config),
    provider = CASE WHEN sqlc.arg(preset)::text = 'zaw' THEN 'ZAW' ELSE provider END,
    etag = CASE WHEN sqlc.arg(reset_validators)::boolean THEN NULL ELSE etag END,
    last_modified = CASE WHEN sqlc.arg(reset_validators)::boolean THEN NULL ELSE last_modified END,
    next_refresh_at = COALESCE(sqlc.narg(next_refresh_at)::timestamptz, next_refresh_at),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- Imported events are written only by Calendar's source adapter, inside the
-- refresh transaction that holds LockSource.

-- name: ListSourceEvents :many
SELECT * FROM events WHERE source_id = $1 ORDER BY id FOR UPDATE;

-- name: InsertImportedEvent :one
-- A source's new event. A series (Google) has its rule; reminders and
-- use_default_reminders are the source's (an ICS feed gives the column
-- defaults: none of its own, following the source's defaults), and
-- source_reminders records them (null for a feed). priority is the event's
-- own priority as its source gives it (Google, through a Life OS extended
-- property); null follows the source's default.
INSERT INTO events (
    title, description, location, all_day, start_at, end_at, start_date, end_date, time_zone,
    recurrence_frequency, recurrence_interval, recurrence_until, recurrence_by_weekday, recurrence_count,
    reminders, use_default_reminders, source_reminders,
    status, cancelled_at, source_kind, source_id, external_uid, priority
)
VALUES (
    sqlc.arg(title), sqlc.narg(description), sqlc.narg(location), sqlc.arg(all_day), sqlc.narg(start_at), sqlc.narg(end_at),
    sqlc.narg(start_date), sqlc.narg(end_date), sqlc.narg(time_zone),
    sqlc.narg(recurrence_frequency), sqlc.narg(recurrence_interval), sqlc.narg(recurrence_until),
    sqlc.narg(recurrence_by_weekday)::text[], sqlc.narg(recurrence_count),
    sqlc.arg(reminders), sqlc.arg(use_default_reminders), sqlc.narg(source_reminders),
    sqlc.arg(status), sqlc.narg(cancelled_at), sqlc.arg(source_kind), sqlc.arg(source_id), sqlc.arg(external_uid), sqlc.narg(priority)
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
    status = sqlc.arg(status), cancelled_at = sqlc.narg(cancelled_at), removed_from_source_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id) AND source_id = sqlc.arg(source_id);

-- name: MarkImportedEventRemoved :exec
-- The event left its source: cancelled, kept as history, and marked removed.
UPDATE events
SET status = 'cancelled', cancelled_at = COALESCE(cancelled_at, sqlc.arg(now)::timestamptz),
    removed_from_source_at = sqlc.arg(now)::timestamptz, updated_at = now()
WHERE id = sqlc.arg(id) AND source_id = sqlc.arg(source_id);
