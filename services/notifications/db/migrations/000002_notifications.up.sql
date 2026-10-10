-- Notifications are the one path by which Life OS tells its owner about
-- something. Producers post them over HTTP; this service never reads another
-- service's data.
CREATE TABLE notifications (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title TEXT NOT NULL CHECK (btrim(title) <> '' AND char_length(title) <= 200),
    body TEXT NOT NULL DEFAULT '' CHECK (char_length(body) <= 2000),
    importance TEXT NOT NULL CHECK (importance IN ('low', 'medium', 'high')),
    category TEXT NOT NULL CHECK (category ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$' AND char_length(category) <= 100),
    source TEXT NOT NULL CHECK (source ~ '^[a-z0-9][a-z0-9._-]*$' AND char_length(source) <= 64),
    link TEXT CHECK (link IS NULL OR (link ~ '^/([^/\\]|$)' AND char_length(link) <= 500)),
    dedupe_key TEXT NOT NULL CHECK (btrim(dedupe_key) <> '' AND char_length(dedupe_key) <= 200),
    -- The producer's workflow, so delivery activity joins the same trace.
    correlation_id TEXT,
    occurred_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at TIMESTAMPTZ,
    CONSTRAINT notifications_dedupe_key_key UNIQUE (dedupe_key)
);

-- Newest-first listing and keyset paging, with and without filters.
CREATE INDEX notifications_created_idx ON notifications (created_at DESC, id DESC);
CREATE INDEX notifications_unread_created_idx ON notifications (created_at DESC, id DESC) WHERE read_at IS NULL;
CREATE INDEX notifications_category_idx ON notifications (category text_pattern_ops, created_at DESC);
CREATE INDEX notifications_source_idx ON notifications (source, created_at DESC);
CREATE INDEX notifications_importance_idx ON notifications (importance, created_at DESC);
-- The unread badge counts only unread rows.
CREATE INDEX notifications_unread_importance_idx ON notifications (importance) WHERE read_at IS NULL;

-- One delivery per notification and channel enabled when it was created. A
-- worker claims a due delivery with a lease (lease_owner, lease_expires_at);
-- an expired lease makes it claimable again, so a crash never loses one.
CREATE TABLE deliveries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    notification_id UUID NOT NULL REFERENCES notifications (id) ON DELETE CASCADE,
    channel TEXT NOT NULL CHECK (btrim(channel) <> '' AND char_length(channel) <= 64),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'retrying', 'sent', 'failed', 'skipped')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ,
    lease_owner TEXT,
    lease_expires_at TIMESTAMPTZ,
    last_error TEXT,
    sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT deliveries_notification_channel_key UNIQUE (notification_id, channel),
    CONSTRAINT deliveries_due_check CHECK (status NOT IN ('pending', 'retrying') OR next_attempt_at IS NOT NULL)
);

CREATE INDEX deliveries_due_idx ON deliveries (next_attempt_at) WHERE status IN ('pending', 'retrying');
