-- name: CreateSource :one
INSERT INTO calendar_sources (kind, name, url, enabled, filter_include, filter_exclude, default_reminders, next_refresh_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetSource :one
SELECT * FROM calendar_sources WHERE id = $1;

-- name: ListSources :many
SELECT * FROM calendar_sources ORDER BY created_at, id;

-- name: UpdateSource :one
-- Writes the whole merged configuration, guarded by the updated_at it was
-- read with. reset_validators drops the conditional-request validators (a
-- changed URL or filter needs a full download); next_refresh_at, when given,
-- reschedules the source.
UPDATE calendar_sources
SET name = sqlc.arg(name), url = sqlc.arg(url), enabled = sqlc.arg(enabled),
    filter_include = sqlc.arg(filter_include)::text[], filter_exclude = sqlc.arg(filter_exclude)::text[],
    default_reminders = sqlc.narg(default_reminders),
    etag = CASE WHEN sqlc.arg(reset_validators)::boolean THEN NULL ELSE etag END,
    last_modified = CASE WHEN sqlc.arg(reset_validators)::boolean THEN NULL ELSE last_modified END,
    next_refresh_at = COALESCE(sqlc.narg(next_refresh_at)::timestamptz, next_refresh_at),
    updated_at = now()
WHERE id = sqlc.arg(id) AND updated_at = sqlc.arg(expected_updated_at)
RETURNING *;

-- name: DeleteSource :execrows
-- Also deletes the source's imported events (ON DELETE CASCADE).
DELETE FROM calendar_sources WHERE id = $1;

-- name: ListDueSources :many
-- Enabled sources whose next refresh is due and that nobody is refreshing.
SELECT id FROM calendar_sources
WHERE enabled
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
-- What an imported event shows of its source: its name and default reminders.
SELECT id, name, default_reminders FROM calendar_sources ORDER BY id;

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
WHERE preset IS NULL AND kind = 'ics' AND url = sqlc.arg(url)
ORDER BY created_at, id
LIMIT 1
FOR UPDATE;

-- name: CreatePresetSource :one
INSERT INTO calendar_sources (kind, name, url, enabled, filter_include, filter_exclude, default_reminders, next_refresh_at, preset, preset_config)
VALUES (sqlc.arg(kind), sqlc.arg(name), sqlc.arg(url), true, sqlc.arg(filter_include)::text[], sqlc.arg(filter_exclude)::text[],
        sqlc.narg(default_reminders), sqlc.arg(next_refresh_at), sqlc.arg(preset)::text, sqlc.arg(preset_config))
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

-- name: InsertImportedEvent :exec
INSERT INTO events (
    title, description, location, all_day, start_at, end_at, start_date, end_date, time_zone,
    status, cancelled_at, source_kind, source_id, external_uid
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14);

-- name: UpdateImportedEvent :exec
-- Rewrites an imported event from its source (also restoring one the source
-- had removed). The owner's reminders and the source identity stay.
UPDATE events
SET title = sqlc.arg(title), description = sqlc.narg(description), location = sqlc.narg(location),
    all_day = sqlc.arg(all_day), start_at = sqlc.narg(start_at), end_at = sqlc.narg(end_at),
    start_date = sqlc.narg(start_date), end_date = sqlc.narg(end_date), time_zone = sqlc.narg(time_zone),
    status = sqlc.arg(status), cancelled_at = sqlc.narg(cancelled_at), removed_from_source_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id) AND source_id = sqlc.arg(source_id);

-- name: MarkImportedEventRemoved :exec
-- The event left its source: cancelled, kept as history, and marked removed.
UPDATE events
SET status = 'cancelled', cancelled_at = COALESCE(cancelled_at, sqlc.arg(now)::timestamptz),
    removed_from_source_at = sqlc.arg(now)::timestamptz, updated_at = now()
WHERE id = sqlc.arg(id) AND source_id = sqlc.arg(source_id);
