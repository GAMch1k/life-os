# Activity service

The Activity service stores durable, user-facing activity events: what happened
in Life OS, who or what did it, and how it relates to the rest of a workflow.
Owning services (Voice, Inbox, MCP, Tasks) record events at their operation
boundaries; the Dashboard reads them through the gateway.

It is product/audit data, not a log sink. It never reads another service's
database, and services never read `activity_db`.

See [`docs/services/activity.md`](../../docs/services/activity.md) for the event
model, correlation rules, and API.

## Layout

```text
api/openapi.yaml       # Activity API contract (source of truth)
cmd/server/            # process entrypoint
db/migrations/         # activity_events table (append-only)
db/queries/            # sqlc input
internal/api/          # HTTP adapter, validation, keyset cursors
```
