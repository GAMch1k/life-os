# Backup service

Backs up every service database each night. At `BACKUP_TIME` (03:15) in
`LIFE_OS_TIMEZONE` it reads the database registry
(`infra/postgres/service-databases.conf`) and dumps each database with
`pg_dump` from one consistent snapshot, records the row count of every table
and the migration version in that snapshot, checks the archive with
`pg_restore --list`, and writes the run to `BACKUP_DIR/auto/<UTC start
time>/` with a `manifest.json`. Old runs are rotated (7 daily, 4 weekly, 12
monthly by default). It logs in as `lifeos_backup`, a read-only role
(`infra/postgres/provision-backup-role.sh`).

It has no database of its own and no gateway route. Its API (`GET
/v1/status`, `GET /v1/backups`, `POST /v1/backups`) is for the Dashboard on
the private network; `tools/backup-now` runs a backup on the deployed stack.

See [`docs/services/backup.md`](../../docs/services/backup.md) for what is
backed up, the layout and manifest, the schedule and rotation, the API, and
the live setup.

## Layout

```text
api/openapi.yaml       # HTTP contract
cmd/server/            # process entrypoint; `/service now [database…]` is the client
internal/api/          # HTTP adapters
internal/backup/       # registry, dumps, run directory, rotation, schedule
internal/cli/          # the `now` client
internal/config/       # configuration
```

## Tests

```sh
GOWORK=off go test -count=1 ./...
```

The integration test (`internal/backup/integration_test.go`) runs real
backups and a restore against a throwaway PostgreSQL 18, never a live one. It
skips unless `BACKUP_TEST_DATABASE_URL` names a superuser URL. Without
`pg_dump` and `pg_restore` on `PATH`, the wrappers in
`internal/backup/testdata` run them from the PostgreSQL image:

```sh
BACKUP_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55442/postgres \
BACKUP_TEST_PG_DUMP=$PWD/internal/backup/testdata/pg_dump \
BACKUP_TEST_PG_RESTORE=$PWD/internal/backup/testdata/pg_restore \
GOWORK=off go test -count=1 -run TestBackupsAreConsistentAndRestore ./internal/backup/
```
