-- The integration API (P11-06): integration services register their
-- sources by (integration, external_id) and sync their events through
-- Calendar's public API (internal/api/integration*.go).

-- name: CreateIntegrationSource :one
-- Registers a source an integration syncs; nothing (no row) when it is
-- already registered: the caller then locks and updates it.
INSERT INTO calendar_sources (
    kind, name, enabled, default_reminders, default_priority,
    integration, external_id, provider, read_only, private_location, integration_config
)
VALUES (
    'external', sqlc.arg(name), true, sqlc.narg(default_reminders), sqlc.arg(default_priority),
    sqlc.arg(integration)::text, sqlc.arg(external_id)::text, sqlc.arg(provider)::text, sqlc.arg(read_only),
    sqlc.arg(private_location), sqlc.arg(integration_config)
)
ON CONFLICT (integration, external_id) DO NOTHING
RETURNING *;

-- name: GetIntegrationSource :one
SELECT * FROM calendar_sources
WHERE integration = sqlc.arg(integration)::text AND external_id = sqlc.arg(external_id)::text;

-- name: LockIntegrationSource :one
SELECT * FROM calendar_sources
WHERE integration = sqlc.arg(integration)::text AND external_id = sqlc.arg(external_id)::text
FOR UPDATE;

-- name: ListIntegrationSources :many
SELECT * FROM calendar_sources WHERE integration = sqlc.arg(integration)::text ORDER BY created_at, id;

-- name: UpdateIntegrationSource :one
-- Writes a registration again (the whole merged state, computed by the
-- caller under LockIntegrationSource).
UPDATE calendar_sources
SET name = sqlc.arg(name), provider = sqlc.arg(provider)::text, read_only = sqlc.arg(read_only),
    private_location = sqlc.arg(private_location),
    default_reminders = sqlc.narg(default_reminders), default_priority = sqlc.arg(default_priority),
    integration_config = sqlc.arg(integration_config), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteIntegrationSource :execrows
-- Also deletes the source's events (ON DELETE CASCADE).
DELETE FROM calendar_sources WHERE id = $1;

-- name: ListSourceIdentities :many
-- The integration identity of the given sources, for the change feed.
SELECT id, integration, external_id, read_only FROM calendar_sources WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: RekeyImportedEvent :execrows
-- Gives a source's event the external uid the source now knows it by (an
-- event created in Life OS for the source, such as a split's new series,
-- once the integration has created it there), unless that uid is taken.
UPDATE events
SET external_uid = sqlc.arg(external_uid)::text, updated_at = now()
WHERE events.source_id = sqlc.arg(source_id)::uuid AND events.external_uid = sqlc.arg(previous_uid)::text
  AND NOT EXISTS (
    SELECT 1 FROM events AS taken WHERE taken.source_id = sqlc.arg(source_id)::uuid AND taken.external_uid = sqlc.arg(external_uid)::text
  );
