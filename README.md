# Life OS

Life OS is a local-first personal operating system. Independent domain services sit behind one dashboard, a constrained AI/MCP interaction layer, and an asynchronous local voice-transcription path.

This repository is the single monorepo for the project. It contains the implemented Finance, Tasks, Inbox, Calendar, Voice, MCP, dashboard, and private Whisper worker components through Phase 6, Food (the shopping list and pantry, Phase 15), integration services such as Banking, which syncs the owner's bank accounts into Finance through a licensed PSD2 provider (read-only), calendar-feeds (ICS feeds into Calendar), recipe-import (recipes from links and captions into Food), and google-calendar (the owner's Google calendars, synced both ways with Calendar), a backup service that dumps every database each night (see [`docs/services/backup.md`](docs/services/backup.md)), and host-health, which reports the server's memory, load, disks, and containers to the Status page (see [`docs/services/host-health.md`](docs/services/host-health.md)).

## Quick start

Run Life OS on your own Linux server from the published Docker images, with one setup script and nothing to build:

```sh
mkdir ~/life-os && cd ~/life-os
version=v0.1.0-preview.1
for file in compose.yaml .env.example setup.sh SHA256SUMS; do
  curl -fsSLO "https://github.com/GAMch1k/life-os/releases/download/$version/$file"
done
sha256sum --ignore-missing -c SHA256SUMS
chmod +x setup.sh && ./setup.sh && ./setup.sh up
```

**This is an early preview.** Life OS has no login of its own yet, so it listens only on `127.0.0.1`, and you reach it through an SSH tunnel, a VPN, or Cloudflare Access. The [install guide](docs/install/README.md) covers the requirements, safe access, AI processing, the integrations, the phone apps, backups, updates, and troubleshooting.

## Technical documentation

The human-readable technical guide is maintained as ordinary repository Markdown
under [`docs/`](docs/README.md). It is designed for GitHub browsing and Obsidian;
there is no GitHub Pages or Wiki dependency.

To run your own Life OS from the published images, see the
[install guide](docs/install/README.md). To build and run it from this
repository instead, see [`SETUP.md`](SETUP.md), the self-hosting guide.

## Repository layout

| Path | Purpose |
| --- | --- |
| `apps/dashboard/` | Next.js and TypeScript dashboard |
| `services/` | Independently deployable Go domain and integration services |
| `pkg/` | Shared Go infrastructure packages, never shared domain models |
| `contracts/` | OpenAPI contracts and client-generation configuration |
| `templates/` | Canonical service templates used by repository tooling |
| `infra/` | Local infrastructure, containers, and provisioning |
| `tools/` | Development scripts and generators |

Each service owns its API implementation, domain model, persistence code, migrations, and database. Services and consumers communicate through APIs and never access another service's database directly.

## Prerequisites

- Git
- Go 1.27.3 or newer in the 1.27 release line
- Docker Engine with Docker Compose v2
- Node.js 22 or newer once dashboard development begins

Docker and Node.js are required to run the complete local stack; Go is required for service development and verification.

## Local infrastructure

Create a local environment file and start PostgreSQL:

```sh
make infra-up
make infra-ps
```

Stop the stack without deleting its persistent data with `make infra-down`. Use `make infra-reset` only when you intentionally want to delete and recreate persistent Compose volumes; it refuses without `CONFIRM=infra-reset`. `make infra-up` and `make life-up` are for a first start: once Life OS is deployed they refuse (`CONFIRM=<target>` overrides), and services are redeployed one at a time with `docker compose up -d --no-build --no-deps <service>`. See [`docs/development/getting-started.md`](docs/development/getting-started.md) and [`docs/operations/deployment.md`](docs/operations/deployment.md) for Compose usage, configuration, and operations.

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

It validates OpenAPI documents, regenerates Go and TypeScript contracts and
fails on generated-code drift, then runs `sqlc generate` and performs the Go
and dashboard checks. Install `go`, `sqlc`, `oapi-codegen`, and Node/npm on
`PATH` first; the exact database-tool versions are documented in
[`pkg/database/README.md`](pkg/database/README.md).

## Shared HTTP runtime

`pkg/httpserver` provides the thin Fiber runtime used by Go services. `httpserver.New` returns an app that embeds the native Fiber app while installing structured `slog` request logging, request/correlation IDs, a safe error envelope, panic recovery, and `/health` and `/ready` endpoints. `Run` handles SIGINT/SIGTERM, drains HTTP traffic, and invokes registered shutdown hooks in reverse order.

## HTTP contracts

Life OS API conventions and reusable transport schemas are documented in
[`contracts/openapi`](contracts/openapi/README.md). Service OpenAPI documents
are public transport contracts only; they do not expose or share internal Go
domain structs.

Finance CSV imports are documented in
[`docs/finance-csv-import.md`](docs/finance-csv-import.md).

## Development commands

Repository-wide commands are introduced alongside the capabilities they operate.
`make create-service` creates a canonical backend service, `make check` is the
non-mutating verification path, and `make dashboard-check` runs only the
dashboard's formatting, lint, type, test, and production-build checks.

## Complete local Life OS

Start the backend services and dashboard together with:

```sh
make life-up
```

Open <http://localhost:3000>. The Docker Nginx gateway is the sole published
application port; it routes the dashboard and all `/api/*` requests to private
containers. Run the browser-facing smoke check, on the disposable verification
stack (never on the running one), with:

```sh
make life-smoke
```

If port 3000 is already in use, set `LIFE_OS_GATEWAY_PORT` in `.env` (for
example, `LIFE_OS_GATEWAY_PORT=3001`).

To expose the single gateway through WireGuard, bind it to the server's
WireGuard address: `LIFE_OS_GATEWAY_BIND_ADDRESS=10.8.0.1` in `.env`, with
your own address. The gateway then also answers that address as a Host.
This exposes Dashboard, write APIs, and MCP to every allowed WireGuard peer;
never bind it to a public/LAN address without an authenticated edge. See
[SETUP.md](SETUP.md) for every setting.

## Architectural boundaries

- Go and Fiber are the default backend stack.
- PostgreSQL is shared as a server only; each service has its own database, credentials, and migrations.
- OpenAPI is the source of truth for HTTP contracts and generated Go and TypeScript clients.
- Shared packages contain infrastructure concerns only, never Finance, Tasks, Inbox, or Calendar business types.
- Public authentication, queues, Kubernetes, global tags/areas, and direct cross-service database access are intentionally deferred.

## Official sources and name

The official Life OS is published only at [github.com/GAMch1k](https://github.com/GAMch1k) (the source code and its releases), [ghcr.io/gamch1k](https://github.com/GAMch1k?tab=packages) (the Docker images), and lifeos.gamch1k.com. Forks are welcome under the AGPL, but please don't present a fork as the official Life OS: give it a name of its own, and say what it is based on.

## License

Copyright (C) 2026 GAMch1k and the Life OS contributors.

Life OS is free software: you can redistribute it and/or modify it under the terms of the GNU Affero General Public License, version 3 only (SPDX: `AGPL-3.0-only`), as published by the Free Software Foundation. It is distributed in the hope that it will be useful, but WITHOUT ANY WARRANTY; see [`LICENSE`](LICENSE) for the full text. If you run a modified version for other people over a network, the license requires you to offer them its source code.

Contributions are accepted under the [Contributor License Agreement](CLA.md): you keep your copyright, your contribution stays available under the AGPL, and the maintainer may also offer it under other licenses. See [CONTRIBUTING.md](CONTRIBUTING.md).
