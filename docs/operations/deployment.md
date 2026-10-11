# Operations and troubleshooting

Docker Compose runs PostgreSQL, one migration job and one service per domain, Dashboard, and gateway. Migration jobs should exit successfully; long-running services and gateway should remain up. Start with `make life-up`; stop non-destructively with `make infra-down`.

Start with the Dashboard Status page (`/status`). It reports a stopped processor, a signed-out or rate-limited Codex, stuck or retrying Inbox and Voice work, a stopped Whisper or Voice worker, and unready services, each with a suggested action. If a page fails, test the gateway (`curl http://127.0.0.1:3000/` by default), then `/api/<domain>/ready`, then inspect `docker compose logs <service>` and its migration job. A 502 normally means an unavailable upstream; a bind error means the configured gateway address/port is unavailable; an empty reply means the Host is not one the gateway serves (it answers only `lifeos.gamch1k.com`, `10.66.66.1`, `127.0.0.1`, and `localhost`; see [Gateway hosts](../architecture/networking.md#gateway-hosts)). Do not publish individual service ports as a diagnostic shortcut.

Life OS supports loopback gateway behind host Nginx TLS/Basic Auth, or a WireGuard-bound gateway. It can additionally be reached through a Cloudflare Tunnel guarded by Cloudflare Access, using an outbound-only `cloudflared` connector on the private network ([Cloudflare Tunnel and Access](cloudflare.md)). The deployed WireGuard model trusts every allowed peer to call Dashboard, write APIs, and MCP without an application login; never expose it publicly or claim host-Nginx protection for it. Host Nginx, WireGuard peers, firewall, and DNS remain host-administration tasks and are never changed by repository commands. Gateway baseline limits, headers, proxy timeouts, and forwarded-header sanitisation are versioned here. Dashboard is same-origin; cookie/session auth needs Origin/CSRF protection before it is introduced. The [backup service](../services/backup.md) dumps every database each night to the host's backup directory (see [Backups](#backups)); a supported restore workflow and restore checks are not implemented yet (P14-02), so test a restore from a dump yourself before relying on it.

## Adding the Activity service to an existing deployment

`activity_db` is provisioned automatically only on a fresh PostgreSQL volume. On an existing host, add `LIFE_OS_ACTIVITY_DB_PASSWORD` to `.env` and run `make db-provision` once (idempotent). Then start the stack so `activity-migrate` and `activity` come up before the gateway. Until Activity is reachable, services keep working and only skip recording events.

## Adding the Notifications service to an existing deployment

`notifications_db` is provisioned automatically only on a fresh PostgreSQL volume. On an existing host, add `LIFE_OS_NOTIFICATIONS_DB_PASSWORD` to `.env`, then recreate the PostgreSQL container (`docker compose up --detach postgres`) so it sees the new variable and the updated `infra/postgres/service-databases.conf`, and run `make db-provision` once (idempotent). Then start the stack so `notifications-migrate` and `notifications` come up before the gateway, which routes `/api/notifications/` and waits for the service. No channel is configured by default (`LIFE_OS_NOTIFICATIONS_CHANNELS` empty): notifications are stored and listed only. See [Notifications](../services/notifications.md).

`banking_db` is provisioned like `notifications_db`: add `LIFE_OS_BANKING_DB_PASSWORD` to `.env`, recreate the PostgreSQL container (`docker compose up --detach postgres`), and run `make db-provision` once. Then start `banking-migrate` and `banking` before the gateway, which routes `/api/integrations/banking/` and waits for the service. Without the Enable Banking settings Banking runs and reports "not configured"; setting them up (application id, the key file bind-mounted read-only, `LIFE_OS_BANKING_TOKEN_KEY`, `LIFE_OS_BANKING_RUNTIME_UID`), connecting, reconnecting, and revoking are in [Bank connection](banking.md). The verification stack never receives these settings.

## Adding the Food service

[Food](../services/food.md) (P15-01) owns `food_db`. A fresh PostgreSQL volume provisions it; on an existing host, add `LIFE_OS_FOOD_DB_PASSWORD` to `.env` and provision it once (idempotent). PostgreSQL keeps the environment it was created with, so either recreate it (`docker compose up --detach postgres`) and run `make db-provision`, or, without restarting it, pass the new password and read the registry from the directory mount, which sees the updated `infra/postgres/service-databases.conf`:

```sh
docker compose exec -T \
  -e LIFE_OS_FOOD_DB_PASSWORD="$(sed -n 's/^LIFE_OS_FOOD_DB_PASSWORD=//p' .env)" \
  -e LIFE_OS_DATABASE_CONFIG=/opt/lifeos/postgres/service-databases.conf \
  postgres /docker-entrypoint-initdb.d/10-provision-service-databases.sh
```

Then build and start `food-migrate` and `food` before the Dashboard and the gateway: the gateway routes `/api/food/` and waits for the service, and the Dashboard's Status page probes Food's `/ready`, so a Dashboard deployed first reports Food as unavailable (and the monitor notifies) until it runs. Food has no settings of its own beyond `LIFE_OS_TIMEZONE`.

## Phone notifications (ntfy)

Optional. The `ntfy` container (Compose profile `ntfy`) pushes notifications to the owner's Android phone; the Notifications service publishes to it when `LIFE_OS_NOTIFICATIONS_CHANNELS=ntfy`. It shares `notify-edge` with the Cloudflare connector only and the internal `notify-internal` with the Notifications service only, and is reached publicly at `https://ntfy.gamch1k.com` through the tunnel without Access and protected by ntfy's own deny-all accounts. Generating the `.env` values (`LIFE_OS_NTFY_PUBLISHER_TOKEN` and the two bcrypt hashes, in single quotes), the deploy order (ntfy, then notifications, dashboard, and cloudflared, then the gateway), the checks (`tools/check-ingress`, `tools/check-cloudflare-access`, `tools/check-ntfy-route`), the phone setup, rotation, and troubleshooting are in [ntfy](ntfy.md). The verification stack never receives these settings.

A deploy of this change also recreates the gateway (`docker compose up -d --force-recreate --no-deps gateway`): it now answers only its listed hosts. Check WireGuard (`curl -s -o /dev/null -w '%{http_code}\n' http://10.66.66.1:3001/` gives 200) and the public path (`tools/check-cloudflare-access`) afterwards.

## Adding the monitor to an existing deployment

The [monitor](../services/monitor.md) needs no database, secret, or `.env` change (`LIFE_OS_MONITOR_INTERVAL_SECONDS` and `LIFE_OS_MONITOR_CONFIRMATIONS` are optional). Rebuild and start it with the Dashboard, which gains `/api/status` and probes the monitor's readiness: `docker compose up --detach --build dashboard monitor`. Status then lists Monitor under Services. A first deployment has no notification history, so a check that is already a problem is notified after the confirmations (two minutes at the defaults).

## Backups

The [backup service](../services/backup.md) dumps every database in `infra/postgres/service-databases.conf` at `LIFE_OS_BACKUP_TIME` (03:15) in `LIFE_OS_TIMEZONE`, from one consistent snapshot per database, into `LIFE_OS_BACKUP_DIR/auto/` on the host, and rotates the runs (7 daily, 4 weekly, 12 monthly). `.env`, the other secrets, Voice audio, and the Whisper models are not backed up; the page says why. To add it to an existing deployment:

1. The backup directory must exist, owned by the runtime UID, mode 700 (`install -d -m 700 ~/life-os.backups`). Manual dumps already in it are left alone; the service writes only under `auto/`.
2. Add to `.env`: `LIFE_OS_BACKUP_DIR` (its absolute path; compose refuses every command without it), `LIFE_OS_BACKUP_RUNTIME_UID` (`id -u`), and `LIFE_OS_BACKUP_DB_PASSWORD` (letters and digits, such as `openssl rand -hex 24`). `LIFE_OS_BACKUP_TIME` and `LIFE_OS_BACKUP_KEEP_*` are optional.
3. Provision the read-only `lifeos_backup` role on the running server, without a restart: `make db-provision-backup` (it runs `docker compose exec -T -e LIFE_OS_BACKUP_DB_PASSWORD postgres bash /opt/lifeos/postgres/provision-backup-role.sh` with the password from `.env`). A fresh volume gets the role automatically, and `make db-provision` keeps its grants.
4. Build and start the service alone: `docker compose build backup && docker compose up -d --no-build --no-deps backup`. A minute later it runs a first (catch-up) backup; `docker compose logs backup` shows it, and `tools/backup-now` runs one on demand.

The verification stack writes its backups to a scratch directory of its own; `tools/verify-compose` refuses a plan with the owner's.

## Safe verification on a deployed host

Do not use raw `docker compose down --volumes` with the normal deployment to
test migrations or smoke flows. `make verify-clean-db` and `make e2e-smoke`
run through `tools/verify-compose`, which refuses a Compose plan unless every
test network and persistent volume begins with `lifeos-verify-`. Its cleanup
may remove only those disposable resources. `make verify-isolation` also
creates temporary sentinel files in normal PostgreSQL, Voice-audio, and
Whisper-model volumes, runs the disposable smoke suite, and confirms the
sentinels remain before cleaning them up.
## Codex Inbox processor authentication

Automatic Inbox processing is a normal Compose service and uses the host user's existing authenticated Codex CLI session. Before `docker compose up --build`, configure the ignored `.env` without copying its contents into Git:

```sh
LIFE_OS_CODEX_HOME_HOST_PATH=/absolute/path/to/.codex
LIFE_OS_CODEX_RUNTIME_UID=1000 # output of: id -u
LIFE_OS_CODEX_MODEL=gpt-5.6-luna
LIFE_OS_CODEX_REASONING_EFFORT=medium
LIFE_OS_TIMEZONE=Europe/Berlin # your IANA timezone (fallback); default UTC
```

The processor bind-mounts that path read-only, runs as the owning UID, and copies only the authentication and optional Codex configuration to a container tmpfs. Do not mount a credential as a writable volume and do not add `auth.json` or API keys to `.env`. After deployment, verify from the processor container itself with `docker compose exec processor codex login status`; it should report the already authenticated ChatGPT session. Its private readiness probe only becomes ready after a successful polling pass through MCP.

## Codex runtime settings

Optional `.env` settings for the processor:
- `LIFE_OS_CODEX_TIMEOUT_SECONDS` (default 300): the deadline of one planning run.
- `LIFE_OS_CODEX_MAX_CONCURRENCY` (default 1): concurrent Codex runs.
- `LIFE_OS_PROCESSOR_STALE_AFTER_SECONDS` (default 120, at least 90): when an attempt left by a crashed processor is reconciled.

## Owner timezone

The owner's timezone is the browser's own timezone when it is known, else `LIFE_OS_TIMEZONE` (an IANA name, default `UTC`), else UTC. Set `LIFE_OS_TIMEZONE` to where the owner usually is, before the processor, Calendar, calendar-feeds, and Dashboard start; changing it needs a restart of them (`docker compose up -d processor calendar calendar-feeds tasks dashboard`). An unknown name falls back to UTC; the processor and Calendar log it at startup and the processor's zone is listed under Automatic processing on `/status`.

- **Dashboard.** Every "today", date, and time is shown in the browser's timezone, which the browser reports in the `lifeos-tz` cookie; until it has (the very first page load) and for clients without it, in `LIFE_OS_TIMEZONE`. See [Dashboard](../frontend/dashboard.md).
- **Calendar.** All-day events are dates; reads that compare them with instants use the `timeZone` the client passes (the Dashboard passes the zone it shows), else `LIFE_OS_TIMEZONE`. See [Calendar](../services/calendar.md).
- **Tasks.** A due date without a time is the owner-local midnight, and Task notifications (the reminders, the morning and evening lists, the morning summary) are due on the owner's wall clock. See [Tasks](../services/tasks.md#notifications).
- **Processor.** Captures such as "tomorrow at 10" or "on Friday at 15:00" are resolved in the capturing browser's timezone (sent as `sourceMetadata.timeZone` with text and voice captures), else in `LIFE_OS_TIMEZONE`, from the moment the capture was made, and stored as the correct instants. See [MCP and AI](../services/mcp.md#dates-and-the-owners-timezone).

Stored data and API payloads stay UTC instants. Changing the timezone, or travelling, does not rewrite them: Tasks and events keep their instants and are shown in the new zone.

A redeploy or `docker compose stop processor` cancels a run in progress and hands the capture back, so the capture is not lost. See [MCP and AI](../services/mcp.md#codex-runtime).

## Calendar sources

The [calendar-feeds](../services/calendar-feeds.md) service refreshes the subscribed ICS feeds and syncs their events into Calendar through its integration API (P11-08); Calendar downloads no feed. `calendar_feeds_db` is provisioned like `notifications_db`: add `LIFE_OS_CALENDAR_FEEDS_DB_PASSWORD` to `.env`, recreate the PostgreSQL container (`docker compose up --detach postgres`), and run `make db-provision` once. Then start `calendar-feeds-migrate` and `calendar-feeds` before the gateway, which routes `/api/integrations/calendar-feeds/` and waits for the service. (The P11-08 release handed the existing feeds over once; see [Calendar feeds handoff](calendar-feeds-handoff.md), kept as a record.) Optional `.env` settings:

- `LIFE_OS_CALENDAR_SOURCE_REFRESH_AT` (default `04:30`): the daily refresh time, HH:MM in `LIFE_OS_TIMEZONE` (calendar-feeds' `CALENDAR_FEEDS_REFRESH_AT`).
- `LIFE_OS_ZAW_ICS_URL` (default empty): the owner's personal ZAW waste-collection feed (the MyMüll iCal export, `https://service.mymuell.de/api.php?m=zaw&r=ical&city_id=<city>&area_id=<area>&…`). When set, calendar-feeds keeps one "ZAW" feed for it, created or updated at its start and imported right away; change it with `docker compose up -d calendar-feeds`. See [ZAW waste collection](../services/calendar-feeds.md#zaw-waste-collection). The verification stack never receives it.

calendar-feeds needs outbound https from the `backend` network to reach feeds, and refuses feeds on private or loopback addresses. A refresh outcome (and the last error) is on the feed (`GET /api/integrations/calendar-feeds/v1/feeds`), on Status, and in the Event Log (domain "Calendar feeds"); a failing feed keeps its last good events and is retried with backoff. Keep personal feed URLs (they can carry a street) in the ignored `.env` or in calendar-feeds, never in the repository.

## Google Calendar

Optional. Google Calendar is the google-calendar integration service since P11-07 (see [google-calendar](../services/google-calendar.md)). `google_calendar_db` is provisioned like the others: add `LIFE_OS_GOOGLE_CALENDAR_DB_PASSWORD` to `.env`, recreate the PostgreSQL container (`docker compose up --detach postgres`), and run `make db-provision` once; then `google-calendar-migrate` and `google-calendar` start before the gateway, which routes `/api/integrations/google-calendar/`. The Google settings (`LIFE_OS_GOOGLE_CLIENT_ID`, `LIFE_OS_GOOGLE_CLIENT_SECRET`, `LIFE_OS_GOOGLE_TOKEN_KEY`, `LIFE_OS_GOOGLE_REDIRECT_URI`, `LIFE_OS_CALENDAR_GOOGLE_SYNC_SECONDS`, and the new `LIFE_OS_GOOGLE_CALENDAR_FEED_SECONDS`) go to the integration; Calendar no longer reads them. Without them the integration runs and the Calendar page says Google is not set up. The owner's one-time setup (Google Cloud project, consent screen in production, Web client with the redirect URI `https://lifeos.gamch1k.com/calendar/google/callback`, the `.env` lines, `openssl rand -base64 32` for the key) is in [Google Calendar setup](google-calendar.md). The verification stack never receives these settings (`tools/verify-compose` blanks them and refuses a plan with them; `compose.verify.yaml` points the integration's Google endpoints nowhere). Back up the token key with the other secrets: without it the stored refresh token cannot be decrypted and Google must be reconnected.

**Calendar contract (P11-09).** Calendar's migration `000017_calendar_contract` drops the legacy provider data the P11-07 and P11-08 handovers left in `calendar_db` (the `google_*` tables, the feed and Google columns of `calendar_sources`, `in_process`, the `ics`/`google` kinds; see [Calendar: contract](../services/calendar.md#contract-p11-09)). Back up `calendar_db` first (`pg_dump`); the down migration rebuilds the structure, not the data. It refuses to run, changing nothing, while any source is still synced by Calendar itself or of kind `ics`/`google`, so a stack from before P11-07 or P11-08 goes through those releases and their handoffs first; after such a refusal golang-migrate leaves `calendar_db` dirty at version 17: fix the cause, `migrate force 16`, and migrate again. Deploy Calendar, calendar-feeds, google-calendar, the Dashboard, and MCP from the same release: the Calendar contract no longer has `inProcess`, the `ics`/`google` kinds, or the sources' `url`/`filter`/`preset`/`refresh`.

## Task notifications

Tasks sends its reminders, the morning and evening lists, and the morning summary through Notifications (see [Tasks](../services/tasks.md#notifications)). Migration `000004_task_notifications` adds the dispatcher state on the next `tasks-migrate` run; compose gives Tasks `NOTIFICATIONS_SERVICE_URL`, `CALENDAR_SERVICE_URL`, and `LIFE_OS_TIMEZONE`. Optional `.env` settings, HH:MM in `LIFE_OS_TIMEZONE`: `LIFE_OS_TASKS_SUMMARY_TIME` (08:00), `LIFE_OS_TASKS_MORNING_TIME` (09:00; the same as the summary time sends one message), `LIFE_OS_TASKS_EVENING_TIME` (19:00), and `LIFE_OS_TASKS_TIMED_LEAD_MINUTES` (30); apply them with `docker compose up -d tasks`. The first run after the deployment only starts the cursor, so nothing due before it is sent. Health: `GET /v1/notifications/status` on Tasks (inside the stack).

## Voice worker recovery

Voice jobs use a persisted lease instead of treating `processing` as permanent ownership. Configure `LIFE_OS_VOICE_WORKER_LEASE_SECONDS`, `LIFE_OS_VOICE_WORKER_RETRY_BACKOFF_SECONDS`, and `LIFE_OS_VOICE_WORKER_MAX_ATTEMPTS` in the ignored `.env` if the defaults are unsuitable. On restart, the normal `voice-worker` automatically reclaims only expired leases; do not edit Voice job rows manually. A job that repeatedly loses its lease or fails will eventually appear as `failed` from `GET /api/voice/v1/audio-jobs/{id}` with its attempt count and error summary.

## Voice upload limit

A recording may be up to 25 MiB (`LIFE_OS_VOICE_MAX_UPLOAD_BYTES`, default 26214400). The limit appears in three places that must change together: `LIFE_OS_VOICE_MAX_UPLOAD_BYTES` in `.env` (Voice also allows 1 MiB of multipart framing on top), `client_max_body_size 26m` in `infra/nginx/life-os.conf`, and `VOICE_MAX_UPLOAD_BYTES` in `apps/dashboard/src/lib/voice-job-state.ts`, which the Dashboard checks before uploading. Through Cloudflare an over-limit upload may show as 502 rather than 413; the Dashboard's own check is what users normally see. See [Voice and STT](../services/voice-stt.md).

The Whisper image converts recordings with ffmpeg. After a change under `services/voice/whisper/`, rebuild it (`docker compose build whisper`) together with `voice` and `voice-worker` before redeploying.
