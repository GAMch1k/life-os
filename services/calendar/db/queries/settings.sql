-- name: GetSettings :one
SELECT * FROM calendar_settings WHERE id;

-- name: UpdateSettings :one
-- Replaces the lists given (a NULL list stays), creating the row on the
-- first change.
INSERT INTO calendar_settings (id, timed_default_reminders, all_day_default_reminders, updated_at)
VALUES (true, COALESCE(sqlc.narg(timed_default_reminders)::jsonb, '[]'::jsonb), COALESCE(sqlc.narg(all_day_default_reminders)::jsonb, '[]'::jsonb), now())
ON CONFLICT (id) DO UPDATE
SET timed_default_reminders = COALESCE(sqlc.narg(timed_default_reminders)::jsonb, calendar_settings.timed_default_reminders),
    all_day_default_reminders = COALESCE(sqlc.narg(all_day_default_reminders)::jsonb, calendar_settings.all_day_default_reminders),
    updated_at = now()
RETURNING *;
