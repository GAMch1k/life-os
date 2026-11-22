# Backups and restore

Life OS backs itself up. Every night at 03:15 in your timezone (`LIFE_OS_BACKUP_TIME`), the `backup` service saves every database, each from one consistent snapshot, and the photos you captured, into `backups/` next to `setup.sh`. It keeps one backup of each of the last 7 days, 4 weeks, and 12 months. Every Sunday it restores the newest backup into a scratch database and checks that nothing is missing. The first backup runs about a minute after the first start.

The **Status** page shows the last backup, the last restore check, and the free disk space, and has **Back up now**. If backups stop working, Status says so (and your phone, if [notifications](notifications.md) reach it).

## From the server

```sh
docker compose exec -T backup /service now             # back up now, and wait for the result
docker compose exec -T backup /service restore-check   # check the newest backup now
./setup.sh restore list                                 # the backups, newest first, in your timezone
```

`./setup.sh update` backs up before it updates, and stops if the backup fails. In `backups/auto`, each backup is a directory named by its start time in UTC, such as `20261121T021500Z`.

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

`./setup.sh restore` puts Life OS back as a backup has it. It replaces everything in Life OS: what was written after that backup is lost.

```sh
./setup.sh restore list                # the backups, newest first, in your timezone
./setup.sh restore                     # the newest backup
./setup.sh restore 2026-11-21          # the newest backup of that day
./setup.sh restore 20261121T021500Z    # one backup, by its name in backups/auto
```

It needs `jq` (`sudo apt install jq`), which `./setup.sh` warns about when it is missing. It lists the backups with their sizes, checks the one you chose, and asks before it changes anything, showing that backup's date and size. `--yes` answers for you, for scripts. Then it:

1. stops Life OS, except PostgreSQL and the backup service;
2. backs up what Life OS holds now, so you can go back;
3. restores every database of the backup: each is dropped, created again, filled from the backup, compared with it table by table, and brought up to this release;
4. copies back the photos the backup lists that the server lacks (it never deletes one);
5. starts Life OS again, waits until it answers, and says what it restored.

To undo a restore, restore the backup it took first: the last lines name it, such as `./setup.sh restore 20261122T130212Z`. The list marks it "taken before a restore", and `./setup.sh restore` without a name, or with a day, passes over such backups.

`--skip-backup` restores without the backup first, for when the backup service cannot run. What Life OS holds then is lost for good.

When the backup it chose is less than a quarter of the size of an older one, it warns before the question and names the bigger one: the newest backup may be an empty Life OS, such as a new server's ([On a new server](#on-a-new-server)). With `--yes` it then refuses, unless you named the backup by its day or its name.

If a database fails to restore, Life OS stays stopped, so that nothing uses it half restored. The command says why, and prints the commands to try again, to go back, or to start Life OS as it is.

A restore does not touch `.env` or the AI sign-in. Signed-in browsers, paired devices, and API tokens are as the backup has them: sign in again where Life OS asks.

### On a new server

1. Download the release's files as in the [Quick start](README.md#quick-start), steps 1 and 2: the release you ran, or a newer one. A backup restores into its own release or a newer one, never an older one.
2. Put your copy of `.env` next to `setup.sh`, and `chmod 600 .env`. With the same directory and the same user as before, nothing in it changes. Otherwise change what names the old server: the paths `LIFE_OS_BACKUP_DIR` and `LIFE_OS_CODEX_HOME_HOST_PATH`, the user `LIFE_OS_BACKUP_RUNTIME_UID`, `LIFE_OS_CODEX_RUNTIME_UID`, and `LIFE_OS_BANKING_RUNTIME_UID` (your `id -u`), and `LIFE_OS_DOCKER_GID` (`stat -c %g /var/run/docker.sock`).
3. Copy the backups into the directory `LIFE_OS_BACKUP_DIR` names (`backups/` next to `setup.sh`), owned by you, and `chmod 700` it.
4. `./setup.sh up` starts an empty Life OS. Ignore the setup code it prints: the restore brings your account back.
5. `./setup.sh restore list`, then `./setup.sh restore <the backup>` by its name. Name it: a minute after it starts, the empty Life OS backs itself up too, and that backup is the newest. (`./setup.sh restore` alone warns about that small backup, and with `--yes` refuses it.)

Rehearse this on another machine before you need it, and ask in the project's issues if you get stuck. A server that runs Life OS from the source code restores the same way; the [backups runbook](../operations/backups.md) has the details and the tools underneath.

More: [Backup service](../services/backup.md), and what it saves, how, and in which layout.
