-- name: ClaimIdempotencyKey :one
-- Claims key for this transaction. While another transaction holds the same
-- key this waits for it; once that one has committed, nothing is inserted and
-- no row is returned, and the caller reads the stored response instead.
INSERT INTO idempotency_keys (key, request_hash)
VALUES (@key, @request_hash)
ON CONFLICT (key) DO NOTHING
RETURNING key;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE key = @key;

-- name: CompleteIdempotencyKey :exec
UPDATE idempotency_keys SET status = @status, response = @response WHERE key = @key;

-- name: PruneIdempotencyKeys :exec
DELETE FROM idempotency_keys WHERE created_at < @before;
