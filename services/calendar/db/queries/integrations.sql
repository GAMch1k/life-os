-- The integration API (P11-06): integration services register their
-- sources by (integration, external_id) and sync their events through
-- Calendar's public API (internal/api/integration*.go).

-- name: CreateIntegrationSource :one
-- Registers a source an integration syncs; nothing (no row) when it is
-- already registered: the caller then locks and updates it.
INSERT INTO calendar_sources (
    kind, name, url, enabled, default_reminders, default_priority,
    integration, external_id, provider, read_only, in_process, integration_config
)
VALUES (
    sqlc.arg(kind), sqlc.arg(name), sqlc.arg(url), true, sqlc.narg(default_reminders), sqlc.arg(default_priority),
    sqlc.arg(integration), sqlc.arg(external_id), sqlc.arg(provider), sqlc.arg(read_only), false, sqlc.arg(integration_config)
)
ON CONFLICT (integration, external_id) DO NOTHING
RETURNING *;

-- name: GetIntegrationSource :one
SELECT * FROM calendar_sources
WHERE integration = sqlc.arg(integration) AND external_id = sqlc.arg(external_id);

-- name: LockIntegrationSource :one
SELECT * FROM calendar_sources
WHERE integration = sqlc.arg(integration) AND external_id = sqlc.arg(external_id)
FOR UPDATE;

-- name: ListIntegrationSources :many
SELECT * FROM calendar_sources WHERE integration = sqlc.arg(integration) ORDER BY created_at, id;

-- name: UpdateIntegrationSource :one
-- Writes a registration again (the whole merged state, computed by the
-- caller under LockIntegrationSource).
UPDATE calendar_sources
SET name = sqlc.arg(name), url = sqlc.arg(url), provider = sqlc.arg(provider), read_only = sqlc.arg(read_only),
    default_reminders = sqlc.narg(default_reminders), default_priority = sqlc.arg(default_priority),
    integration_config = sqlc.arg(integration_config), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteIntegrationSource :execrows
-- Also deletes the source's events (ON DELETE CASCADE).
DELETE FROM calendar_sources WHERE id = $1;
