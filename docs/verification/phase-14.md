# Phase 14 verification

Phase 14 makes Life OS's data recoverable. Every night the [backup service](../services/backup.md) dumps every service database from one consistent snapshot each, and it now also copies the Inbox capture photos. Old runs are rotated. A weekly restore check proves that the dumps restore with the same row counts, and the Status page and the phone say when backups stop. The [runbook](../operations/backups.md) covers restoring one database or everything. For now, copies stay on this server. An encrypted off-site copy to Cloudflare R2 is planned as P14-04, and the owner has a reminder for 17 Oct 2026.

Unit and integration tests prove the pieces against a throwaway PostgreSQL 18. This sign-off adds the live runs on the deployed stack (2026-10-11 and 2026-10-12) and a restore rehearsal on the disposable `lifeos-verify` stack.

## Commands

| Command | What it proves |
| --- | --- |
| `go test -race ./...` in `services/backup` (with `BACKUP_TEST_DATABASE_URL`) | **Snapshot:** 10 rows committed between the snapshot and `pg_dump` are in neither the manifest nor the dump; removing `--snapshot` makes the test fail. **Restore checks:** they catch a changed row count, a changed migration and a damaged dump. **Lifecycle:** schedule and DST, catch-up, rotation, the lock, interrupted runs, and media copy and rotation are all covered. |
| `tools/test-restore-database`, `tools/test-restore-media` | **The restore tools:** they refuse mismatches. A restore brings back rows, owners, grants and the migration state, and removes later tables. Media restores only add missing files and never delete. |
| `make browser-e2e` | **The Backups card:** it shows the backups, and Back up now runs one. A stopped backup service is reported once, by Services. |
| `make db-provision-backup` | **The backup role:** the read-only `lifeos_backup` role is reconciled on the running server from the current registry, with no restart. |

## Required acceptance

| # | Required | Evidence |
| --- | --- | --- |
| 1 | The first live nightly run covers every database | Scheduled run `20261012T011500Z` (03:15 CEST): **succeeded**, 11 databases, 3,776,569 bytes. The media section recorded 0 Inbox images, since no photo has been captured yet. The earlier catch-up run `20261011T025119Z` succeeded too, with 11 databases in 3.3 s. Each manifest has per-table row counts and the migration version (none dirty). |
| 2 | A restore check of the real data passes | `20261011T033347Z`, a check of backup `20261011T025119Z`, **passed in 4.2 s, 11/11**. Every table's row count matched the manifest: finance 51, tasks 54, inbox 30837, calendar 95, voice 13, activity 268, notifications 31, banking 3, google_calendar 53, calendar_feeds 2, food 196. No scratch database was left behind. |
| 3 | A restore is rehearsed with the tool | On the verify stack: a Task was created, a backup was taken, and a second Task was created. `tools/restore-database tasks latest` first printed the plan and changed nothing. With `--confirm` it took a fresh dump, stopped only `tasks` and `backup`, recreated `tasks_db`, restored it, matched the manifest, ran migrate ("no change"), and started both again. Only the first Task remained. |
| 4 | Backups going missing reach Status and the phone | **On Status:** the `backups` check's state rules have unit tests (no success for 26 h or 50 h, failed or interrupted runs, failed restore checks, low disk, a missing restore check). The browser suite covers the card and a stopped service. **On the phone:** the monitor needs no new code, because every Status check becomes `status.<id>` notifications (see [Monitor](../services/monitor.md)). That path delivered live `status.server` notifications on 2026-10-11. A real 26-hour gap was not waited for. |
| 5 | Inbox photos are included | `backup` mounts `lifeos-inbox-media` read-only (group 101). The photo list is read inside the `inbox_db` snapshot, and files go into a content-addressed store. The log reads "backing up the Inbox images". |

## Results

- Verification of each ticket ran on `master` before its deploy, one job at a time: `tools/check` passed, and the browser suite was 87 → 94 → 108 passed with 0 failed as the phase progressed. The smokes passed.
- Live, 2026-10-11:
  - `lifeos_backup` was first granted CONNECT on only 8 of 11 databases. The postgres container's single-file registry mount was stale (9 lines instead of 12), and rerunning from the directory mount granted all 11. The provisioning scripts and Make targets now read `/opt/lifeos/postgres/service-databases.conf`.
  - The Backups card read **ok**: "Last backup … (42 min ago)", 11 databases, next run 03:15, last restore check passed.
- Live, 2026-10-12: the first scheduled nightly run succeeded (row 1).

## Not covered yet

- **Off-site copy (P14-04):** waiting for the owner to set up Cloudflare R2.
- **The weekly scheduled restore check:** it first runs on Sunday 18 Oct 2026. The checks so far were on-demand runs of the same code.
- **Point-in-time recovery:** WAL archiving is out of scope.
