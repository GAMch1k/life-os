-- name: CreateEvent :one
-- A replay with the same automation idempotency key returns the original row.
INSERT INTO events (
    title, description, location, all_day, start_at, end_at, start_date, end_date, time_zone,
    recurrence_frequency, recurrence_interval, recurrence_until, reminders, use_default_reminders,
    status, cancelled_at, source_kind, source_id, external_uid, removed_from_source_at,
    external_provider, external_id, automation_idempotency_key, automation_payload_hash,
    recurrence_by_weekday, recurrence_count, priority
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27)
ON CONFLICT (automation_idempotency_key) WHERE automation_idempotency_key IS NOT NULL DO UPDATE SET automation_idempotency_key = EXCLUDED.automation_idempotency_key
RETURNING *;

-- name: GetEvent :one
SELECT * FROM events WHERE id = $1;

-- name: LockEvent :one
-- Reads an event for a change that also writes its occurrence exceptions,
-- holding it until the transaction ends.
SELECT * FROM events WHERE id = $1 FOR UPDATE;

-- name: ListEvents :many
-- Non-recurring events overlapping [range_start, range_end) (recurring ones
-- are expanded into occurrences by the caller, see ListRecurringEvents). An
-- all-day event spans from midnight of its first day to midnight after its
-- last day in time_zone, so its dates are matched as the owner's days. The
-- after_* arguments are the previous page's last row: a keyset cursor over
-- exactly the ORDER BY.
SELECT sqlc.embed(events), span.starts_at::timestamptz AS starts_at
FROM events
CROSS JOIN LATERAL (
    SELECT
        CASE WHEN events.all_day THEN events.start_date::timestamp AT TIME ZONE sqlc.arg(time_zone)::text ELSE events.start_at END AS starts_at,
        CASE WHEN events.all_day THEN (events.end_date + 1)::timestamp AT TIME ZONE sqlc.arg(time_zone)::text ELSE events.end_at END AS ends_at
) AS span
WHERE events.recurrence_frequency IS NULL
  AND span.starts_at < sqlc.arg(range_end)::timestamptz
  AND span.ends_at > sqlc.arg(range_start)::timestamptz
  AND (sqlc.arg(include_cancelled)::boolean OR events.status = 'active')
  AND (sqlc.narg(source_kind)::text IS NULL OR events.source_kind = sqlc.narg(source_kind)::text)
  AND (sqlc.narg(source_id)::uuid IS NULL OR events.source_id = sqlc.narg(source_id)::uuid)
  AND (
    sqlc.narg(after_id)::uuid IS NULL
    OR (span.starts_at, events.id) > (sqlc.narg(after_starts_at)::timestamptz, sqlc.narg(after_id)::uuid)
  )
ORDER BY span.starts_at, events.id
LIMIT sqlc.arg(page_limit);

