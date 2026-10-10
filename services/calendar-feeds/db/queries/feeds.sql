-- name: CreateFeed :one
-- A feed added through the API, already registered in Calendar; its first
-- refresh is due right away.
INSERT INTO feeds (id, source_id, name, url, enabled, filter_include, filter_exclude, next_refresh_at)
VALUES (sqlc.arg(id), sqlc.narg(source_id), sqlc.arg(name), sqlc.arg(url), sqlc.arg(enabled),
        sqlc.arg(filter_include)::text[], sqlc.arg(filter_exclude)::text[], sqlc.arg(next_refresh_at))
RETURNING *;

-- name: GetFeed :one
SELECT * FROM feeds WHERE id = $1;

-- name: ListFeeds :many
SELECT * FROM feeds ORDER BY created_at, id;

-- name: CountFeeds :one
SELECT count(*) FROM feeds;

-- name: UpdateFeedConfig :one
-- Writes the merged URL and filter, guarded by the updated_at they were read
-- with. reset_validators drops the conditional-request validators (a changed
-- URL or filter needs a full download); next_refresh_at, when given,
-- reschedules the feed.
UPDATE feeds
SET url = sqlc.arg(url), filter_include = sqlc.arg(filter_include)::text[], filter_exclude = sqlc.arg(filter_exclude)::text[],
    etag = CASE WHEN sqlc.arg(reset_validators)::boolean THEN NULL ELSE etag END,
    last_modified = CASE WHEN sqlc.arg(reset_validators)::boolean THEN NULL ELSE last_modified END,
    next_refresh_at = COALESCE(sqlc.narg(next_refresh_at)::timestamptz, next_refresh_at),
    updated_at = now()
WHERE id = sqlc.arg(id) AND updated_at = sqlc.arg(expected_updated_at)
RETURNING *;

-- name: DeleteFeed :execrows
DELETE FROM feeds WHERE id = $1;

-- name: MirrorSource :one
-- Records what Calendar reports of the feed's source: its id, the owner's
-- name for it, and whether the owner enabled it. A source switched back on
-- is due right away, as it was when Calendar refreshed it itself.
UPDATE feeds
SET source_id = sqlc.arg(source_id), name = sqlc.arg(name), enabled = sqlc.arg(enabled),
    next_refresh_at = CASE WHEN sqlc.arg(enabled)::boolean AND NOT enabled THEN sqlc.arg(now)::timestamptz ELSE next_refresh_at END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetFeedEnabled :exec
-- Calendar refused a sync because the owner disabled the source.
UPDATE feeds SET enabled = sqlc.arg(enabled) WHERE id = sqlc.arg(id);

-- name: SetRegisterPending :exec
UPDATE feeds SET register_pending = sqlc.arg(register_pending) WHERE id = sqlc.arg(id);

-- name: ListRegisterPending :many
SELECT * FROM feeds WHERE register_pending ORDER BY created_at, id;

-- name: ListDueFeeds :many
-- Enabled feeds whose next refresh is due and that nobody is refreshing.
SELECT id FROM feeds
WHERE enabled
  AND next_refresh_at <= sqlc.arg(now)::timestamptz
  AND (refreshing_until IS NULL OR refreshing_until <= sqlc.arg(now)::timestamptz)
ORDER BY next_refresh_at, id;

-- name: ClaimFeedRefresh :one
-- Takes the feed's refresh lease: one refresh per feed at a time, across
-- processes too. A lease left by a crashed refresh expires and is taken over.
UPDATE feeds
SET refreshing_until = sqlc.arg(lease_until)::timestamptz, last_attempt_at = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id)
  AND enabled
  AND (refreshing_until IS NULL OR refreshing_until <= sqlc.arg(now)::timestamptz)
RETURNING *;

-- name: ReleaseFeedRefresh :exec
-- Gives the lease up after a refresh that could not record an outcome.
UPDATE feeds SET refreshing_until = NULL WHERE id = $1;

-- name: LockFeed :one
SELECT * FROM feeds WHERE id = $1 FOR UPDATE;

-- name: FinishFeedRefresh :one
-- Records a refresh outcome (the whole new state, computed by the caller
-- under LockFeed) and releases the lease.
UPDATE feeds
SET refreshing_until = NULL,
    last_outcome = sqlc.narg(last_outcome), last_error = sqlc.narg(last_error), last_counts = sqlc.narg(last_counts),
    last_success_at = sqlc.narg(last_success_at), consecutive_failures = sqlc.arg(consecutive_failures),
    etag = sqlc.narg(etag), last_modified = sqlc.narg(last_modified), next_refresh_at = sqlc.narg(next_refresh_at)
WHERE id = sqlc.arg(id)
RETURNING *;

-- Preset feeds are configured from the environment at startup
-- (EnsurePreset), inside one transaction holding LockPreset.

-- name: GetPresetFeed :one
SELECT * FROM feeds WHERE preset = sqlc.arg(preset)::text;

-- name: LockPreset :exec
-- Serializes ensuring a preset across processes until the transaction ends.
SELECT pg_advisory_xact_lock(hashtext('feeds.preset:' || sqlc.arg(preset)::text));

-- name: LockPresetFeed :one
SELECT * FROM feeds WHERE preset = sqlc.arg(preset)::text FOR UPDATE;

-- name: LockAdoptableFeed :one
-- A feed added through the API for the preset's URL: the preset takes it
-- over rather than adding a second feed for the same URL.
SELECT * FROM feeds
WHERE preset IS NULL AND url = sqlc.arg(url)::text
ORDER BY created_at, id
LIMIT 1
FOR UPDATE;

-- name: CreatePresetFeed :one
-- A new preset feed, due right away; Calendar registers its source before
-- its first refresh (register_pending).
INSERT INTO feeds (name, url, enabled, filter_include, filter_exclude, next_refresh_at, preset, preset_config, register_pending)
VALUES (sqlc.arg(name), sqlc.arg(url), true, sqlc.arg(filter_include)::text[], sqlc.arg(filter_exclude)::text[],
        sqlc.arg(next_refresh_at), sqlc.arg(preset)::text, sqlc.arg(preset_config), true)
RETURNING *;

-- name: ApplyFeedPreset :one
-- Writes a preset's configuration to its feed and records it as applied.
-- reset_validators and next_refresh_at work as in UpdateFeedConfig;
-- register_pending asks for the source to be registered again in Calendar
-- (its name, provider, and default reminders); enabled and the refresh
-- state stay.
UPDATE feeds
SET url = sqlc.arg(url), filter_include = sqlc.arg(filter_include)::text[], filter_exclude = sqlc.arg(filter_exclude)::text[],
    preset = sqlc.arg(preset)::text, preset_config = sqlc.arg(preset_config), register_pending = true,
    etag = CASE WHEN sqlc.arg(reset_validators)::boolean THEN NULL ELSE etag END,
    last_modified = CASE WHEN sqlc.arg(reset_validators)::boolean THEN NULL ELSE last_modified END,
    next_refresh_at = COALESCE(sqlc.narg(next_refresh_at)::timestamptz, next_refresh_at),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;
