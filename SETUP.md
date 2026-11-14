# Self-hosting Life OS

This guide takes you from a fresh Linux server to a working Life OS. Follow the steps in order. Life OS has accounts and a login ([Accounts and signing in](docs/install/accounts.md)); the first account, the owner's, is made with a one-time setup code once Life OS runs. While [legacy access](docs/install/accounts.md#legacy-access) is on, as after updating a server from before accounts, anyone who can reach its gateway acts as the owner, so how you expose the gateway ([step 9](#9-remote-access)) stays your first line of defense.

This guide builds Life OS from this repository. To install the published images instead, with `setup.sh` and nothing to build, follow the [install guide](docs/install/README.md). `setup.sh` also writes this guide's `.env` (step 4) in a clone: run `./setup.sh` there, and it uses the directories below.

The guide links to the detailed runbooks instead of repeating them. Unless noted otherwise, commands run from the checkout (`~/life-os` here). Values such as `lifeos.example.com`, `<you>`, and `<generate>` are placeholders.

## 1. What you get

Everything runs as one Docker Compose project, `lifeos`, on one host. PostgreSQL is a single server in which every service has its own database and login. Go/Fiber services own the domains (Finance, Tasks, Inbox, Calendar, Food, Voice, Activity, Notifications). Integration services connect outside providers to them, only through their APIs: `banking`, `google-calendar`, `calendar-feeds`, and `recipe-import`. A Next.js Dashboard is the UI, and an nginx `gateway` is the only container that publishes a port. The `processor` plans each Inbox capture with the Codex CLI under your ChatGPT sign-in and acts through the `mcp` tool service. The `voice-worker` transcribes recordings with a private whisper.cpp worker, `whisper`. The `monitor`, `backup`, and `host-health` services watch the stack, back it up, and report on the server. Optional Compose profiles add a Cloudflare Tunnel connector (`cloudflare`) and an ntfy server (`ntfy`). For more, see the [architecture overview](docs/architecture/overview.md), [networking](docs/architecture/networking.md), and the [technical documentation](docs/README.md).

## 2. Requirements

**Software**

