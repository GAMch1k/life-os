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
make infra-up     # first start: create the networks and volumes and start the stack healthy
make infra-ps     # inspect container and health state
make infra-logs   # follow PostgreSQL logs
make infra-down   # stop containers; preserve database data
make infra-reset CONFIRM=infra-reset  # stop containers and deliberately delete all data
make db-provision # reconcile configured service databases and roles
make db-provision-backup # reconcile the backup service's read-only role
make db-check-isolation # verify every service can reach only its own database
```

Running `docker compose up` directly is also supported after `.env` exists. Once Life OS is deployed, `make infra-up` and `make life-up` refuse (`CONFIRM=<target>` overrides): redeploy one service at a time with `docker compose up -d --no-build --no-deps <service>`, since a plain `up` also recreates PostgreSQL after any `.env` change.

## Naming conventions

- Compose project: `lifeos`
- Infrastructure service: `postgres`
- Private application network: `lifeos-backend`
- Persistent PostgreSQL volume: `lifeos-postgres-data`
- Environment variables: `LIFE_OS_` prefix with uppercase snake case

Services join the project-scoped `lifeos-backend` bridge and address PostgreSQL as `postgres:5432`. PostgreSQL publishes no host port; reach it inside its container, such as `docker compose exec postgres psql -U <LIFE_OS_POSTGRES_USER> -d finance_db`.

`make infra-down` is nondestructive. Use `make infra-reset` only when intentionally recreating the database from an empty volume; it refuses without `CONFIRM=infra-reset`.

## Service database provisioning

`postgres-provision` provisions the databases. Its image (`infra/postgres/Dockerfile`) holds the scripts and the registry of this directory; it waits until PostgreSQL accepts connections, logs in over the network as the admin (`LIFE_OS_POSTGRES_USER`), runs `provision-service-databases.sh`, and exits. Compose runs it after PostgreSQL is healthy and before every `<service>-migrate`, so every start reconciles the databases: a fresh volume gets them all, and an update that adds a service gets its database on its first start. No file of the repository is mounted (P20-02, [releases](../docs/operations/releases.md#self-contained-nothing-from-the-repository-at-run-time)).

`make db-provision` runs it on a running server (`docker compose run --rm --no-deps postgres-provision`, which leaves PostgreSQL as it is), with the passwords from `.env` at that moment. After a change to this directory, build its image first: `docker compose build postgres-provision`.

Service database declarations live in `infra/postgres/service-databases.conf` as:

```text
service|database|role|password_environment_variable
```

To add a generated service, add one row to that file, add its referenced password variable to `postgres-provision`'s environment in `compose.yaml`, to the local `.env`, and a safe placeholder to `.env.example`. Do not put passwords in the service registry. Provisioning creates or updates a non-privileged login, makes it owner of its database, removes grants held by previous owners, revokes public database access, and grants access only to the configured owner.

Provisioning is intentionally non-destructive: removing a registry row does not delete its database or role. Retire those resources through an explicit reviewed migration or administrative operation so data is never dropped merely because a configuration line changed.

The one login that is not a service's own is `lifeos_backup`, the [backup service](../docs/services/backup.md)'s: it reads every table (`pg_read_all_data`), connects to every database in the registry, and may create databases (for restore checks), but owns nothing. `infra/postgres/provision-backup-role.sh` provisions it with the password in `LIFE_OS_BACKUP_DB_PASSWORD`; provisioning runs it last, so a fresh volume gets it and a reapply restores its connect grants and adds new databases. `make db-provision-backup` applies it alone to a running server (`postgres-provision provision-backup-role.sh`), and `make db-check-isolation` checks that each service's login reaches its own database only (`verify-database-isolation.sh`).

The migrations are an image too: `infra/migrate/Dockerfile` builds golang-migrate (only its PostgreSQL driver) with every service's `db/migrations` at `/migrations/<service>`, the image every `<service>-migrate` runs. A new migration takes `docker compose build <service>-migrate` before it runs.