-- name: ListRecurringEvents :many
-- Recurring events that may have an occurrence overlapping
-- [range_start, range_end): they start before range_end, and their last
-- possible occurrence (starting at recurrence_until, a day later for an
-- all-day series whose until is compared as the owner's date) ends after
-- range_start; or one of their occurrences was moved, possibly into the
-- range. The caller expands them; a personal calendar holds few of them.
SELECT sqlc.embed(events)
FROM events
CROSS JOIN LATERAL (
    SELECT
        CASE WHEN events.all_day THEN events.start_date::timestamp AT TIME ZONE sqlc.arg(time_zone)::text ELSE events.start_at END AS starts_at,
        CASE WHEN events.all_day THEN (events.end_date + 1)::timestamp AT TIME ZONE sqlc.arg(time_zone)::text ELSE events.end_at END AS ends_at
) AS span
WHERE events.recurrence_frequency IS NOT NULL
  AND (
    (
      span.starts_at < sqlc.arg(range_end)::timestamptz
      AND (events.recurrence_until IS NULL
           OR events.recurrence_until + (span.ends_at - span.starts_at) + CASE WHEN events.all_day THEN interval '1 day' ELSE interval '0' END > sqlc.arg(range_start)::timestamptz)
    )
    OR EXISTS (
      SELECT 1 FROM event_exceptions
      WHERE event_exceptions.event_id = events.id
        AND (event_exceptions.changes ->> 'startAt' IS NOT NULL OR event_exceptions.changes ->> 'startDate' IS NOT NULL)
    )
  )
  AND (sqlc.arg(include_cancelled)::boolean OR events.status = 'active')
  AND (sqlc.narg(source_kind)::text IS NULL OR events.source_kind = sqlc.narg(source_kind)::text)
  AND (sqlc.narg(source_id)::uuid IS NULL OR events.source_id = sqlc.narg(source_id)::uuid)
ORDER BY events.id;

-- name: SearchEvents :many
-- Non-recurring events whose title, description, or location contain every
-- pattern (ILIKE patterns, already escaped; recurring ones come from
-- SearchRecurringEvents). Relevance weighs a pattern found in the title
-- 3, in the location 2, in the description 1. Then events not over at the
-- anchor instant come first (bucket 0, soonest start first), then past events
-- (bucket 1, most recent start first): sort_key is the start in microseconds,
-- negated for past events, so one ascending keyset covers both directions.
SELECT sqlc.embed(events), ranked.relevance::integer AS relevance, ordering.bucket::integer AS bucket, ordering.sort_key::bigint AS sort_key
FROM events
CROSS JOIN LATERAL (
    SELECT
        CASE WHEN events.all_day THEN events.start_date::timestamp AT TIME ZONE sqlc.arg(time_zone)::text ELSE events.start_at END AS starts_at,
        CASE WHEN events.all_day THEN (events.end_date + 1)::timestamp AT TIME ZONE sqlc.arg(time_zone)::text ELSE events.end_at END AS ends_at
) AS span
CROSS JOIN LATERAL (
    SELECT COALESCE(SUM(
        CASE WHEN events.title ILIKE term.pattern THEN 3 ELSE 0 END
        + CASE WHEN COALESCE(events.location, '') ILIKE term.pattern THEN 2 ELSE 0 END
        + CASE WHEN COALESCE(events.description, '') ILIKE term.pattern THEN 1 ELSE 0 END
    ), 0) AS relevance
    FROM unnest(sqlc.arg(patterns)::text[]) AS term(pattern)
) AS ranked
CROSS JOIN LATERAL (
    SELECT
        CASE WHEN span.ends_at > sqlc.arg(anchor)::timestamptz THEN 0 ELSE 1 END AS bucket,
        CASE WHEN span.ends_at > sqlc.arg(anchor)::timestamptz THEN 1 ELSE -1 END
            * (EXTRACT(EPOCH FROM span.starts_at) * 1000000)::bigint AS sort_key
) AS ordering
WHERE events.recurrence_frequency IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM unnest(sqlc.arg(patterns)::text[]) AS term(pattern)
    WHERE NOT (events.title ILIKE term.pattern OR COALESCE(events.location, '') ILIKE term.pattern OR COALESCE(events.description, '') ILIKE term.pattern)
  )
  AND (sqlc.narg(range_end)::timestamptz IS NULL OR span.starts_at < sqlc.narg(range_end)::timestamptz)
  AND (sqlc.narg(range_start)::timestamptz IS NULL OR span.ends_at > sqlc.narg(range_start)::timestamptz)
  AND (sqlc.arg(include_cancelled)::boolean OR events.status = 'active')
  AND (sqlc.narg(source_kind)::text IS NULL OR events.source_kind = sqlc.narg(source_kind)::text)
  AND (sqlc.narg(source_id)::uuid IS NULL OR events.source_id = sqlc.narg(source_id)::uuid)
  AND (
    sqlc.narg(after_id)::uuid IS NULL
    OR (-ranked.relevance, ordering.bucket, ordering.sort_key, events.id)
       > (-sqlc.narg(after_relevance)::integer, sqlc.narg(after_bucket)::integer, sqlc.narg(after_sort_key)::bigint, sqlc.narg(after_id)::uuid)
  )
