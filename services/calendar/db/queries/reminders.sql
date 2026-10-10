-- name: ListReminderEvents :many
-- Active non-recurring events that start in [range_start, range_end) for
-- reminder delivery: timed ones by their start instant, all-day ones by
-- their first date between first_date and last_date (the owner's days
-- around the range; the caller narrows them to the instants). Recurring
-- events come from ListRecurringEvents. Each arm uses its kind's start
-- index.
SELECT * FROM events
WHERE recurrence_frequency IS NULL AND status = 'active' AND NOT all_day
  AND start_at >= sqlc.arg(range_start)::timestamptz AND start_at < sqlc.arg(range_end)::timestamptz
UNION ALL
SELECT * FROM events
WHERE recurrence_frequency IS NULL AND status = 'active' AND all_day
  AND start_date >= sqlc.arg(first_date)::date AND start_date <= sqlc.arg(last_date)::date;

-- name: ListPresetSourceIDs :many
-- The sources Calendar configures itself (ZAW).
SELECT id FROM calendar_sources WHERE preset IS NOT NULL;

-- name: ClaimReminderRun :one
-- Takes the dispatcher's lease until lease_until, creating the state row on
-- the first run. Returns no row while another process holds an unexpired
-- lease.
INSERT INTO calendar_reminder_dispatch (id, lease_owner, lease_until, last_attempt_at, updated_at)
VALUES (true, sqlc.arg(owner)::text, sqlc.arg(lease_until)::timestamptz, sqlc.arg(now)::timestamptz, now())
ON CONFLICT (id) DO UPDATE
SET lease_owner = EXCLUDED.lease_owner, lease_until = EXCLUDED.lease_until,
    last_attempt_at = EXCLUDED.last_attempt_at, updated_at = now()
WHERE calendar_reminder_dispatch.lease_until IS NULL
   OR calendar_reminder_dispatch.lease_until <= sqlc.arg(now)::timestamptz
   OR calendar_reminder_dispatch.lease_owner = sqlc.arg(owner)::text
RETURNING *;

-- name: FinishReminderRun :execrows
-- Moves the cursor and releases the lease, if owner still holds it. A run
-- with an error keeps its message; one without records its success.
UPDATE calendar_reminder_dispatch
SET last_run_at = sqlc.arg(last_run_at)::timestamptz,
    lease_owner = NULL, lease_until = NULL,
    last_success_at = CASE WHEN sqlc.narg(error)::text IS NULL THEN sqlc.arg(now)::timestamptz ELSE last_success_at END,
    last_error = COALESCE(sqlc.narg(error)::text, last_error),
    last_error_at = CASE WHEN sqlc.narg(error)::text IS NULL THEN last_error_at ELSE sqlc.arg(now)::timestamptz END,
    last_sent_at = COALESCE(sqlc.narg(sent_at)::timestamptz, last_sent_at),
    updated_at = now()
WHERE id AND lease_owner = sqlc.arg(owner)::text;

-- name: GetReminderDispatch :one
SELECT * FROM calendar_reminder_dispatch WHERE id;

-- name: ListHandledReminders :many
-- Which of the given dedupe keys were already handled.
SELECT dedupe_key FROM calendar_reminder_log WHERE dedupe_key = ANY(sqlc.arg(keys)::text[]);

-- name: RecordReminder :execrows
-- Records one reminder's outcome; 0 rows when it was already recorded.
INSERT INTO calendar_reminder_log (dedupe_key, due_at, outcome, recorded_at)
VALUES (sqlc.arg(dedupe_key), sqlc.arg(due_at), sqlc.arg(outcome), sqlc.arg(recorded_at))
ON CONFLICT (dedupe_key) DO NOTHING;

-- name: PruneReminderLog :execrows
DELETE FROM calendar_reminder_log WHERE due_at < sqlc.arg(before)::timestamptz;
