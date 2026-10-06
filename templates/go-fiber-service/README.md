# Canonical Go + Fiber service template

This is the neutral starting point for Life OS backend services. It provides
the standard process, HTTP, persistence, migration, and OpenAPI layout without
introducing a Finance, Tasks, Inbox, Calendar, or other business domain.

`tools/create-service` will turn this template into a named service in a later
foundation ticket. Do not add business entities, routes, tables, or queries to
the template itself.

## Layout

```text
api/openapi.yaml                 # transport-contract placeholder
cmd/server/                      # process entrypoint
db/migrations/                   # reversible, service-owned migrations
db/queries/                      # service-owned sqlc input
internal/api/                    # HTTP adapters
internal/config/                 # service configuration
internal/domain/                 # service domain types
internal/repository/             # persistence adapters and sqlc output
internal/service/                # service use cases
```

## Conventions

- Every generated service is an independent Go module.
- Shared infrastructure comes only from `github.com/GAMch1k/life-os/pkg`.
- The service owns all contents of `db/`; it never reads another service's
  database.
- Migrations are six-digit, ordered, and reversible.
- sqlc input lives in `db/queries`; generated pgx/v5 code is committed under
  `internal/repository/generated`.
- The process exposes the shared `/health` and `/ready` endpoints and performs
  graceful shutdown through `pkg/httpserver`.

Runnable local and Docker commands are added alongside the process entrypoint.

