# Operations and troubleshooting

Docker Compose runs PostgreSQL, one migration job and one service per domain, Dashboard, and gateway. Migration jobs should exit successfully; long-running services and gateway should remain up. Start with `make life-up`; stop non-destructively with `make infra-down`.

Start with the Dashboard Status page (`/status`). It reports a stopped processor, a signed-out or rate-limited Codex, stuck or retrying Inbox and Voice work, a stopped Whisper or Voice worker, and unready services, each with a suggested action. If a page fails, test the gateway (`curl http://127.0.0.1:3000/` by default), then `/api/<domain>/ready`, then inspect `docker compose logs <service>` and its migration job. A 502 normally means an unavailable upstream; a bind error means the configured gateway address/port is unavailable. Do not publish individual service ports as a diagnostic shortcut.

Life OS supports loopback gateway behind host Nginx TLS/Basic Auth, or a WireGuard-bound gateway. It can additionally be reached through a Cloudflare Tunnel guarded by Cloudflare Access, using an outbound-only `cloudflared` connector on the private network ([Cloudflare Tunnel and Access](cloudflare.md)). The deployed WireGuard model trusts every allowed peer to call Dashboard, write APIs, and MCP without an application login; never expose it publicly or claim host-Nginx protection for it. Host Nginx, WireGuard peers, firewall, and DNS remain host-administration tasks and are never changed by repository commands. Gateway baseline limits, headers, proxy timeouts, and forwarded-header sanitisation are versioned here. Dashboard is same-origin; cookie/session auth needs Origin/CSRF protection before it is introduced. Automated backup/restore is not implemented: operators must use an external PostgreSQL process and test restores independently until a supported workflow exists.

## Adding the Activity service to an existing deployment

`activity_db` is provisioned automatically only on a fresh PostgreSQL volume. On an existing host, add `LIFE_OS_ACTIVITY_DB_PASSWORD` to `.env` and run `make db-provision` once (idempotent). Then start the stack so `activity-migrate` and `activity` come up before the gateway. Until Activity is reachable, services keep working and only skip recording events.

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

The owner's timezone is the browser's own timezone when it is known, else `LIFE_OS_TIMEZONE` (an IANA name, default `UTC`), else UTC. Set `LIFE_OS_TIMEZONE` to where the owner usually is, before the processor, Calendar, and Dashboard start; changing it needs a restart of them (`docker compose up -d processor calendar dashboard`). An unknown name falls back to UTC; the processor and Calendar log it at startup and the processor's zone is listed under Automatic processing on `/status`.

- **Dashboard.** Every "today", date, and time is shown in the browser's timezone, which the browser reports in the `lifeos-tz` cookie; until it has (the very first page load) and for clients without it, in `LIFE_OS_TIMEZONE`. See [Dashboard](../frontend/dashboard.md).
- **Calendar.** All-day events are dates; reads that compare them with instants use the `timeZone` the client passes (the Dashboard passes the zone it shows), else `LIFE_OS_TIMEZONE`. See [Calendar](../services/calendar.md).
- **Processor.** Captures such as "tomorrow at 10" or "on Friday at 15:00" are resolved in the capturing browser's timezone (sent as `sourceMetadata.timeZone` with text and voice captures), else in `LIFE_OS_TIMEZONE`, from the moment the capture was made, and stored as the correct instants. See [MCP and AI](../services/mcp.md#dates-and-the-owners-timezone).

Stored data and API payloads stay UTC instants. Changing the timezone, or travelling, does not rewrite them: Tasks and events keep their instants and are shown in the new zone.

A redeploy or `docker compose stop processor` cancels a run in progress and hands the capture back, so the capture is not lost. See [MCP and AI](../services/mcp.md#codex-runtime).

## Voice worker recovery

Voice jobs use a persisted lease instead of treating `processing` as permanent ownership. Configure `LIFE_OS_VOICE_WORKER_LEASE_SECONDS`, `LIFE_OS_VOICE_WORKER_RETRY_BACKOFF_SECONDS`, and `LIFE_OS_VOICE_WORKER_MAX_ATTEMPTS` in the ignored `.env` if the defaults are unsuitable. On restart, the normal `voice-worker` automatically reclaims only expired leases; do not edit Voice job rows manually. A job that repeatedly loses its lease or fails will eventually appear as `failed` from `GET /api/voice/v1/audio-jobs/{id}` with its attempt count and error summary.

## Voice upload limit

A recording may be up to 25 MiB (`LIFE_OS_VOICE_MAX_UPLOAD_BYTES`, default 26214400). The limit appears in three places that must change together: `LIFE_OS_VOICE_MAX_UPLOAD_BYTES` in `.env` (Voice also allows 1 MiB of multipart framing on top), `client_max_body_size 26m` in `infra/nginx/life-os.conf`, and `VOICE_MAX_UPLOAD_BYTES` in `apps/dashboard/src/lib/voice-job-state.ts`, which the Dashboard checks before uploading. Through Cloudflare an over-limit upload may show as 502 rather than 413; the Dashboard's own check is what users normally see. See [Voice and STT](../services/voice-stt.md).

The Whisper image converts recordings with ffmpeg. After a change under `services/voice/whisper/`, rebuild it (`docker compose build whisper`) together with `voice` and `voice-worker` before redeploying.
