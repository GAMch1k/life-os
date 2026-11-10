# Backups and restore

Life OS backs itself up. Every night at 03:15 in your timezone (`LIFE_OS_BACKUP_TIME`), the `backup` service saves every database, each from one consistent snapshot, and the photos you captured, into `backups/` next to `setup.sh`. It keeps one backup of each of the last 7 days, 4 weeks, and 12 months. Every Sunday it restores the newest backup into a scratch database and checks that nothing is missing. The first backup runs about a minute after the first start.

The **Status** page shows the last backup, the last restore check, and the free disk space, and has **Back up now**. If backups stop working, Status says so (and your phone, if [notifications](notifications.md) reach it).

## From the server

```sh
docker compose exec -T backup /service now             # back up now, and wait for the result
docker compose exec -T backup /service restore-check   # check the newest backup now
ls backups/auto                                         # the backups, named by their start time in UTC
```

`./setup.sh update` backs up before it updates, and stops if the backup fails.

## What is not in the backups

- **`.env`**, with every password and key. Keep your own copy, apart from the backups, such as in a password manager. A restore needs the database passwords from it, and the bank and Google connections need its token keys (without them, connect them again).
- **`codex-home/`**, the AI sign-in: sign in again with `./setup.sh codex`.
- **The bank's key file**, if you set up [bank sync](bank-sync.md).
- Voice recordings (their text is in Inbox), the Whisper model (downloaded again), and ntfy's data (made again from `.env`).

## Keep a copy elsewhere

The backups sit on the same disk as the database. They protect you from mistakes and broken updates, not from losing the server. Copy them somewhere else regularly, for example with `rsync` to another machine:

```sh
rsync -a backups/ you@other-machine:life-os-backups/
```

The directory is readable only by you; keep the copy private too, since it holds everything in Life OS. Store `.env` separately, encrypted.

## Restore

In this preview, restoring is a manual job: the [backups runbook](../operations/backups.md) has the steps, written for a server that runs Life OS from the source code. A restore command for the release files is planned. Rehearse a restore on another machine before you need one, and ask in the project's issues if you get stuck.

More: [Backup service](../services/backup.md), and what it saves, how, and in which layout.