- **OS and Docker.** You need a 64-bit Linux server with systemd and cgroup v2. The reference server runs Ubuntu 24.04 with Docker Engine 29.4 and the Compose plugin 5.1, using the systemd cgroup driver. The repository sets no minimum version. It needs Docker Engine with the `docker compose` plugin (Compose v2 or later, not the old `docker-compose`). The Compose files use recent features: non-recursive read-only binds in `compose.yaml`, and `!reset` and `!override` in `compose.verify.yaml`. Install a current release.
- **Host tools.** `git`, `openssl`, `curl`, and `jq`. `tools/verify-compose` and the backup runbook need `jq`.
- **Codex.** The Codex CLI on the host, and a ChatGPT account to sign it in ([step 5](#5-sign-in-to-codex-ai-processing)). Or, instead, Claude Code with a Claude Pro or Max plan (and the Claude Code CLI on any machine to create its token) or an Anthropic API key ([Claude Code instead of Codex](#claude-code-instead-of-codex)).
- **Go and Node.js, only outside Docker.** The images build everything they need. You need Go 1.27.4 or newer (1.27 line) only to generate the web push keys ([step 10](#10-notifications)) and to run `make check`. You need Node.js 22 or newer with npm, `sqlc`, `oapi-codegen`, and `migrate` only for development and `make check`; see [Getting started](docs/development/getting-started.md).
- **Outbound HTTPS.** The `backend` network needs internet access to reach Codex, the Whisper model download, the push services, recipe pages, feeds, and the integrations.
- **Free address ranges.** `compose.yaml` gives its edge networks fixed subnets in `10.255.249.0/24`, and the verification stack uses `10.255.250.0/24`. Keep both ranges free.

**Hardware.** There is no tested minimum. The reference server has 6 CPUs and 7.7 GiB of RAM with no swap, and shares them with other applications. Life OS runs well there as long as heavy jobs run one at a time.

| Resource | What to expect |
| --- | --- |
| RAM at idle | Under 1 GiB for the whole stack. Measured on the reference server: PostgreSQL about 260 MiB, the processor about 230 MiB, the Dashboard about 100 MiB, each Go service 5–25 MiB. |
| RAM at peak | These add to idle use: Whisper while it transcribes (up to its cap, `LIFE_OS_WHISPER_MEMORY`, default `2g`), Codex runs in the processor, image builds (the Dashboard's Next.js build and the whisper.cpp compile are the largest), and the verification stack, which is a second complete Life OS. With no swap, overlapping heavy jobs can exhaust memory. |
| CPU | Whisper is limited to `LIFE_OS_WHISPER_CPUS` (2.0) and `LIFE_OS_WHISPER_THREADS` (2). Transcription is slower with fewer cores. |
| Disk | About 3 GB of images: Dashboard ~1 GB, processor ~0.8 GB, backup ~0.3 GB, whisper ~0.2 GB, each Go service 20–35 MB. Add Docker's build cache, the Whisper model (`small` is about 466 MB), PostgreSQL's data, and the backups, which are on the same disk. Status warns when a disk has less than 10 GiB or 10% free. The Backups check warns when less than 5 GB is free where the backups are. |

**For access from outside the server**, you need one of: a WireGuard network, a host Nginx with TLS, or a domain on Cloudflare ([step 9](#9-remote-access)).

## 3. Get the code

```sh
git clone <repository-url> ~/life-os
cd ~/life-os
cp .env.example .env
chmod 600 .env
```

> **Deploy from one checkout only.** `compose.yaml` fixes the project name (`name: lifeos`). A `docker compose` command in *any* checkout of this repository acts on the same live stack, including a second clone or a git worktree. Containers it recreates then take their bind mounts (`./infra/…`, the migrations) from that checkout. Run tests with `tools/verify-compose`, which uses the separate `lifeos-verify` project.

## 4. Configure `.env`

Compose reads `.env` from the checkout. `.env.example` groups the settings by area (General, Database, Backups, Cloudflare, AI, and so on), each under a header such as `# ---------- AI ----------`, and explains every setting in comments; the tables below group them by purpose. Generate the values with:

```sh
openssl rand -hex 24              # passwords: letters and digits only (they go into postgres:// URLs)
openssl rand -base64 32           # the Google and banking token keys
id -u                             # the *_RUNTIME_UID values: your numeric user ID
stat -c %g /var/run/docker.sock   # LIFE_OS_DOCKER_GID
```

Compose refuses every command until these five variables are set: `LIFE_OS_POSTGRES_USER`, `LIFE_OS_POSTGRES_PASSWORD`, `LIFE_OS_PUBLIC_URL`, `LIFE_OS_CODEX_RUNTIME_UID`, and `LIFE_OS_BACKUP_DIR`. Set `LIFE_OS_CODEX_HOME_HOST_PATH` too unless you use Claude Code (below). The template's hostnames (`lifeos.example.com`, `ntfy.example.com`) are placeholders: replace them with yours.

**Core**

| Variable | Value |
| --- | --- |
| `LIFE_OS_POSTGRES_USER` | **Required.** The PostgreSQL admin login, for example `lifeos_admin`. |
| `LIFE_OS_POSTGRES_PASSWORD` | **Required.** `<generate>` |
| `LIFE_OS_POSTGRES_IMAGE`, `LIFE_OS_POSTGRES_DB` | Keep the defaults: leave `LIFE_OS_POSTGRES_IMAGE` unset (`compose.yaml` pins the image by digest). PostgreSQL publishes no port. |
| `LIFE_OS_TIMEZONE` | Your IANA zone, for example `Europe/Berlin` (default `UTC`). Used when the browser's own zone is not known. See [Owner timezone](docs/operations/deployment.md#owner-timezone). |
| `LIFE_OS_FINANCE_CURRENCY` | Currency of the built-in Cash account (default `EUR`). |
| `LIFE_OS_PUBLIC_URL` | **Required.** The URL Life OS is reached at, without a trailing slash, such as `https://lifeos.example.com`. Phone notifications open their pages here, and the Google and bank redirect URIs default to it. Without a hostname, use the gateway's address, such as `http://127.0.0.1:3000`. |

**Database passwords, all required.** Generate one for each: `LIFE_OS_FINANCE_DB_PASSWORD`, `LIFE_OS_TASKS_DB_PASSWORD`, `LIFE_OS_INBOX_DB_PASSWORD`, `LIFE_OS_CALENDAR_DB_PASSWORD`, `LIFE_OS_VOICE_DB_PASSWORD`, `LIFE_OS_ACTIVITY_DB_PASSWORD`, `LIFE_OS_NOTIFICATIONS_DB_PASSWORD`, `LIFE_OS_BANKING_DB_PASSWORD`, `LIFE_OS_GOOGLE_CALENDAR_DB_PASSWORD`, `LIFE_OS_CALENDAR_FEEDS_DB_PASSWORD`, `LIFE_OS_FOOD_DB_PASSWORD`, and `LIFE_OS_BACKUP_DB_PASSWORD` (the backup service's read-only login). Provisioning refuses an empty password, even for integrations you don't use.

**Accounts** ([Accounts and signing in](docs/install/accounts.md)). All required except the last three.

| Variable | Value |
| --- | --- |
| `LIFE_OS_AUTH_DB_PASSWORD` | `<generate>`: the Dashboard's accounts database, `auth_db`. |
| `LIFE_OS_AUTH_SECRET` | `openssl rand -hex 32`. Signs the session cookies and encrypts two-factor secrets: keep it with your backups. |
| `LIFE_OS_IDENTITY_SIGNING_KEY`, `LIFE_OS_TRUSTED_KEYS` | An Ed25519 key pair as JWKs; `./setup.sh` writes them (also in a clone), `./setup.sh account-keys` adds them to an `.env` from before accounts, or make them as [Keys](docs/install/accounts.md#keys) says. |
| `LIFE_OS_LEGACY_ACCESS` | `off` (default) for a new server; `on` for one that ran before accounts, so that it keeps working without a sign-in until you turn it off. |
| `LIFE_OS_INSECURE_LOGIN_HOSTS` | Plain-HTTP hosts on which signing in is allowed anyway, such as a WireGuard address. Empty (default): sign in at the HTTPS address or `localhost`. |
| `LIFE_OS_REQUIRE_CLOUDFLARE_ACCESS` | Empty (default): the tunnel needs Cloudflare Access's token, as before; `off` makes the login the only gate there. |

**Gateway** ([step 9](#9-remote-access))

| Variable | Value |
| --- | --- |
| `LIFE_OS_GATEWAY_BIND_ADDRESS` | `127.0.0.1` (default), or your WireGuard address. The gateway also answers it as a Host. |
| `LIFE_OS_GATEWAY_PORT` | `3000` (default). |
| `LIFE_OS_PUBLIC_HOSTNAME` | The host of `LIFE_OS_PUBLIC_URL` when it is a name, such as `lifeos.example.com`: the gateway answers it too. Empty: none. |
| `LIFE_OS_ANDROID_RELEASES_DIR` | Default `~/life-os.android`, in the home of the user who runs Compose ([step 13](#13-the-android-app)). Set it only for another directory. |

**AI processing** ([step 5](#5-sign-in-to-codex-ai-processing))

| Variable | Value |
| --- | --- |
| `LIFE_OS_AI_PROVIDER` | `codex` (default), or `claude` for Claude Code on your Claude plan or Anthropic API key. |
| `LIFE_OS_AI_PROVIDERS` | The CLIs the processor image installs: `codex` (default, as in the published image), or `codex,claude` to build Claude Code in ([Claude Code instead of Codex](#claude-code-instead-of-codex)). |
| `LIFE_OS_CODEX_HOME_HOST_PATH` | **Required with Codex.** `/home/<you>/.codex`, an absolute path. Leave it out with Claude. Compose cannot require it for Codex alone: with Codex, leaving it unset makes the processor exit at startup. |
| `LIFE_OS_CODEX_RUNTIME_UID` | **Required.** The processor's UID, `id -u`; with Codex, of that directory's owner. |
| `LIFE_OS_CODEX_MODEL`, `LIFE_OS_CODEX_REASONING_EFFORT` | `gpt-5.6-luna` and `medium`. |
| `LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN`, `LIFE_OS_ANTHROPIC_API_KEY` | **One of them, not both, with Claude.** The token from `claude setup-token` (your Claude plan), or an Anthropic API key (billed per use by Anthropic). |
| `LIFE_OS_CLAUDE_MODEL`, `LIFE_OS_CLAUDE_EFFORT` | `sonnet` and empty (Claude Code's default). |
| `LIFE_OS_INBOX_CONTEXT_MINUTES`, `LIFE_OS_IDEAS_TIME`, `LIFE_OS_IDEAS_WEB_SEARCH`, `LIFE_OS_AI_CATEGORIES`, `LIFE_OS_AI_BUDGET_PLANS`, `LIFE_OS_AI_BUDGET_PLAN_MONTHLY`, `LIFE_OS_PROCESSOR_POLL_MILLIS` | The AI jobs and their schedules. The defaults are fine. |

**Voice.** `LIFE_OS_WHISPER_*` and `LIFE_OS_VOICE_*`. Choose the model in [step 6](#6-voice-whisper-model); keep the other defaults.

**Notifications and reminders** ([step 10](#10-notifications)). These are `LIFE_OS_NOTIFICATIONS_CHANNELS` (empty: notifications are only stored and shown in the Dashboard), `LIFE_OS_WEBPUSH_*`, and `LIFE_OS_NTFY_*` (with `LIFE_OS_NTFY_PUBLIC_URL`, ntfy's own public URL). Reminder times: `LIFE_OS_TASKS_SUMMARY_TIME`, `LIFE_OS_TASKS_MORNING_TIME`, `LIFE_OS_TASKS_EVENING_TIME`, `LIFE_OS_TASKS_TIMED_LEAD_MINUTES`, and `LIFE_OS_FOOD_EXPIRY_TIME`. The monitor: `LIFE_OS_MONITOR_INTERVAL_SECONDS` and `LIFE_OS_MONITOR_CONFIRMATIONS`.

**Integrations** ([step 11](#11-optional-integrations)). Optional. Each one reports "not configured" until its settings are set. `LIFE_OS_GOOGLE_REDIRECT_URI` and `LIFE_OS_BANKING_REDIRECT_URI` default to `LIFE_OS_PUBLIC_URL` plus the Dashboard's callback route.

**Backups and server health**

| Variable | Value |
| --- | --- |
| `LIFE_OS_BACKUP_DIR` | **Required.** `/home/<you>/life-os.backups`, created below. |
| `LIFE_OS_BACKUP_RUNTIME_UID` | `id -u` of the directory's owner. |
| `LIFE_OS_BACKUP_TIME`, `LIFE_OS_BACKUP_KEEP_*`, `LIFE_OS_BACKUP_RESTORE_CHECK_DAY` | Defaults: 03:15; keep 7 daily, 4 weekly, 12 monthly, and failed runs for 3 days; restore check on `sunday`. |
| `LIFE_OS_INBOX_MEDIA_GID` | `101`, the inbox image's group. |
| `LIFE_OS_DOCKER_GID` | The Docker socket's group (see above). Without the right value, Status cannot read the containers. |
| `LIFE_OS_DOCKER_ROOT`, `LIFE_OS_HOST_HEALTH_*` | Set the Docker root only if Docker's data is not in `/var/lib/docker`. `LIFE_OS_HOST_HEALTH_*` are the Status page's warning thresholds. |

**Modules.** `COMPOSE_PROFILES` lists the optional [modules](docs/install/modules.md) that run, next to the core: `ai`, `voice`, `google-calendar`, `banking`, `server-health`, and later `cloudflare` and `ntfy`. Empty runs the core alone. The reference server runs `ai,voice,google-calendar,banking,server-health`, plus the tunnel from step 9. Keep `LIFE_OS_MODULES_LISTED=yes`. `./setup.sh modules on|off <id>` edits the list and prints the Compose commands; see [Deployment → Modules](docs/operations/deployment.md#modules).

**Remote access.** Leave `cloudflare` and `ntfy` out of `COMPOSE_PROFILES`, and `LIFE_OS_CLOUDFLARE_TUNNEL_TOKEN`, `LIFE_OS_CF_ACCESS_TEAM_DOMAIN`, `LIFE_OS_CF_ACCESS_CLIENT_ID`, and `LIFE_OS_CF_ACCESS_CLIENT_SECRET` empty for now ([step 9](#9-remote-access)).

Create the two host directories:

```sh
install -d -m 700 ~/life-os.backups    # LIFE_OS_BACKUP_DIR, owned by LIFE_OS_BACKUP_RUNTIME_UID
install -d -m 755 ~/life-os.android    # LIFE_OS_ANDROID_RELEASES_DIR
```

Keep a copy of `.env` in a password manager. Backups do not include it. After first use, a lost Google or banking token key means connecting that account again.

## 5. Sign in to Codex (AI processing)

The `processor` is the only service that runs Codex. `mcp` is the tool server the processor calls, and it needs no credentials. No API key is used: the processor uses the ChatGPT sign-in of the Codex CLI on the host. Its runs count against that account's Codex usage; the Status page shows the usage and the reset times.

1. As the user whose UID is in `LIFE_OS_CODEX_RUNTIME_UID`, install the Codex CLI on the host. The processor image installs it with `npm install --global @openai/codex@0.160.0` (see `services/mcp/Dockerfile`). Any install that can sign in works.
2. Sign in with `codex login`. This writes `auth.json` into `~/.codex`.
3. Set `LIFE_OS_CODEX_HOME_HOST_PATH` to that directory's absolute path, and `LIFE_OS_CODEX_RUNTIME_UID` to `id -u`.

When it starts, the processor mounts that directory read-only, runs as that UID, and copies only `auth.json` and an optional `config.toml` into a tmpfs. Without `auth.json` it exits with code 78. That includes leaving `LIFE_OS_CODEX_HOME_HOST_PATH` unset: Compose cannot make it required for Codex alone, so it starts the processor with an empty placeholder directory instead, and with Codex, leaving this unset makes the processor exit at startup. Under `restart: unless-stopped` it then keeps restarting, and the Status page's Server check reports the restart loop, until you set the path and recreate the processor. Never copy `auth.json` or an API key into the repository or `.env`. After the first start ([step 8](#8-build-and-start)), check the sign-in from inside the container with `docker compose exec processor codex login status`. If Status later says Codex is signed out, run `codex login` on the host again, then `docker compose up -d --no-build --no-deps --force-recreate processor`.

The routine model and effort are `LIFE_OS_CODEX_MODEL=gpt-5.6-luna` and `LIFE_OS_CODEX_REASONING_EFFORT=medium`, chosen by the [AI runtime benchmark](docs/verification/ai-runtime-benchmark.md). An empty effort keeps the Codex configuration's default. Timeout, concurrency, and the other runtime settings are in [Codex runtime settings](docs/operations/deployment.md#codex-runtime-settings) and [MCP and AI](docs/services/mcp.md#codex-runtime).

### Claude Code instead of Codex

With `LIFE_OS_AI_PROVIDER=claude`, every AI job runs through the Claude Code CLI, signed in one of two ways, never both:

- **Your Claude Pro or Max plan** (`LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN`): its runs count against the plan's usage limits, and nothing is billed to the Anthropic API. The processor never passes `ANTHROPIC_API_KEY`, or any other `ANTHROPIC_*` setting, to a run.
- **An Anthropic API key** (`LIFE_OS_ANTHROPIC_API_KEY`, `sk-ant-api…`): billed per use by Anthropic. The processor passes it to the Claude Code runs alone, as `ANTHROPIC_API_KEY`, and then not the plan's token.

Claude Code is Anthropic's software, under Anthropic's terms for your account; the published images never contain it. A source build installs it only when `LIFE_OS_AI_PROVIDERS` names it:

1. Either: on any machine with Claude Code, run `claude setup-token` and sign in with your plan; it prints a long-lived token. Or: create an API key in the Claude Console.
2. In `.env`, set `LIFE_OS_AI_PROVIDER=claude`, `LIFE_OS_AI_PROVIDERS=codex,claude`, and `LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN` to the token or `LIFE_OS_ANTHROPIC_API_KEY` to the key (`./setup.sh claude` does this, and asks for the model). Leave `LIFE_OS_CODEX_HOME_HOST_PATH` out; `LIFE_OS_CODEX_RUNTIME_UID` is still the processor's UID.
3. Optionally choose `LIFE_OS_CLAUDE_MODEL` (default `sonnet`, or `opus`, `haiku`, or a full model name) and `LIFE_OS_CLAUDE_EFFORT` (`low`, `medium`, `high`, `xhigh`, or `max`; empty is Claude Code's default).
4. Build the processor (`docker compose build processor`) and recreate it.

The build installs the official npm package `@anthropic-ai/claude-code` at the version and sha512 integrity `services/mcp/claude-code/package-lock.json` pins (2.1.319; `npm ci`), and only its native binary goes into the image; npm does not stay. That directory is the one place to bump it: the release's `./setup.sh claude` builds from the same files. Without a sign-in, or with both, the processor exits with code 78.

Claude Code adds about 330 MB (311 MiB, its one binary) to the unpacked image on linux/amd64, the platform of the release images, and about 480 MB on linux/arm64 (about 150 MB compressed). A processor whose `LIFE_OS_AI_PROVIDER` CLI is not in the image exits with code 78 and says to rebuild. Check the sign-in with `docker compose exec processor claude auth status`; it should report `"authMethod": "oauth_token"` (plan) or `"api_key"`. If Status says the AI is signed out, create a new token or key, put it in `.env`, and recreate the processor. To switch providers, change `LIFE_OS_AI_PROVIDER` and run `docker compose up -d --no-build --no-deps --force-recreate processor`.

## 6. Voice: Whisper model

On its first start, `whisper` downloads its model once into the `lifeos-whisper-models` volume. It reports ready only once the model is there. The template and Compose use the multilingual `small` model (`ggml-small.bin`, about 466 MB), which handles Russian, German, and English well on a CPU. To use another model, set both lines together:

```sh
LIFE_OS_WHISPER_MODEL_PATH=/models/ggml-small.bin
LIFE_OS_WHISPER_MODEL_URL=https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin
```

Whisper guesses the language of each note (`LIFE_OS_WHISPER_LANGUAGE=auto`, the default). If you speak one language only, set its whisper.cpp code, such as `de`, `ru`, or `en`: short notes come out more accurately. Whisper refuses a code whisper.cpp does not know at its start ([Voice and STT](docs/services/voice-stt.md#whisper-worker)).

At idle the worker uses little memory. While it transcribes, it can grow up to `LIFE_OS_WHISPER_MEMORY` (default `2g`), its hard cap; plan RAM around that cap. `LIFE_OS_WHISPER_CPUS` and `LIFE_OS_WHISPER_THREADS` limit its CPU. The docs name `large-v3-turbo` as the next step up; it needs more time and memory, but no limits are documented for it. Recordings can be up to 25 MiB. See [Voice and STT](docs/services/voice-stt.md#whisper-worker).

## 7. Databases

On a fresh install there is nothing to run by hand:

- **PostgreSQL.** `postgres` keeps its data in the `lifeos-postgres-data` volume and publishes no port.
- **Provisioning.** The registry `infra/postgres/service-databases.conf` lists each service's database, login, and password variable (`service|database|role|password variable`). On an empty volume, PostgreSQL's init runs `infra/postgres/provision-service-databases.sh`. It gives each service a non-privileged login that owns that service's database, revokes public access, and finally creates the read-only `lifeos_backup` login (`provision-backup-role.sh`).
- **Migrations.** Each service with a database has a migration job, `<service>-migrate` (golang-migrate). It applies `services/<service>/db/migrations` from the checkout and exits, and the service starts only after the job succeeds.

These commands are idempotent and safe on a running server:

```sh
make db-provision          # reconcile every database and login in the registry
make db-provision-backup   # the backup login alone
make db-check-isolation    # check that each service can reach only its own database
```

PostgreSQL keeps the environment it was created with. If you add a database password to `.env` later, recreate PostgreSQL or pass the password in; see [Adding the Food service](docs/operations/deployment.md#adding-the-food-service) and [infra/README.md](infra/README.md). Never run `make infra-reset` on a deployment: it deletes the volumes, and refuses without `CONFIRM=infra-reset`.

## 8. Build and start

1. **Build the images.** On a server with memory to spare, `make life-up` builds every image, every module's, and starts the core and the modules in `COMPOSE_PROFILES` in one step (`docker compose build --pull` with every module's profile, then `docker compose up --detach`). It is for the first start only: once Life OS is deployed, it refuses (see [Redeploying](#redeploying-always---no-deps)). On a small server, build one image at a time first, as the repository's own smokes do:

   ```sh
   for service in activity finance tasks food inbox calendar calendar-feeds recipe-import \
     notifications banking google-calendar monitor backup host-health host-health-docker \
     voice voice-worker whisper mcp processor dashboard; do
     docker compose build --pull "$service" || break
   done
   ```

2. **With the `voice` module, start Whisper first.** The Voice worker waits for Whisper to be healthy, so starting Whisper on its own keeps the model download from holding up the rest:

   ```sh
   docker compose up -d --no-build --no-deps whisper
   docker compose ps whisper          # wait until it shows (healthy)
   ```

3. **Start everything else**: the core and the modules `COMPOSE_PROFILES` lists. Keep `cloudflare` out of it until [step 9](#9-remote-access): the tunnel must not start before Cloudflare Access protects its hostname.

   ```sh
   docker compose up -d --no-build
   docker compose ps
   ```

   Compose starts PostgreSQL, which provisions the databases on a fresh volume. It then runs every `*-migrate` job (each ends `Exited (0)`) and starts the services, the Dashboard, and the gateway. About a minute later, the backup service runs its first, catch-up backup.

4. **Check health.**

   ```sh
   curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:3000/login   # 200
   docker compose ps                                                        # auth-migrate Exited (0), dashboard healthy
   docker compose exec processor codex login status
   ```

   These use the default bind address and port; substitute yours if you changed them. The services' routes (`/api/<domain>/…`) need a sign-in now; their `/ready` answers inside the Docker network.

5. **Create your account.** Print a one-time setup code, open `http://127.0.0.1:3000/setup` (or your HTTPS address), and enter it with a username and password; then turn on two-factor sign-in in Settings → Security:

   ```sh
   docker compose exec -T dashboard node scripts/lifeos-auth.mjs setup-code
   ```

   Then open the Dashboard's **Status** page (`/status`). It covers every service, automatic processing and Codex, Voice and Whisper, Backups, the server, and notifications, and suggests an action for each problem. Start any troubleshooting there; see [Operations and troubleshooting](docs/operations/deployment.md).

### Redeploying: always `--no-deps`

After the first start, change one service at a time:

```sh
docker compose build --pull <service>
docker compose up -d --no-build --no-deps <service>
```

Without `--no-deps`, `up` also recreates every dependency of the named service whose configuration changed. Every service with a database depends on PostgreSQL through its migration job, and PostgreSQL gets the admin login and every database password from `.env`. So after a database password changes, redeploying one service without `--no-deps` would also recreate PostgreSQL, restarting the database under every service, along with any other dependency whose settings changed. `--no-build` keeps each build a separate, deliberate step, one image at a time. Recreate PostgreSQL only on purpose, with `docker compose up -d --no-build --no-deps postgres`, when it must see a new database password.

The gateway needs nothing after a service redeploy, because it resolves service names on each request. It does ask the Dashboard's sign-in check for every service route, so while the Dashboard is being recreated the API answers `503 auth_unavailable` for a few seconds. Recreate it only when `infra/nginx/life-os.conf.template` or its `.env` settings change: `docker compose up -d --no-build --no-deps --force-recreate gateway`. See [Redeploying services and the gateway](docs/operations/deployment.md#redeploying-services-and-the-gateway).

## 9. Remote access

The gateway listens on `LIFE_OS_GATEWAY_BIND_ADDRESS:LIFE_OS_GATEWAY_PORT` (default `127.0.0.1:3000`). It serves Life OS only to four Host names: `127.0.0.1`, `localhost`, `LIFE_OS_GATEWAY_BIND_ADDRESS`, and `LIFE_OS_PUBLIC_HOSTNAME`. Any other Host gets no answer (444). At every start, the gateway renders them into `server_name` from `infra/nginx/life-os.conf.template`. After you change either setting, recreate it: `docker compose up -d --no-build --no-deps --force-recreate gateway`. `tools/check-gateway-hosts`, part of `tools/check`, checks the list. See [Gateway hosts](docs/architecture/networking.md#gateway-hosts).

Life OS has no login of its own, so choose one access model deliberately:

| Model | Setup | Who gets in |
| --- | --- | --- |
| Loopback behind a host Nginx | Keep `127.0.0.1`; terminate TLS and Basic Auth in the host Nginx ([example](docs/host-nginx-example.conf)). | Whoever passes your host Nginx. |
| WireGuard | Set `LIFE_OS_GATEWAY_BIND_ADDRESS` to the server's WireGuard address. Never use a public or LAN address. | Every allowed peer. |
| Cloudflare Tunnel and Access (the reference deployment) | Below. | The email addresses you allow. |

Installing the Dashboard on a phone, and sharing to Life OS, need a service worker. Browsers allow service workers only over https or on localhost, so they don't work over a plain-http WireGuard address.

**Cloudflare Tunnel and Access.** In outline (the details are in [Cloudflare Tunnel and Access](docs/operations/cloudflare.md)):

1. Add your domain to Cloudflare and create a Zero Trust team.
2. **Set up Access first.** Create a self-hosted Access application for `lifeos.example.com` with a 24-hour session. Use the One-time PIN login, create a group holding your email address, and add an **Allow** policy that includes that group. Access then denies everyone else.
3. Create a remotely managed tunnel and copy its token. Add a published application route: `lifeos.example.com` → HTTP → `gateway:80`. On that route, turn on **Enforce Access JSON Web Token (JWT) validation** for the application.
4. In `.env`, set `LIFE_OS_PUBLIC_HOSTNAME=lifeos.example.com` and `LIFE_OS_PUBLIC_URL=https://lifeos.example.com`, then recreate the gateway. Make sure an anonymous request is redirected to the Access login: `curl -s -o /dev/null -w '%{http_code}\n' https://lifeos.example.com/` must print `302`.
5. Only then start the connector. It reaches the gateway over the internal `edge` network, so the gateway can stay on loopback. In `.env`, add `cloudflare` to `COMPOSE_PROFILES` and set `LIFE_OS_CLOUDFLARE_TUNNEL_TOKEN=<from the tunnel>`. Then run `docker compose up -d --no-build --no-deps cloudflared`, and recreate the Dashboard and the gateway (`docker compose up -d --no-build --no-deps dashboard gateway`), which read the module list. Recreate the notifications service too, so notification links use the new `LIFE_OS_PUBLIC_URL`: `docker compose up -d --no-build --no-deps notifications`.
6. Verify with `tools/check-ingress` (only the gateway publishes a port, and each network holds only its intended members) and `docker compose ps cloudflared` (healthy). `tools/check-cloudflare-access` checks that anonymous requests to `LIFE_OS_PUBLIC_HOSTNAME` are refused; set `LIFE_OS_CF_ACCESS_TEAM_DOMAIN` to your team's `<team>.cloudflareaccess.com` first. Its second half needs an Access service token: add a Service Auth policy for the token, and put the token in `LIFE_OS_CF_ACCESS_CLIENT_ID` and `LIFE_OS_CF_ACCESS_CLIENT_SECRET`.

The repository uses a service token only for that check. Browsers and the installed phone app get through Access with the email code and its 24-hour session. How the Android and Wear OS apps authenticate is not documented in this repository. The tunnel token is a credential: never commit it or paste it anywhere.

## 10. Notifications

With `LIFE_OS_NOTIFICATIONS_CHANNELS` empty, notifications are only stored and listed in the Dashboard.

**Web push to the Life OS Android app** (the reference deployment's only channel). The Dashboard has no browser notifications. To set it up:

1. Generate the VAPID key pair (needs Go on the host). It prints two lines; paste both into `.env`:

   ```sh
   cd services/notifications && GOWORK=off go run ./cmd/vapid-keys
   ```

2. Optionally, set `LIFE_OS_WEBPUSH_SUBJECT=mailto:you@example.com` (default: `LIFE_OS_PUBLIC_URL`). Keep the key pair: a new pair invalidates every device's subscription. The private key is a secret.
3. Apply with `docker compose up -d --no-build --no-deps notifications`, then turn on notifications in the Android app.
4. Add `webpush` to `LIFE_OS_NOTIFICATIONS_CHANNELS` and run the same command again. To send a test push: `curl -X POST http://127.0.0.1:3000/api/notifications/v1/push/test -H 'X-Life-OS-Actor: dev:curl'`.

Pushes travel through Google's FCM. Some networks, such as some corporate Wi-Fi, block it, and nothing in Life OS can work around that. See [Notifications → Web push](docs/services/notifications.md#web-push).

**ntfy** is an optional, self-hosted alternative (profile `ntfy`, channel `ntfy`). It is [paused](docs/operations/ntfy.md#paused-since-2026-10-12) on the reference deployment. It needs a hostname and tunnel route of its own, *without* Access, plus a publisher token and two bcrypt password hashes. Set `LIFE_OS_NTFY_PUBLIC_URL` to its public URL, such as `https://ntfy.example.com`. Setup is in [ntfy](docs/operations/ntfy.md).

## 11. Optional integrations

Each integration runs either way and stays "not configured" until its settings are in `.env`. Apply settings with `docker compose up -d --no-build --no-deps <service>`.

**Google Calendar** (`google-calendar`) syncs your Google calendars with Calendar in both directions. You need:

- A Google Cloud project with the Google Calendar API enabled.
- An OAuth consent screen published **in production**. In Testing, Google expires the refresh token after 7 days.
- A **Web application** OAuth client with the redirect URI `LIFE_OS_PUBLIC_URL` plus `/calendar/google/callback`, such as `https://lifeos.example.com/calendar/google/callback`.

Then set `LIFE_OS_GOOGLE_CLIENT_ID`, `LIFE_OS_GOOGLE_CLIENT_SECRET`, and `LIFE_OS_GOOGLE_TOKEN_KEY` (`openssl rand -base64 32`; keep it). Set `LIFE_OS_GOOGLE_REDIRECT_URI` only if you registered another redirect URI. Connect under **Calendar → Google Calendar → Connect Google**. See [Google Calendar setup](docs/operations/google-calendar.md).

**ICS feeds** (`calendar-feeds`) are refreshed daily at `LIFE_OS_CALENDAR_SOURCE_REFRESH_AT` (04:30). Feeds on private or loopback addresses are refused.

- **ZAW waste calendar.** `LIFE_OS_ZAW_ICS_URL` takes the MyMüll iCal export URL for one street, with the format shown in `.env.example`. calendar-feeds then keeps a "ZAW" feed for it. The URL names your street, so keep it only in `.env`. See [ZAW waste collection](docs/services/calendar-feeds.md#zaw-waste-collection).
- **Any other feed.** The Dashboard has no form for adding one; subscribe through the API:

  ```sh
  curl -H 'X-Life-OS-Actor: dev:curl' -H 'content-type: application/json' \
    -d '{"name":"Holidays","url":"https://example.com/holidays.ics"}' \
    http://127.0.0.1:3000/api/integrations/calendar-feeds/v1/feeds
  ```

**Enable Banking** (`banking`) gives read-only access to bank accounts and syncs them into Finance. You need:

- An Enable Banking application in production, with account information (AIS) only, in "restricted" mode for your own linked accounts.
- The redirect URL `LIFE_OS_PUBLIC_URL` plus `/finance/bank/callback`, such as `https://lifeos.example.com/finance/bank/callback`. Set `LIFE_OS_BANKING_REDIRECT_URI` only if you registered another one.
- The application's private key (PEM) on the server, mode 0600, owned by `LIFE_OS_BANKING_RUNTIME_UID`.

Set `LIFE_OS_ENABLE_BANKING_APP_ID`, `LIFE_OS_ENABLE_BANKING_KEY_FILE`, `LIFE_OS_BANKING_TOKEN_KEY` (`openssl rand -base64 32`; keep it), and `LIFE_OS_BANKING_RUNTIME_UID`, then connect from **Finance**. The service is built and tested for one bank, Deutsche Bank in Germany (its default institution). See [Bank connection](docs/operations/banking.md).

## 12. Backups

The `backup` service dumps every database in the registry each night at `LIFE_OS_BACKUP_TIME` (03:15, in `LIFE_OS_TIMEZONE`). Each database gets a `pg_dump` from one consistent snapshot, saved in `LIFE_OS_BACKUP_DIR/auto/<UTC start time>/`. Inbox capture photos are copied to `media/`. Retention keeps the newest successful backup of each of the last 7 days, 4 weeks, and 12 months, and keeps failed runs for 3 days. After the Sunday run (`LIFE_OS_BACKUP_RESTORE_CHECK_DAY`), a restore check restores the newest backup into scratch databases and compares it with its manifest. The Status page's Backups check reports all of this and offers **Back up now**.

```sh
tools/backup-now                                       # back up now and wait for the result
docker compose exec -T backup /service restore-check   # run a restore check now
tools/restore-database finance latest                  # show a restore plan; --confirm restores
tools/restore-media latest                             # capture photos; --confirm restores
```

**Not backed up:** `.env`, the Codex session, the Enable Banking key, Voice audio, the Whisper models, and ntfy's data. Keep the secrets yourself.

The backups sit on the same disk as PostgreSQL. They protect against mistakes and bad migrations, not against losing the server. Off-site copies (encrypted, to Cloudflare R2) are planned but not built yet, so copy `LIFE_OS_BACKUP_DIR` off the server yourself in the meantime. For restoring one database, or everything after losing PostgreSQL's volume, see the [backups runbook](docs/operations/backups.md) and the [backup service](docs/services/backup.md).

## 13. The Android app

The phone and Wear OS apps are built and signed in a separate repository (`life-os-app`). This server only stores and serves them, from `LIFE_OS_ANDROID_RELEASES_DIR`, which the gateway mounts read-only:

- `/android/` is the download page for the first install.
- `/android/updates` is the update manifest the app checks.
- The APKs are served next to them.

Everything is behind the same access boundary as the Dashboard. The directory created in [step 4](#4-configure-env) is all the server needs. If the gateway started before that directory existed, Docker created it owned by root. In that case, give it back to your user (`sudo chown "$(id -u):$(id -g)" ~/life-os.android`) and recreate the gateway with `docker compose up -d --no-build --no-deps --force-recreate gateway`. Then check it and publish a build:

```sh
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:3000/android/updates   # 404 until the first publish, then 200
tools/publish-android-release <release-dir> --notes "What changed"
```

The same command also publishes the build as a GitHub Release of the app repository with [gh](https://cli.github.com/), which is where apps that are not connected to your server update from. It needs `gh auth login` as an account that may push there; `--no-github` skips it, `LIFE_OS_ANDROID_GITHUB_REPO` names another repository (or `off`), and `--dry-run` shows what it would do. The manifest format, version rules, GitHub Releases, and troubleshooting are in [Android releases](docs/operations/android-releases.md).

## 14. Verifying an install

- **Live stack.** Check the Status page, `docker compose ps`, the `/api/<domain>/ready` probes, and `docker compose exec processor codex login status`. These only read.
- **Disposable verification stack.** Run every smoke there. `tools/verify-compose` runs a second, isolated copy (`lifeos-verify-*` containers, networks, and volumes; gateway on `127.0.0.1:13001`) from the images already built, then deletes it. It never mounts your backup or Android directories, blanks your integration and notification credentials, and never starts the tunnel or ntfy. Its processor does use your Codex sign-in. It needs memory for a second Life OS, so don't run it alongside other heavy jobs.
  - `make life-smoke` starts the gateway and the services behind it on that stack, from the images already built, and runs `tools/smoke-core-services` and `tools/smoke-dashboard`. They write a few test records (a Task, a capture, an event) there, never into your data. `make core-smoke` runs the first only.
  - The two scripts refuse to run on their own until `LIFE_OS_SMOKE_GATEWAY` names a gateway. Point it at the live one (`LIFE_OS_SMOKE_GATEWAY=http://127.0.0.1:3000 tools/smoke-core-services`) only to write those records into your data on purpose. They are marked as a developer tool, and the processor plans the capture with Codex.
  - Without Codex runs: `make life-smoke`, `make verify-clean-db`, `make e2e-smoke`, `make verify-isolation`, `make action-idempotency-smoke`, `make voice-lease-recovery-smoke`, and `tools/smoke-gateway-recreate`.
  - With the real Codex CLI and your sign-in, using your Codex usage: `make processor-smoke`, `make processor-codex-actions-smoke`, `make human-review-smoke`, `make activity-trace-smoke`, `make runtime-status-smoke`, `make retry-schedule-smoke`, `make codex-runtime-smoke`, `make relative-dates-smoke`, `make cross-language-smoke`, `make calendar-ai-smoke`, `make phase8-acceptance`, and `make browser-e2e`.
  - Every run that uses Voice downloads the Whisper model again into its scratch volume.
- **Source checks.** `tools/check` (`make check`) verifies the code, not the running stack. It needs a clean working tree plus `go`, `sqlc`, `oapi-codegen`, and `npm`. It checks the gateway's host list and the contracts, then runs sqlc drift, the release bundle's `deploy/compose.yaml` drift (Python 3 with PyYAML), gofmt, vet, tests, and builds for every Go module, and the Dashboard checks. It is CPU- and memory-heavy. Flags run only some parts (`tools/check --go`); GitHub Actions runs them on every push, with the integration tests and govulncheck (see [Continuous integration](docs/operations/ci.md)).

## 15. Upgrading

1. Note the current commit (`old=$(git rev-parse HEAD)`), then run `git pull`.
2. Look for new settings and databases with `git diff "$old" -- .env.example infra/postgres/service-databases.conf`. Add new variables to `.env`. For a new database, provision it as described in [Adding the Food service](docs/operations/deployment.md#adding-the-food-service). [Operations](docs/operations/deployment.md) has a section for each service added to an existing deployment.
3. Build each changed service, one at a time: `docker compose build --pull <service>`.
4. For a service with a database, apply its migrations first. The job exits when it is done: `docker compose up --no-build --no-deps <service>-migrate`.
5. Recreate the service: `docker compose up -d --no-build --no-deps <service>`. Deploy data services before `mcp` and `processor`, and the Dashboard after the services it reads. Recreate the gateway only if its configuration changed.
6. Check the Status page.

On a small server, run one heavy job at a time: don't build images while the verification stack, a restore check, or `tools/check` is running. A stack older than the calendar-feeds release must go through that release's [one-time handoff](docs/operations/calendar-feeds-handoff.md) first; a fresh install skips it.

## 16. Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| Compose refuses with "Set LIFE_OS_… in .env" | One of the six required variables ([step 4](#4-configure-env)) is missing. |
| The processor keeps restarting; its log says `missing Codex authentication at /run/lifeos-codex-source/auth.json` | `LIFE_OS_CODEX_HOME_HOST_PATH` is unset or wrong, or you haven't signed in yet. Run `codex login` on the host, then `docker compose up -d --no-build --no-deps --force-recreate processor`. |
| Status says Codex or Claude Code is signed out or usage-limited | Signed out: sign in again as above. Usage-limited: captures are retried after the reset time Status shows. |
| The processor keeps restarting; its log says `this image has no claude CLI` (or `no codex CLI`) | `LIFE_OS_AI_PROVIDERS` left that CLI out of the image (the default, `codex`, has no Claude Code). Add it, then `docker compose build processor` and recreate the processor. |
| The processor keeps restarting; its log says `missing Claude Code's sign-in` or `both ... are set` | `LIFE_OS_AI_PROVIDER=claude` needs exactly one of `LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN` (from `claude setup-token`) and `LIFE_OS_ANTHROPIC_API_KEY`. Fix `.env`, then recreate the processor as above. |
| `curl` gets an empty reply from the gateway | The Host is not one the gateway answers. Set `LIFE_OS_PUBLIC_HOSTNAME` or `LIFE_OS_GATEWAY_BIND_ADDRESS`, then recreate the gateway ([step 9](#9-remote-access)). |
| 502 from a page or API | The service behind it is not running. Check `docker compose ps`, `docker compose logs <service>`, and its `<service>-migrate` job. Don't publish service ports as a shortcut. |
| The gateway fails to start with a bind error | `LIFE_OS_GATEWAY_BIND_ADDRESS` or `LIFE_OS_GATEWAY_PORT` is unavailable on the host. |
| Voice recordings wait; Status says Whisper is not ready | The model is still downloading on the first start (`docker compose logs whisper`). |
| The Server card cannot read the containers | `LIFE_OS_DOCKER_GID` is wrong. Fix it, then `docker compose up -d --no-build --no-deps host-health-docker`. |
| Backups: `lifeos_backup cannot connect to <database>` | Run `make db-provision-backup`. If the capture images failed, check `LIFE_OS_INBOX_MEDIA_GID` (`docker compose exec inbox id -g`). |
| Phone pushes don't arrive on one network but do on mobile data | That network blocks Google's FCM. Life OS can't fix this. |
| A command run from a second clone or worktree changed the live stack | `name: lifeos` ([step 3](#3-get-the-code)). Deploy from one checkout; test with `tools/verify-compose`. |
| `make life-up`, `core-up`, or `infra-up` says Life OS is deployed here | They are for the first start. Redeploy one service at a time ([step 8](#redeploying-always---no-deps)). `CONFIRM=<target>` overrides the check. |
| A verification run's teardown exits 1 after its tests passed | A stale Docker network endpoint blocked the removal of a `lifeos-verify-*` network. The test result stands. Run `tools/verify-compose down --volumes --remove-orphans` again; it touches only `lifeos-verify-*` resources. |
| The server runs out of memory, or Status says memory is low | The Server card names the biggest users. Run one heavy job at a time (builds, the verification stack, restore checks, `tools/check`). Lower `LIFE_OS_WHISPER_MEMORY` and `LIFE_OS_WHISPER_THREADS`, or use a smaller Whisper model. |
| `tools/check` fails at `check-gateway-hosts` | It names the problem: a host setting that is not an exact name, a `LIFE_OS_PUBLIC_URL` whose host the gateway does not answer (set `LIFE_OS_PUBLIC_HOSTNAME`), or a template edit that drops a required entry. |
| Remote access fails (Cloudflare 1033/530, 502 after login, no PIN email) | See [Cloudflare → Diagnose](docs/operations/cloudflare.md#diagnose). |
