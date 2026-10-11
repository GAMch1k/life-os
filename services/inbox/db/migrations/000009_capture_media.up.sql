-- Photos attached to captures (P16-02). Inbox owns capture media: each image
-- is stored once on the inbox-media volume, in a file named by its SHA-256,
-- and this table records what it is. A capture names its images in
-- inbox_items.media_references as "inbox-media:<sha256>". Media stays while a
-- capture refers to it (captures are never deleted); an upload no capture
-- refers to is removed a day later.
CREATE TABLE inbox_media (
    sha256 TEXT PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    content_type TEXT NOT NULL CHECK (content_type IN ('image/jpeg', 'image/png', 'image/webp')),
    byte_size BIGINT NOT NULL CHECK (byte_size > 0),
    -- The latest upload of these bytes: an orphan is judged from it.
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX inbox_media_created_idx ON inbox_media (created_at);
