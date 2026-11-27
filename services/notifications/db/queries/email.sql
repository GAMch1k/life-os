-- name: GetEmailStatus :one
-- No row yet: nothing was sent.
SELECT last_success_at, last_failure_at, failing_since, last_error_code
FROM email_status WHERE id = 1;

-- name: NoteEmailSent :exec
-- An email went out: sends work again.
INSERT INTO email_status (id, last_success_at, updated_at)
VALUES (1, sqlc.arg(at)::timestamptz, now())
ON CONFLICT (id) DO UPDATE SET last_success_at = EXCLUDED.last_success_at,
  failing_since = NULL, updated_at = now();

-- name: NoteEmailFailed :exec
-- A send failed: failing since the first failure after the latest success.
INSERT INTO email_status (id, last_failure_at, failing_since, last_error_code, updated_at)
VALUES (1, sqlc.arg(at)::timestamptz, sqlc.arg(at)::timestamptz, sqlc.arg(code)::text, now())
ON CONFLICT (id) DO UPDATE SET last_failure_at = EXCLUDED.last_failure_at,
  failing_since = coalesce(email_status.failing_since, EXCLUDED.failing_since),
  last_error_code = EXCLUDED.last_error_code, updated_at = now();
