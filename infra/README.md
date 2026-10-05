# Local infrastructure

The root `compose.yaml` defines the local infrastructure shared by Life OS services. Application services are added only by later tickets.

## Environment

Docker Compose automatically reads the ignored root `.env` file. Create it from the committed example before invoking Compose directly:

```sh
cp .env.example .env
```

`make infra-up` performs that copy when `.env` is absent. Values in `.env.example` are development examples, not deployed secrets. Put actual local credentials only in `.env`.

## Commands

```sh
make infra-up     # create the network/volume and start healthy infrastructure
make infra-ps     # inspect container and health state
make infra-logs   # follow PostgreSQL logs
make infra-down   # stop containers; preserve database data
make infra-reset  # stop containers and deliberately delete local database data
make db-provision # reconcile configured service databases and roles
make db-check-isolation # verify every service can reach only its own database
```

Running `docker compose up` directly is also supported after `.env` exists.

## Naming conventions

- Compose project: `lifeos`
- Infrastructure service: `postgres`
- Private application network: `lifeos-backend`
- Persistent PostgreSQL volume: `lifeos-postgres-data`
- Environment variables: `LIFE_OS_` prefix with uppercase snake case

Services join the project-scoped `lifeos-backend` bridge and address PostgreSQL as `postgres:5432`. The database is published only on host loopback for local tools, so it is not exposed on external host interfaces.

`make infra-down` is nondestructive. Use `make infra-reset` only when intentionally recreating the database from an empty volume.

## Service database provisioning

The PostgreSQL container runs `infra/postgres/provision-service-databases.sh` when a clean volume is initialized. The same idempotent provisioning can be reapplied to a running container with `make db-provision`.

Service database declarations live in `infra/postgres/service-databases.conf` as:

```text
service|database|role|password_environment_variable
```

To add a generated service, add one row to that file and add its referenced password variable to the local `.env` (and a safe placeholder to `.env.example`). Do not put passwords in the service registry. Provisioning creates or updates a non-privileged login, makes it owner of its database, removes grants held by previous owners, revokes public database access, and grants access only to the configured owner.

Provisioning is intentionally non-destructive: removing a registry row does not delete its database or role. Retire those resources through an explicit reviewed migration or administrative operation so data is never dropped merely because a configuration line changed.
