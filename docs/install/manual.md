# Manual install: Portainer, Dockge, Unraid

Life OS runs from two files: the release's `compose.yaml` and a `.env` with your settings, passwords, and keys. The [Quick start](README.md#quick-start) has `setup.sh` write the `.env` and run Docker Compose for you. This page is for when something else runs Compose: Portainer, Dockge, Unraid's Compose Manager, or `docker compose` itself. `setup.sh` still writes the `.env`, on any machine, without Docker; you do the rest.

> [!WARNING]
> **Early preview.** Life OS has a login ([Accounts](accounts.md)), but it is new. Keep it on `127.0.0.1` (the default) or a private address, and reach it the way [Access it safely](access.md#choose-a-way-in) describes. Never publish its port to the internet over plain HTTP.

## What you need

- **A server** as the [install guide](README.md#what-you-need) describes: 64-bit Linux (amd64 or arm64), Docker Engine 25 or newer with Compose 2.27 or newer, 2 GB of memory at least, and 10 GB of free disk.
- **A stack manager that runs Compose files on that Docker**: Portainer with a Docker Standalone environment (not Swarm, whose `docker stack deploy` has no profiles), Dockge, or Unraid with the Docker Compose Manager plugin. Or plain `docker compose`.
- **One machine with Bash 4.4 or newer and OpenSSL 1.1.1 or newer**, to make the `.env`: the server itself, any Linux computer, or WSL on Windows. It needs no Docker. A Mac's own Bash and OpenSSL are too old: install newer ones with `brew install bash openssl`, and run `setup.sh` with them: `PATH="$(brew --prefix openssl)/bin:$PATH" "$(brew --prefix)/bin/bash" ./setup.sh env` (not yet tested on a Mac).
- **One Life OS per machine, in a stack named `lifeos`.** Every install uses the same `lifeos-*` volumes and networks, whatever its stack is called, so a second stack would take the first one's data. The name `lifeos` also gives the containers the names this page uses, such as `lifeos-dashboard-1`.

There is no container for making the `.env`: none of Life OS's images has Bash, OpenSSL, and `setup.sh`, and an image only for this step would be one more to build, scan, and keep current. Any machine from the list above does it.

## 1. Download the release

On the machine where you make the `.env`, download and check the newest release's files, as in the [Quick start](README.md#quick-start), steps 1 and 2:

```sh
mkdir ~/life-os && cd ~/life-os
version=$(curl -fsSL https://api.github.com/repos/GAMch1k/life-os/releases | sed -n 's/^ *"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
echo "$version"
release=https://github.com/GAMch1k/life-os/releases/download/$version
for file in compose.yaml setup.sh SHA256SUMS; do curl -fsSLO "$release/$file"; done
# GitHub serves .env.example as default.env.example
curl -fsSL -o .env.example "$release/default.env.example"
sha256sum -c SHA256SUMS
```

You need `compose.yaml` again in step 4.

## 2. Make the .env

```sh
chmod +x setup.sh
./setup.sh env
```

It asks what the first setup asks (your timezone, your currency, and the address you will open Life OS at), and then about the server: the directory where Life OS keeps its backups and the AI sign-in, and the user ID that owns it. On the server itself, the defaults fit. It writes `.env`, readable only by you (mode 600), and does nothing else: no Docker, no checks, no directories, no containers. The passwords and keys come from the same code as the first setup's: a new password for each database login, the accounts' secret and signing key ([Keys](accounts.md#keys)), legacy access off, and the core alone, with no optional module on.

Without questions, with the options:

```sh
./setup.sh env --yes --timezone Europe/Berlin --currency EUR --dir /srv/life-os --uid 1000
```

| Option | What it sets | Default |
| --- | --- | --- |
| `--timezone ZONE` | Your timezone, such as `Europe/Berlin` | This machine's, else `UTC` |
| `--currency CODE` | The currency of Finance's Cash account | `EUR` |
| `--public-url URL`, `--bind ADDRESS`, `--port PORT` | The address you open Life OS at, and where it listens on the server ([Access it safely](access.md)) | `http://127.0.0.1:3000`, on `127.0.0.1:3000` |
| `--allow-http-login`, `--trusted-proxies LIST` | Signing in over plain HTTP at a LAN or VPN `--bind` address ([Signing in](accounts.md#signing-in)), and your reverse proxies ([Your own reverse proxy](access.md#your-own-reverse-proxy)) | Off, none |
| `--dir DIR` | The directory on the server: the backups go to `DIR/backups`, the AI sign-in to `DIR/codex-home` | This directory |
| `--uid UID` | The user on the server who owns that directory. The backup service, AI processing, and bank sync run as it. | Yours here (`id -u`) |
| `--docker-gid GID` | The group of the server's Docker socket, for the `server-health` module (`stat -c %g /var/run/docker.sock` on the server) | This machine's, else `999` |
| `--stdout` | Print the `.env` instead of writing it | |
| `--force-new` | Write a new `.env` over the one here. It keeps a copy of the old one, and its passwords and keys. | |

On Unraid, use `--dir /mnt/user/appdata/lifeos --uid 99` (Unraid's user for app data, `nobody`).

It never overwrites an existing `.env`: `cat .env` shows the one you have, and `--force-new` writes a new one. It never prints a password or key, except with `--stdout`: that prints the whole `.env`, for pasting into Portainer, behind a warning on the terminal (the warning is not part of the output). It holds every password and key, so prefer the file on a shared screen or in a terminal that keeps a log.

**Keep a copy of `.env`** in a password manager or another safe place. The backups don't include it, and without it a backup can't be opened by a new install.

## 3. Prepare the server

Create the backup directory, owned by the user from `--uid`, readable only by it. `./setup.sh env` prints this line with your values:

```sh
sudo install -d -m 700 -o 1000 /srv/life-os/backups
```

Without it, the start fails: Docker does not create a missing directory for the backups. That is all the core needs: every image is published, and `compose.yaml` needs no other file. The AI sign-in's directory comes with [AI processing](#ai-processing-codex).

## 4. Start it

### Portainer

1. **Stacks → Add stack**, in a Docker Standalone environment. Name: `lifeos`.
2. **Build method: Upload**, and choose the release's `compose.yaml`. (Or **Web editor**, and paste it.)
3. **Environment variables → Load variables from .env file**, and choose your `.env`. Or switch to **Advanced mode** and paste it (`./setup.sh env --stdout` prints it). Portainer drops the comments and keeps each value as it is, quotes included, and Compose reads them as it reads a `.env`.
4. **Deploy the stack.** The first start downloads the images and takes a few minutes.

To change a setting later: the stack → **Editor**, change the variable, **Update the stack**. Keep your own copy of `.env` in step.

### Dockge

1. **+ Compose**. Stack name: `lifeos`.
2. Paste the release's `compose.yaml` into the compose editor, and your `.env` into the `.env` editor.
3. **Deploy.**

Dockge keeps them in its stacks directory, such as `/opt/stacks/lifeos/compose.yaml` and `.env`: the layout `setup.sh` itself uses, so `setup.sh` can manage the same stack ([What still works with setup.sh](#what-still-works-with-setupsh)).

### Unraid (Compose Manager)

1. Install **Docker Compose Manager** from **Apps** (Community Applications).
2. Make the `.env` with `--dir /mnt/user/appdata/lifeos --uid 99`, and in Unraid's terminal create the backup directory: `install -d -m 700 -o 99 /mnt/user/appdata/lifeos/backups`.
3. On the **Docker** tab, under **Compose**: **Add New Stack**, named `lifeos`.
4. The stack's gear icon → **Edit Stack → Compose File**: paste the release's `compose.yaml`, and save. Then **Edit Stack → ENV File**: paste your `.env`, and save.
5. **Compose Up**, without choosing a profile: `COMPOSE_PROFILES` in the `.env` picks the modules.

Compose Manager keeps the stack, `.env` included, on the flash drive (`/boot/config/plugins/compose.manager/projects/lifeos`).

### Plain Docker Compose

Put `compose.yaml` and `.env` into one directory on the server, and start it there:

```sh
docker compose up -d
```

## 5. The first account

Once the containers run, open Life OS. It answers only on the server unless you chose another address; from your computer, open an SSH tunnel as in the [Quick start](README.md#quick-start), step 5, then <http://127.0.0.1:3000>.

The first account needs a one-time setup code. On the server:

```sh
docker exec lifeos-dashboard-1 node scripts/lifeos-auth.mjs setup-code
```

Or open a console in the container `lifeos-dashboard-1` from your stack manager (Portainer: **Containers → lifeos-dashboard-1 → Console → Connect**) and run `node scripts/lifeos-auth.mjs setup-code`. The code works once, for 24 hours. At the login, go to **Set up the first account**, enter it, and choose a username and password; then turn on two-factor sign-in in **Settings → Security** ([Accounts](accounts.md)).

Then look at the **Status** page (`/status`): it shows what works, what is still starting, and what needs you.

`postgres-provision` and the containers whose names end in `-migrate` show as **exited**. That is right: at every start they create the databases and bring them up to date, then end with code 0.

## Settings

### Required

`./setup.sh env` writes all of these. Without one of those marked Compose, Compose refuses to start and names it.

| Setting | What it is |
| --- | --- |
| `LIFE_OS_VERSION` (Compose) | The release to run. It must be the release of your `compose.yaml`, which names it on its second line (`# Life OS release: …`). |
| `LIFE_OS_PUBLIC_URL` (Compose) | The address you open Life OS at. |
| `LIFE_OS_POSTGRES_USER`, `LIFE_OS_POSTGRES_PASSWORD` (Compose) | PostgreSQL's admin login. |
| `LIFE_OS_*_DB_PASSWORD`, 13 of them | Each service's own database login. Letters and digits only: `openssl rand -hex 24`. Compose starts without them, but the services cannot connect. |
| `LIFE_OS_AUTH_SECRET`, `LIFE_OS_IDENTITY_SIGNING_KEY`, `LIFE_OS_TRUSTED_KEYS` (Compose) | The accounts' secret and keys ([Keys](accounts.md#keys)). |
| `LIFE_OS_BACKUP_DIR` (Compose), `LIFE_OS_BACKUP_RUNTIME_UID` | The backup directory on the server, and the user that owns it. |
| `LIFE_OS_CODEX_HOME_HOST_PATH`, `LIFE_OS_CODEX_RUNTIME_UID` (Compose) | The AI sign-in's directory, and the user AI processing runs as. Required even with AI processing off; the directory need not exist until it is on. |

`./setup.sh env` also writes the timezone, the currency, the gateway's address and port, `LIFE_OS_BANKING_RUNTIME_UID`, `LIFE_OS_DOCKER_GID`, `LIFE_OS_LEGACY_ACCESS=off`, and `COMPOSE_PROFILES` empty (the core alone). Every other setting in `.env.example` is optional, with the default it shows.

### Modules

Each optional [module](modules.md) is a Compose profile of its name. To turn one on, add its id to `COMPOSE_PROFILES` (comma-separated, such as `COMPOSE_PROFILES=voice,server-health`), add the settings it needs, and redeploy: Portainer's **Update the stack**, Dockge's **Deploy**, Unraid's **Compose Up**, or `docker compose up -d`. The Dashboard and the gateway restart with the new list.

To turn one off, take it out of `COMPOSE_PROFILES` and redeploy, **then remove its containers**: Compose leaves the containers of a profile that is off running (`./setup.sh up` removes them for you). Its data stays. For voice notes:

```sh
docker rm -f lifeos-voice-migrate-1 lifeos-voice-1 lifeos-voice-worker-1 lifeos-whisper-1
```

In Portainer: **Containers**, tick them, **Remove**.

| Module | Its containers (`lifeos-<name>-1`) | What it needs |
| --- | --- | --- |
| `ai` | `mcp`, `processor` | The Codex sign-in in `LIFE_OS_CODEX_HOME_HOST_PATH`: see [AI processing](#ai-processing-codex) below. |
| `voice` | `voice-migrate`, `voice`, `voice-worker`, `whisper` | Nothing more; the model and language are optional ([Voice notes](voice.md)). On amd64 the server's CPU needs AVX2: `grep -m 1 -o avx2 /proc/cpuinfo` there must print `avx2`. |
| `google-calendar` | `google-calendar-migrate`, `google-calendar` | `LIFE_OS_GOOGLE_CLIENT_ID`, `LIFE_OS_GOOGLE_CLIENT_SECRET`, and `LIFE_OS_GOOGLE_TOKEN_KEY` (`openssl rand -base64 32`, kept for good) ([Google Calendar](google-calendar.md)). |
| `banking` | `banking-migrate`, `banking` | `LIFE_OS_ENABLE_BANKING_APP_ID`, `LIFE_OS_ENABLE_BANKING_KEY_FILE` (the key file's path on the server, owned by `LIFE_OS_BANKING_RUNTIME_UID`, mode 600), and `LIFE_OS_BANKING_TOKEN_KEY` (`openssl rand -base64 32`, kept for good) ([Bank sync](bank-sync.md)). |
| `cloudflare` | `cloudflared` | `LIFE_OS_CLOUDFLARE_TUNNEL_TOKEN`, `LIFE_OS_PUBLIC_URL=https://<hostname>`, and `LIFE_OS_PUBLIC_HOSTNAME=<hostname>`. Cloudflare Access must protect the hostname first ([Cloudflare Tunnel and Access](access.md#cloudflare-tunnel-and-access)). |
| `ntfy`, with `cloudflare` | `ntfy` | Its address, a token, and two password hashes: see [ntfy](#ntfy) below. |
| `server-health` | `host-health`, `host-health-docker` | `LIFE_OS_DOCKER_GID`: `stat -c %g /var/run/docker.sock` on the server. `LIFE_OS_DOCKER_ROOT`, if Docker keeps its data elsewhere than `/var/lib/docker`. |

Push to the phone app is part of the core: it needs a key pair (`LIFE_OS_WEBPUSH_VAPID_PUBLIC_KEY`, `LIFE_OS_WEBPUSH_VAPID_PRIVATE_KEY`), `webpush` in `LIFE_OS_NOTIFICATIONS_CHANNELS`, and, without an `https://` address, `LIFE_OS_WEBPUSH_SUBJECT=mailto:<your address>`. `./setup.sh webpush` writes them, on any machine ([below](#what-still-works-with-setupsh)).

#### AI processing (Codex)

The Codex sign-in needs a shell on the server, once. With the values from your `.env`:

```sh
version=v0.1.0-preview.7             # LIFE_OS_VERSION
uid=1000                             # LIFE_OS_CODEX_RUNTIME_UID
dir=/srv/life-os/codex-home          # LIFE_OS_CODEX_HOME_HOST_PATH
sudo install -d -m 700 -o "$uid" "$dir"
docker run --rm -it --user "$uid" -e CODEX_HOME=/codex-login -v "$dir:/codex-login" \
  --entrypoint codex "ghcr.io/gamch1k/life-os-processor:$version" login --device-auth
```

That is what `./setup.sh codex` runs. Codex prints a link and a code: open the link on any device and sign in ([AI processing → Sign in](ai-processing.md#sign-in)). Then add `ai` to `COMPOSE_PROFILES` and redeploy.

Claude Code instead of Codex is not in the published images: `./setup.sh claude` adds it to a local copy of the processor image, on the server. Run it in a directory there with the release's files and a copy of your `.env`; then copy what it wrote (`LIFE_OS_AI_PROVIDER`, `LIFE_OS_PROCESSOR_IMAGE`, the sign-in, `LIFE_OS_CLAUDE_MODEL`, and `ai` in `COMPOSE_PROFILES`) into the stack. After each update, run it again for the new version.

#### ntfy

ntfy needs the `cloudflare` module, and a hostname of its own in the tunnel ([Notifications → ntfy](notifications.md#ntfy)). On the server, with the ntfy image your `compose.yaml` names:

```sh
image=$(grep -o 'binwiederhier/ntfy:[^}]*' compose.yaml | head -n 1)
echo "tk_$(openssl rand -hex 15 | cut -c 1-29)"     # LIFE_OS_NTFY_PUBLISHER_TOKEN
docker run --rm -it "$image" user hash              # a random password: LIFE_OS_NTFY_PUBLISHER_PASSWORD_HASH
docker run --rm -it "$image" user hash              # your phone's password (12 characters or more): LIFE_OS_NTFY_PHONE_PASSWORD_HASH
```

Put each hash in single quotes, as it contains `$`: `LIFE_OS_NTFY_PHONE_PASSWORD_HASH='$2a$10$…'`. Set `LIFE_OS_NTFY_PUBLIC_URL` to ntfy's `https://` address, add `ntfy` to `COMPOSE_PROFILES` (after `cloudflare`) and to `LIFE_OS_NOTIFICATIONS_CHANNELS`, and redeploy.

## What setup.sh does that you now do

### Around the start

`./setup.sh up` does more than `docker compose up -d`:

| `./setup.sh up` | By hand |
| --- | --- |
| Refuses `setup.sh`, `compose.yaml`, and `.env.example` of two releases together. | Check the files with `sha256sum -c SHA256SUMS`. Keep `LIFE_OS_VERSION` the release your `compose.yaml` names. |
| Checks the server: Docker and Compose versions, a 64-bit platform, memory, disk. | The [requirements](README.md#what-you-need). |
| Refuses to start while another directory's Life OS runs. | One stack, named `lifeos`. |
| Refuses a public gateway address. | Keep `LIFE_OS_GATEWAY_BIND_ADDRESS` on `127.0.0.1` or a private address ([Access it safely](access.md)). |
| Warns when voice notes are on without AVX2, or with a language Whisper does not know. | The AVX2 check above; `LIFE_OS_WHISPER_LANGUAGE` is `auto` or a code from [Voice notes → Language](voice.md#language). |
| Builds the local processor image with Claude Code when it is missing. | `./setup.sh claude` again ([above](#ai-processing-codex)). |
| Removes the containers of the modules that are off. | [Modules](#modules), above. |
| Downloads the missing images and starts everything. | Your stack manager. At every start, `postgres-provision` creates each database and login with the passwords in `.env`, and each `*-migrate` container brings its database up to date. Nothing to run yourself. |
| Waits until Life OS answers, then prints its address and the setup code. | The Status page, and the [setup code](#5-the-first-account). |

### Updating

`./setup.sh update` backs up first, adds the new release's settings to `.env`, downloads its images, and restarts ([Updating](updating.md)). By hand:

1. **Read the release notes** on the [releases page](https://github.com/GAMch1k/life-os/releases).
2. **Back up**: the Status page's **Back up now**, or `docker exec lifeos-backup-1 /service now`. Go on only once it succeeded.
3. **Download the new release's files** and check them, as in [step 1](#1-download-the-release).
4. **Add the new settings** to your copy of `.env`, and to the stack. This lists the settings the new `.env.example` has and your `.env` lacks:

   ```sh
   comm -13 <(grep -oE '^[A-Z][A-Z0-9_]*=' .env | sort) <(grep -oE '^[A-Z][A-Z0-9_]*=' .env.example | sort)
   ```

   Give each new `*_PASSWORD` a new value (`openssl rand -hex 24`); copy the others as `.env.example` has them. The release notes say what they are for.
5. **Set `LIFE_OS_VERSION`** to the new release, and **replace the stack's compose file** with the new `compose.yaml`. Always both together: new images with an old `compose.yaml` lack the services and settings they need.
6. With Claude Code, run `./setup.sh claude` for the new version ([above](#ai-processing-codex)).
7. **Redeploy with new images**: Portainer's **Update the stack** with **Re-pull image and redeploy** on; Dockge's **Update**; Unraid's **Update Stack**; or `docker compose pull && docker compose up -d --remove-orphans`. Then check the Status page.

## What still works with setup.sh

- **On any machine, with a copy of your `.env`** next to the release's files: `./setup.sh env`, `gateway`, `google`, `cloudflare`, and `webpush`. They change only `.env` and need no Docker; then copy the `.env` into the stack and redeploy. (`google` and `cloudflare` also turn their module on in `COMPOSE_PROFILES`. `cloudflare` checks that Cloudflare Access protects your hostname from the machine it runs on.)
- **With Dockge**, every command works on the server: Dockge keeps the stack in a directory of its own, with `compose.yaml` and `.env`, as `setup.sh` does. Put the same release's `setup.sh` and `.env.example` next to them in `/opt/stacks/lifeos`, and run `./setup.sh` there: `up`, `update`, `restore`, `modules`, `codex`, and the rest.
- **With Portainer or Unraid**, the stack's containers belong to the stack manager's own directory, so `setup.sh` takes them for another install. In a directory on the server with the release's files and a copy of your `.env`, the commands that only talk to the running Life OS work: `setup-code`, `recover-owner`, `legacy-access`, `sign-out-all`, and `feed`. Those that start or recreate containers refuse, and change nothing: `up`, `update`, `restore`, `codex`, `rotate-keys`, and `modules off`.

### Without setup.sh

- **The account commands**, in the Dashboard's container: `docker exec lifeos-dashboard-1 node scripts/lifeos-auth.mjs` followed by `recover-owner` (with `--reset-2fa`), `legacy-access` (with `on` or `off`), or `sign-out-all` (with `--keep-devices`). Unlike `./setup.sh sign-out-all`, it asks nothing first ([Accounts](accounts.md)).
- **Backups** run every night by themselves, into the backup directory ([Backups](backups.md)). **Back up now** on the Status page, or `docker exec lifeos-backup-1 /service now`.
- **A restore** needs `setup.sh` and `jq` on the server, in the directory Compose runs Life OS from. With Dockge, that is its stack's directory. With Portainer or Unraid, hand Life OS to `setup.sh` for the restore, then back:
  1. In a directory on the server, put the release's four files and a copy of your `.env` (`chmod 600 .env`).
  2. Remove the stack's containers; the data stays in the `lifeos-*` volumes and the backup directory: `docker rm -f $(docker ps -aq --filter label=com.docker.compose.project=lifeos)`.
  3. `./setup.sh up`, then `./setup.sh restore list` and `./setup.sh restore <the backup>` ([Restore](backups.md#restore)).
  4. `docker compose down` in that directory (the containers go, the data stays), and start the stack again in your stack manager.
- **A new signing key** (`./setup.sh rotate-keys`) also needs `setup.sh` where Compose runs Life OS, the same way as a restore, or the steps by hand in [Keys](accounts.md#keys).

More: [Modules](modules.md), [Access it safely](access.md), [Backups](backups.md), [Updating](updating.md), [Troubleshooting](troubleshooting.md).
