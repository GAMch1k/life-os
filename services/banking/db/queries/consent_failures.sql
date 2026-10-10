-- name: CreateConsentFailure :one
INSERT INTO bank_consent_failures (occurred_at, error, description, institution, country, connection_id, state_match)
VALUES (sqlc.arg(occurred_at), sqlc.arg(error), sqlc.narg(description), sqlc.narg(institution), sqlc.narg(country),
        sqlc.narg(connection_id), sqlc.arg(state_match))
RETURNING *;

-- name: PruneConsentFailures :exec
-- Keeps the newest keep failures.
DELETE FROM bank_consent_failures
WHERE id NOT IN (SELECT id FROM bank_consent_failures ORDER BY occurred_at DESC, id DESC LIMIT sqlc.arg(keep)::integer);

-- name: LatestConsentFailure :one
-- The newest failure no connection has succeeded after.
SELECT * FROM bank_consent_failures
WHERE cleared_at IS NULL
ORDER BY occurred_at DESC, id DESC
LIMIT 1;

-- name: ClearConsentFailures :exec
UPDATE bank_consent_failures SET cleared_at = sqlc.arg(now) WHERE cleared_at IS NULL;
