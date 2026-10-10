# Canonical Go + Fiber service template

This is the neutral starting point for Life OS backend services. It provides
the standard process, HTTP, persistence, migration, and OpenAPI layout without
introducing a Finance, Tasks, Inbox, Calendar, or other business domain.

Run `make create-service SERVICE=<name>` from the repository root to turn this
template into a named service. Do not add business entities, routes, tables,
or queries to the template itself.

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

## Commands

From this directory, with `DATABASE_URL` set to this service's database:

```sh
make generate
make test
make migrate-up DATABASE_URL='postgres://...'
make run DATABASE_URL='postgres://...'
```

Build the image from the repository root context and run it on the local Life
OS Docker network:

```sh
make docker-build
make docker-run DATABASE_URL='postgres://banking_user:password@postgres:5432/banking_db?sslmode=disable'
```

The template's baseline migration is intentionally a no-op. It proves the
migration workflow without creating a sample business table; generated services
replace it with their first service-owned migration.
