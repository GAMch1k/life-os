# Calendar feeds handoff (P11-08)

> **History.** This was the one-time upgrade to the P11-08 release, kept as a record. P11-09 removed the `calendar-feeds handoff` command and its compose service after Calendar's migration 000017 dropped the legacy feed columns it read; the steps below cannot be run on a current release. A stack still on a release before P11-08 has to go through the P11-08 release first (000017 refuses to run while Calendar still refreshes a feed itself).

The release that adds [calendar-feeds](../services/calendar-feeds.md) moves the ICS feeds, the ZAW waste calendar among them, out of Calendar. It is done once, in this order, so no feed is refreshed by two services, nothing is imported twice, and the feeds keep their schedule, validators, and last outcome. Nothing here prints the ZAW URL; keep it that way (no `docker compose config`, no `env` dumps of these services).

1. **Prepare `.env`**: add `LIFE_OS_CALENDAR_FEEDS_DB_PASSWORD` (a new random value). `LIFE_OS_ZAW_ICS_URL` and `LIFE_OS_CALENDAR_SOURCE_REFRESH_AT` stay as they are: calendar-feeds reads them now.
2. **Stop Calendar** so its old refresher cannot run during the handover:

   ```sh
   docker compose stop calendar
   ```

3. **Back up** `calendar_db` (the migration changes the feeds' sources and events):

   ```sh
   docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" -Fc calendar_db' > calendar_db-before-p11-08.dump
   ```

4. **Provision and migrate.** Recreate PostgreSQL so it sees the new password and the updated `infra/postgres/service-databases.conf`, provision `calendar_feeds_db` (idempotent), build the new images, and run both migrations: Calendar's 000015 (`calendar_feeds_handover`: the feeds' sources and events become kind `external`, out of process) and calendar-feeds' own.

   ```sh
   docker compose up -d --no-build --no-deps postgres
   make db-provision
   docker compose build --pull calendar calendar-feeds calendar-feeds-handoff dashboard
   docker compose run --rm calendar-migrate
   docker compose run --rm calendar-feeds-migrate
   ```

5. **Hand off** the feeds (reads `calendar_db` in a read-only transaction, writes `calendar_feeds_db` in one transaction, prints counts only; running it again changes nothing):

   ```sh
   docker compose --profile handoff run --rm calendar-feeds-handoff
   ```

   Expect `calendar feeds found in Calendar: N; handed over now: N; already handed over: 0; presets: 1; …; still refreshed by Calendar (run its migrations first): 0`. A non-zero last count means step 4's Calendar migration did not run: run it, then the handoff again.

   Outside Compose the command is `calendar-feeds handoff` with `DATABASE_URL` (calendar_feeds_db, migrated) and `HANDOFF_CALENDAR_DATABASE_URL` (calendar_db, read only).

6. **Start** Calendar, calendar-feeds, and what depends on them:

   ```sh
   docker compose up -d --no-build --no-deps calendar calendar-feeds dashboard
   docker compose up -d --no-build --no-deps --force-recreate gateway
   ```

   At its start calendar-feeds ensures the ZAW feed (`docker compose logs calendar-feeds` says `ZAW feed ensured` with `action unchanged` and the feed id, never the URL), registers each handed-over feed again (Calendar keeps the owner's names, reminders, and priorities), and refreshes what is due.

7. **Verify:**
   - Status lists **Calendar feeds** ready under Services, and **Calendar sources** shows the feeds with their last refresh from before the handoff.
   - `curl -s -H 'X-Life-OS-Actor: dev:claude-code' -X POST http://127.0.0.1:3000/api/integrations/calendar-feeds/v1/feeds/<ZAW feed id>/refresh | jq '.refresh | {lastOutcome, lastCounts}'` shows `imported` with `inserted: 0`: the existing pickups were updated in place or unchanged, none duplicated (the feed id is the ZAW source's id from `GET /api/calendar/v1/sources`).
   - The Event Log's **Calendar feeds** domain shows "Source refreshed: ZAW — …" by "Calendar feed (zaw)".
   - The Calendar shows the pickups labelled "ZAW" with their reminders as before.

**Rolling back** before P11-09: stop calendar-feeds, migrate Calendar down one step (000015 returns the handed-over feeds and their events to kind `ics`, in process), and deploy the previous release; or restore the dump from step 3. The legacy feed columns are dropped only by P11-09.
