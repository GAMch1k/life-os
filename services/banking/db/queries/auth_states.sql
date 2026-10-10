-- name: DeleteExpiredAuthStates :exec
DELETE FROM bank_auth_states WHERE expires_at <= sqlc.arg(now);

-- name: CreateAuthState :exec
INSERT INTO bank_auth_states (state_hash, institution, country, connection_id, expires_at)
VALUES (sqlc.arg(state_hash), sqlc.arg(institution), sqlc.arg(country), sqlc.narg(connection_id), sqlc.arg(expires_at));

-- name: ConsumeAuthState :one
-- Single use: the state is deleted as it is read.
DELETE FROM bank_auth_states WHERE state_hash = sqlc.arg(state_hash)
RETURNING *;
