-- name: CreateInboxItem :one
INSERT INTO inbox_items (content, source, source_metadata, media_references) VALUES ($1, $2, $3, $4) RETURNING *;
-- name: GetInboxItem :one
SELECT * FROM inbox_items WHERE id = $1;
-- name: ListInboxItems :many
SELECT * FROM inbox_items
WHERE (sqlc.narg(unresolved_filter)::boolean IS NULL OR (state IN ('raw', 'processing')) = sqlc.narg(unresolved_filter)::boolean)
  AND (sqlc.narg(state_filter)::text IS NULL OR state = sqlc.narg(state_filter)::text)
ORDER BY created_at DESC LIMIT sqlc.arg(page_limit);
-- name: SetInboxItemState :one
UPDATE inbox_items SET state = $1, resolved_at = CASE WHEN $1 IN ('resolved', 'discarded') THEN now() ELSE resolved_at END, updated_at = now() WHERE id = $2 RETURNING *;
-- name: ListProcessingAttempts :many
SELECT * FROM inbox_processing_attempts WHERE inbox_item_id = $1 ORDER BY created_at DESC;
