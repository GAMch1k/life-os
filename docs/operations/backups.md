# Backups runbook

How Life OS's backups work in practice: what they hold, where they are, how to tell they are good, and how to restore one database or all of them. The [backup service](../services/backup.md) page explains the service itself (consistency, manifest, API, configuration).

## What is backed up

Every database in the registry, `infra/postgres/service-databases.conf`: each night at 03:15 (`LIFE_OS_BACKUP_TIME`, in `LIFE_OS_TIMEZONE`) the backup service dumps each one with `pg_dump` from one consistent snapshot, with its schemas, data, owners, privileges, and migration state. A service added to the registry is included from the first night after its database is provisioned.

Inbox's capture images (the photos of letters and receipts on the volume `lifeos-inbox-media`) are kept too: each run copies the images `inbox_db`'s snapshot names into a shared store, adding only those it lacks, and lists them (see [Backup: capture images](../services/backup.md#capture-images)).

Not backed up, and why (see [What is backed up](../services/backup.md#what-is-backed-up)):

- `.env` and `~/life-os.secrets`: every password and key. Keep them yourself, apart from the backups (a password manager). Without `LIFE_OS_BANKING_TOKEN_KEY` and `LIFE_OS_GOOGLE_TOKEN_KEY` a restored bank session and Google connection must be connected again.
- The Enable Banking key file and the Codex session (`~/.codex`).
- Voice audio (kept 7 days, its text is in Inbox and Voice), the Whisper models (downloaded again), and ntfy's data (provisioned from `.env` at every start).
- The code and `infra/`, which are in Git.

## Where they are

In `LIFE_OS_BACKUP_DIR` on the host (`~/life-os.backups`, mode 700), under `auto/` and `media/`. Each run is a directory named by its start time in UTC, such as `auto/20261011T011500Z/`, with `manifest.json`, one `<database>.dump` (pg_dump custom format) per database, and `media-inbox.json`, the capture images it stored. The images themselves are in `media/<first two hex digits>/<sha256>`, each once for every run that lists it. `auto/history.json` lists the latest runs and `auto/restore-checks.json` the latest restore checks. Anything else in the directory, such as manual dumps, is never touched.

## Retention

The newest successful full backup of each of the last 7 days, 4 weeks, and 12 months that have one (`LIFE_OS_BACKUP_KEEP_DAILY`, `…_WEEKLY`, `…_MONTHLY`), about 20 runs covering a year. Failed runs are kept 3 days (`LIFE_OS_BACKUP_KEEP_FAILED_DAYS`), and runs of only some databases (manual ones, and the fresh dump `tools/restore-database` takes) 7 days. A capture image stays while a kept run lists it. The newest successful backup is never deleted.

## Checking a backup

- **The status.** The Dashboard's Status page shows the Backups check: the last backup, the last restore check, the next backup, and the free disk, with **Back up now**; the monitor notifies you when it needs attention (see [Dashboard](../frontend/dashboard.md)). From the host:

  ```sh
  docker compose exec -T backup wget -qO- http://127.0.0.1:8080/v1/status | jq '{lastRun: .lastRun | {id, result, error}, lastSuccess: .lastSuccess.id, lastRestoreCheck: .lastRestoreCheck | {id, runId, ok, error}, storage}'
  ```

- **A run's manifest.** `jq '{result, error, databases: [.databases[] | {name, result, bytes, rows: ([.tables[].rows] | add), migration, error}]}' ~/life-os.backups/auto/<run>/manifest.json`. To check a dump file by hand: `sha256sum ~/life-os.backups/auto/<run>/<database>.dump` against the manifest's `sha256`.
- **The restore check.** Every week, after the Sunday nightly run (`LIFE_OS_BACKUP_RESTORE_CHECK_DAY`, or `off`), the backup service restores every dump of the newest full backup, one database at a time, into a scratch database `lifeos_restore_check_<database>`, and compares every table's row count and the migration version and dirty flag with the manifest. It checks each dump's SHA-256 first, restores as `lifeos_backup` (without owners and privileges, which that role cannot set), and always drops the scratch database, also after a failure. It also checks the run's capture images: each must be in the store with its size, and they are hashed until 4 GiB have been read. If a Sunday is missed, the next nightly run is followed by one; the very first backup is followed by one too. To run one now and wait for it:

  ```sh
  docker compose exec -T backup /service restore-check                      # the newest full backup
  docker compose exec -T backup /service restore-check -run 20261011T011500Z
  ```

  It prints one line per database: `ok` with its tables and rows, `differs` with each difference (`public.accounts: expected 6 rows, restored 5 rows`), or `failed` with the error. It exits 0 when every database passed. A check needs about as long as the backup itself, and no backup can start while it runs.

## Restoring one database

`tools/restore-database` restores one database from a run, on the stack of the checkout it runs in.

```sh
tools/restore-database finance latest              # shows the plan, changes nothing
tools/restore-database finance 20261011T011500Z --confirm
```

The database is named by service or database name; `latest` is the newest run with a good dump of it. Without `--confirm` it checks the dump and prints the plan. With `--confirm` it:

1. checks the dump's size and SHA-256 against the manifest (it refuses unknown databases and runs, a failed dump, and any mismatch);
2. takes a fresh dump of the database as it is now, a backup run of that one database, and prints where it went (`~/life-os.backups/auto/<run>/finance_db.dump`);
3. stops the services connected to the database (for `voice_db`, `voice` and `voice-worker`) and the backup service, never PostgreSQL;
4. drops the database, creates it again owned by its registry role with the grants provisioning gives, and restores the dump as the PostgreSQL admin in one transaction, owners and privileges included;
5. compares every table's row count and the migration state with the manifest;
6. for Inbox, copies the run's capture images back onto the volume with `tools/restore-media`: only those it lacks, nothing deleted (a problem there is reported, but the services start anyway);
7. runs `<service>-migrate`, which brings an older backup's schema up to this release, and starts again the services that were running before;
8. prints a summary.

It drops and recreates the database rather than restoring over it with `pg_restore --clean`: `--clean` removes only what the dump contains, so a table a later migration created would survive next to the backup's older `schema_migrations`, and that migration would fail when it runs again.

If a step fails after the services were stopped, they stay stopped, and it prints how to go back: `tools/restore-database finance <fresh run> --confirm --no-fresh-dump` restores the dump taken in step 2. `--no-fresh-dump` skips step 2, only for a database that is gone or that you do not need back.

Everything written to that database since the backup is lost, and other services do not know: for example a Task's events stay in Activity. Rehearse first on the verification stack: `RESTORE_COMPOSE=tools/verify-compose LIFE_OS_BACKUP_DIR=${TMPDIR:-/tmp}/lifeos-verify-backups tools/restore-database finance latest --confirm`, after a backup there (`tools/verify-compose exec -T backup /service now`).

## Restoring capture images

`tools/restore-media` copies the capture images a run stored back onto the `inbox-media` volume, for example after losing that volume, or after `tools/restore-database inbox` reported a problem with them:

```sh
tools/restore-media latest              # shows what it would copy, changes nothing
tools/restore-media 20261011T011500Z --confirm
```

It copies only the images the volume lacks, checks each one's SHA-256 against its name first, and never deletes or overwrites a file; a damaged copy in the store is named and skipped (the command then exits 1). It runs in a one-off `inbox` container as root, which can read the backup directory and gives each file to the volume's owner with Inbox's mode (0640). Images whose captures are not in `inbox_db` are removed by Inbox's hourly pass, so restore `inbox_db` from the same run first when both are lost.

## Restoring everything after losing PostgreSQL's volume

When the volume `lifeos-postgres-data` is gone or unusable, but the host and `~/life-os.backups` are fine:

1. **Secrets.** Make sure `.env` has every database password, including `LIFE_OS_BACKUP_DB_PASSWORD`, from your own copy of it. The roles are created from it; the dumps do not contain them.
2. **Stop everything but PostgreSQL**, so nothing writes to empty databases: `docker compose stop` (never `down --volumes`). If the old volume still exists but is broken, keep it aside until you are done; removing it is your decision.
3. **A fresh PostgreSQL.** `docker compose up -d --no-build --no-deps postgres` on an empty `lifeos-postgres-data` runs provisioning: every role and empty database in the registry, and `lifeos_backup`.
4. **Pick the run**: the newest successful full run, `ls ~/life-os.backups/auto` and its `manifest.json` (`"result": "succeeded"`, `"scope": "all"`), ideally one whose restore check passed (`auto/restore-checks.json`).
5. **Restore every database**, without fresh dumps (there is nothing to keep):

   ```sh
   run=20261011T011500Z
   for db in $(jq -r '.databases[].name' ~/life-os.backups/auto/$run/manifest.json); do
     tools/restore-database "$db" "$run" --confirm --no-fresh-dump || break
   done
   ```

   Each one is restored with its owners and migrated, and `inbox_db` gets its capture images back (only those the volume lacks). The services stay stopped, because they were not running, so nothing (the backup service's catch-up run included) sees a database before its turn.
6. **Start the rest** as they were, recreating nothing: `docker compose start` (the Dashboard, the gateway, MCP, the processor, the tunnel).
7. **Check**: the Status page, then `tools/backup-now` and `docker compose exec -T backup /service restore-check`.

Everything since that run is lost. The bank and Google connections come back if their token keys in `.env` are the same; if not, connect them again.

## The registry inside the PostgreSQL container

The PostgreSQL container sees the registry twice: as the single file `/etc/lifeos/service-databases.conf` and inside the directory `/opt/lifeos/postgres/`. A single-file bind mount keeps the file it was created with: Git replaces the file on a pull, so that mount shows the old registry until the container is recreated, and a database added since is missing from it. The provisioning scripts therefore read `/opt/lifeos/postgres/service-databases.conf`, which always shows the checked-out file, and use `/etc/lifeos/` only where the directory is not mounted; they print which one they read. The same goes for the scripts themselves: the initdb copy `/docker-entrypoint-initdb.d/10-provision-service-databases.sh` is a single-file mount too, so `make db-provision` and `make db-provision-backup` run the copies in `/opt/lifeos/postgres/`. A new database's password is a different matter: PostgreSQL keeps the environment it was created with, so pass a new password with `docker compose exec -e` or recreate the container (see [Adding the Food service](deployment.md#adding-the-food-service)). The backup service reads the registry from its own directory mount, so it sees a new database at the next run.

If a backup or restore check says `lifeos_backup cannot connect to food_db: run make db-provision-backup`, the role lacks the grant for a database added after it was last provisioned: run `make db-provision-backup`.

## Limits

- The backups are on the same disk as PostgreSQL: they protect against mistakes, bad migrations, and corruption, not against losing the server. An encrypted copy to Cloudflare R2 is planned (P14-04).
- No point-in-time recovery: a restore brings back last night's state, and up to a day of changes is lost.
- Each database is restored on its own; the services exchange data only through APIs and catch up, but data one service holds about another's (Activity's events, Notifications' history) is not rolled back with it.
- `.env` and the other secrets are yours to keep.
