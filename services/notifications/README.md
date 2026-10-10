# Notifications service

The one path by which Life OS lets its owner know about something. Any service
that needs to tell the owner something (a service went down, Inbox needs Human
Review, a Calendar reminder) posts a notification here, usually through
`pkg/notify`. Notifications stores it so the Dashboard can list it, keeps its
read state, and delivers it through the channels configured on this server.
Producers never talk to a channel themselves.

It never reads another service's database, and services never read
`notifications_db`.

See [`docs/services/notifications.md`](../../docs/services/notifications.md)
for importance, dedupe, delivery states, channels, and the API.

## Layout

```text
api/openapi.yaml       # Notifications API contract (source of truth)
cmd/server/            # process entrypoint: HTTP API and the delivery worker
db/migrations/         # notifications and deliveries tables
db/queries/            # sqlc input
internal/api/          # HTTP adapter, validation, keyset cursors
internal/delivery/     # Channel interface, retry policy, lease-based worker
internal/testdb/       # private migrated schemas for integration tests
```

## Tests

Integration tests run against the throwaway Postgres named by
`NOTIFICATIONS_TEST_DATABASE_URL` (each test creates and drops its own schema)
and skip when it is unset:

```sh
NOTIFICATIONS_TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:55441/postgres?sslmode=disable' \
  GOWORK=off go test -count=1 ./...
```
