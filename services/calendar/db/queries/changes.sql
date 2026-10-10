-- The change feed of events created in Life OS (P11-06). Entries are
-- written by triggers (migration 000014) in the transaction of each change.

-- name: SetChangeActor :exec
-- Names who makes the changes of the current transaction in their change
-- feed entries (until the transaction ends).
SELECT set_config('calendar.actor', sqlc.arg(actor)::text, true);

-- name: GetChangeFeed :one
-- The feed's state: entries up to pruned_through were pruned; head is the
-- latest entry (or pruned_through when none is left).
SELECT pruned_through,
       GREATEST(pruned_through, (SELECT COALESCE(max(seq), 0) FROM calendar_event_changes))::bigint AS head
FROM calendar_change_feed
WHERE id;

-- name: ListEventChanges :many
-- The entries after the given one, oldest first.
SELECT seq, event_id, source_id, external_uid, actor, changed_at FROM calendar_event_changes
WHERE seq > sqlc.arg(after)::bigint
ORDER BY seq
LIMIT sqlc.arg(page_limit);

-- name: HasEventChangesAfter :one
SELECT EXISTS (SELECT 1 FROM calendar_event_changes WHERE seq > sqlc.arg(after)::bigint)::boolean;

-- name: PruneEventChanges :one
-- Deletes the entries recorded before the given instant (the oldest ones:
-- seq follows commit order) and records how far the feed was pruned.
WITH cutoff AS (
    SELECT max(seq) AS seq FROM calendar_event_changes WHERE changed_at < sqlc.arg(before)::timestamptz
), pruned AS (
    DELETE FROM calendar_event_changes WHERE seq <= (SELECT seq FROM cutoff) RETURNING seq
)
UPDATE calendar_change_feed
SET pruned_through = GREATEST(pruned_through, COALESCE((SELECT max(seq) FROM pruned), 0)),
    pruned_at = sqlc.arg(now)::timestamptz
WHERE id
RETURNING pruned_through, (SELECT count(*) FROM pruned)::integer AS pruned;

-- name: HasUnhandledChanges :one
-- Whether the feed has changes after the given entry to the source's
-- events made by anyone but actor (the integration syncing it).
SELECT EXISTS (
    SELECT 1 FROM calendar_event_changes
    WHERE seq > sqlc.arg(after)::bigint AND source_id = sqlc.arg(source_id) AND actor <> sqlc.arg(actor)::text
)::boolean;
