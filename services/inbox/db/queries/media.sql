-- name: RecordMedia :one
-- Records an uploaded image. Uploading the same bytes again refreshes its
-- time, so a fresh upload is never pruned as an old orphan.
INSERT INTO inbox_media (sha256, content_type, byte_size) VALUES ($1, $2, $3)
ON CONFLICT (sha256) DO UPDATE SET created_at = now()
RETURNING *;

-- name: GetMedia :one
SELECT * FROM inbox_media WHERE sha256 = $1;

-- name: ListMedia :many
SELECT * FROM inbox_media WHERE sha256 = ANY(sqlc.arg(hashes)::text[]);

-- name: DeleteOrphanMedia :many
-- Removes media uploaded before the given time that no capture refers to,
-- and returns what it removed so the files go too.
DELETE FROM inbox_media m
WHERE m.created_at < sqlc.arg(before)::timestamptz
  AND NOT EXISTS (
    SELECT 1 FROM inbox_items i
    WHERE i.media_references @> jsonb_build_array('inbox-media:' || m.sha256)
  )
RETURNING m.sha256;
