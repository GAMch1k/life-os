# Life OS

Life OS is a local-first personal operating system. Independent domain services sit behind one dashboard and, later, one AI/MCP interaction layer.

This repository is the single monorepo for the project. The foundation phase establishes shared development rails before domain behavior is introduced.

## Repository layout

| Path | Purpose |
| --- | --- |
| `apps/dashboard/` | Next.js and TypeScript dashboard |
| `services/` | Independently deployable Go domain services |
| `pkg/` | Shared Go infrastructure packages, never shared domain models |
| `contracts/` | OpenAPI contracts and client-generation configuration |
| `templates/` | Canonical service templates used by repository tooling |
| `infra/` | Local infrastructure, containers, and provisioning |
| `tools/` | Development scripts and generators |

Each service owns its API implementation, domain model, persistence code, migrations, and database. Services and consumers communicate through APIs and never access another service's database directly.

## Prerequisites

- Git
- Go 1.27.1 or newer in the 1.27 release line
- Docker Engine with Docker Compose v2
- Node.js 22 or newer once dashboard development begins

Docker and Node.js are not required to inspect this initial repository skeleton. Their runnable workflows arrive in later foundation tickets.

## Local infrastructure

Create a local environment file and start PostgreSQL:

```sh
make infra-up
make infra-ps
```

Stop the stack without deleting its persistent data with `make infra-down`. Use `make infra-reset` only when you intentionally want to delete and recreate the local PostgreSQL volume. See [`infra/README.md`](infra/README.md) for direct Compose usage, environment variables, and naming conventions.

Each domain service uses an isolated database and login inside the shared PostgreSQL container. `make db-provision` reconciles the declarative registry in `infra/postgres/service-databases.conf`; credentials remain in the ignored local `.env` file.

## Go workspace strategy

Backend code uses a multi-module Go workspace. Each service is an independent module so ownership and dependencies stay explicit. Shared infrastructure is a separate module under `pkg/`. Every module is registered in the root `go.work` file when it is created.

Create a service from the canonical template with a lowercase kebab-case name:

```sh
make create-service SERVICE=habits
```

The command creates `services/habits`, registers its module in `go.work`, and
derives `habits_db`, `habits_user`, and
`LIFE_OS_HABITS_DB_PASSWORD`. Use `tools/create-service --help` to override
the database, role, or password-variable names. Run the repeatable local/CI
verification after starting the local infrastructure:

```sh
make infra-up
make create-service-smoke
```

Validate the workspace declaration with:

```sh
go work edit -json
```

## Shared configuration

`pkg/config` loads typed service configuration from environment variables. Struct fields opt in with `env` tags and may declare `required:"true"` or a `default` value. Configuration structs can implement `Validate() error` for cross-field rules. Loading reports readable field errors and updates the target only after the complete configuration is valid.

Run the full baseline verification path from the repository root:

```sh
make check
```

It runs `sqlc generate` and fails if committed generated code changes, then
checks Go formatting, vet, tests, and builds for each repository module before
running the dashboard's format, lint, test, type, and production-build checks.
Install `go`, `sqlc`, and Node/npm on `PATH` first; the exact database-tool
versions are documented in [`pkg/database/README.md`](pkg/database/README.md).

## Shared HTTP runtime

`pkg/httpserver` provides the thin Fiber runtime used by Go services. `httpserver.New` returns an app that embeds the native Fiber app while installing structured `slog` request logging, request/correlation IDs, a safe error envelope, panic recovery, and `/health` and `/ready` endpoints. `Run` handles SIGINT/SIGTERM, drains HTTP traffic, and invokes registered shutdown hooks in reverse order.

## Development commands

Repository-wide commands are introduced alongside the capabilities they operate.
`make create-service` creates a canonical backend service, `make check` is the
non-mutating verification path, and `make dashboard-check` runs only the
dashboard's formatting, lint, type, test, and production-build checks.

## Architectural boundaries

- Go and Fiber are the default backend stack.
- PostgreSQL is shared as a server only; each service has its own database, credentials, and migrations.
- OpenAPI is the source of truth for HTTP contracts and generated Go and TypeScript clients.
- Shared packages contain infrastructure concerns only, never Finance, Tasks, Inbox, or Calendar business types.
- Public authentication, queues, Kubernetes, global tags/areas, and direct cross-service database access are intentionally deferred.