ORDER BY ranked.relevance DESC, ordering.bucket, ordering.sort_key, events.id
LIMIT sqlc.arg(page_limit);

-- name: SearchRecurringEvents :many
-- Recurring events whose title, description, or location contain every
-- pattern, with the same relevance as SearchEvents. The caller picks their
-- occurrences; the range only drops series that start after range_end and
-- have no moved occurrence.
SELECT sqlc.embed(events), ranked.relevance::integer AS relevance
FROM events
CROSS JOIN LATERAL (
    SELECT COALESCE(SUM(
        CASE WHEN events.title ILIKE term.pattern THEN 3 ELSE 0 END
        + CASE WHEN COALESCE(events.location, '') ILIKE term.pattern THEN 2 ELSE 0 END
        + CASE WHEN COALESCE(events.description, '') ILIKE term.pattern THEN 1 ELSE 0 END
    ), 0) AS relevance
    FROM unnest(sqlc.arg(patterns)::text[]) AS term(pattern)
) AS ranked
WHERE events.recurrence_frequency IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM unnest(sqlc.arg(patterns)::text[]) AS term(pattern)
    WHERE NOT (events.title ILIKE term.pattern OR COALESCE(events.location, '') ILIKE term.pattern OR COALESCE(events.description, '') ILIKE term.pattern)
  )
  AND (sqlc.narg(range_end)::timestamptz IS NULL
       OR CASE WHEN events.all_day THEN events.start_date::timestamp AT TIME ZONE sqlc.arg(time_zone)::text ELSE events.start_at END < sqlc.narg(range_end)::timestamptz
       OR EXISTS (
         SELECT 1 FROM event_exceptions
         WHERE event_exceptions.event_id = events.id
           AND (event_exceptions.changes ->> 'startAt' IS NOT NULL OR event_exceptions.changes ->> 'startDate' IS NOT NULL)
       ))
  AND (sqlc.arg(include_cancelled)::boolean OR events.status = 'active')
  AND (sqlc.narg(source_kind)::text IS NULL OR events.source_kind = sqlc.narg(source_kind)::text)
  AND (sqlc.narg(source_id)::uuid IS NULL OR events.source_id = sqlc.narg(source_id)::uuid)
ORDER BY events.id;

-- name: UpdateEvent :one
-- Writes the whole merged event. The expected_updated_at guard makes the
-- read-merge-write optimistic: a concurrent change makes it match nothing.
-- Source identity is never updated.
UPDATE events
SET title = sqlc.arg(title), description = sqlc.narg(description), location = sqlc.narg(location),
    all_day = sqlc.arg(all_day), start_at = sqlc.narg(start_at), end_at = sqlc.narg(end_at),
    start_date = sqlc.narg(start_date), end_date = sqlc.narg(end_date), time_zone = sqlc.narg(time_zone),
    recurrence_frequency = sqlc.narg(recurrence_frequency), recurrence_interval = sqlc.narg(recurrence_interval),
    recurrence_until = sqlc.narg(recurrence_until), recurrence_by_weekday = sqlc.narg(recurrence_by_weekday)::text[],
    recurrence_count = sqlc.narg(recurrence_count), reminders = sqlc.arg(reminders), use_default_reminders = sqlc.arg(use_default_reminders),
    priority = sqlc.narg(priority),
    status = sqlc.arg(status), cancelled_at = sqlc.narg(cancelled_at), removed_from_source_at = sqlc.narg(removed_from_source_at),
    updated_at = now()
WHERE id = sqlc.arg(id) AND updated_at = sqlc.arg(expected_updated_at)
RETURNING *;

-- name: DeleteEvent :execrows
DELETE FROM events WHERE id = $1;
