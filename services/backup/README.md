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

Each week, after the Sunday nightly run (`BACKUP_RESTORE_CHECK_DAY`), a
restore check restores every dump of the newest full run into a scratch
database (`lifeos_restore_check_<database>`, always dropped afterwards) and
compares every table's row count and the migration state with the manifest.

It has no database of its own and no gateway route. Its API (`GET
/v1/status`, `GET /v1/backups`, `POST /v1/backups`, `POST
/v1/restore-checks`) is for the Dashboard on the private network;
`tools/backup-now` runs a backup on the deployed stack, and
`tools/restore-database` restores one database from a run (see the runbook,
[`docs/operations/backups.md`](../../docs/operations/backups.md)).

See [`docs/services/backup.md`](../../docs/services/backup.md) for what is
backed up, the layout and manifest, the schedule and rotation, the API, and
the live setup.

## Layout

```text
api/openapi.yaml       # HTTP contract
cmd/server/            # process entrypoint; `/service now [database…]` and `/service restore-check` are the clients
internal/api/          # HTTP adapters
internal/backup/       # registry, dumps, run directory, rotation, schedule, restore checks, targets
internal/cli/          # the `now` and `restore-check` clients
internal/config/       # configuration
```

## Tests

```sh
GOWORK=off go test -count=1 ./...
```

The integration tests (`internal/backup/integration_test.go`) run real
backups, a restore, and restore checks against a throwaway PostgreSQL 18,
never a live one. They skip unless `BACKUP_TEST_DATABASE_URL` names a
superuser URL. Without `pg_dump` and `pg_restore` on `PATH`, the wrappers in
`internal/backup/testdata` run them (and `psql`) from the PostgreSQL image:

```sh
BACKUP_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55442/postgres \
BACKUP_TEST_PG_DUMP=$PWD/internal/backup/testdata/pg_dump \
BACKUP_TEST_PG_RESTORE=$PWD/internal/backup/testdata/pg_restore \
GOWORK=off go test -count=1 -run 'TestBackupsAreConsistentAndRestore|TestRestoreChecks' ./internal/backup/
```

`tools/test-restore-database` exercises `tools/restore-database` against
the same throwaway server
(`RESTORE_TEST_DATABASE_URL=… RESTORE_TEST_PG_BIN=$PWD/services/backup/internal/backup/testdata`).
