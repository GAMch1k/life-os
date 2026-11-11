# Install Life OS

Life OS is a personal system you run on your own server: tasks, a calendar, an Inbox for quick notes and voice notes, a shopping list and pantry, and your finances, in one web app. An optional AI files what you capture: "dentist on Friday at 10" becomes a calendar event, "buy milk" goes on the shopping list.

A new install runs the core: the Dashboard, Tasks, Calendar, Inbox, Food, Finance, notifications, and backups. AI processing, voice notes, Google Calendar, bank sync, the Cloudflare Tunnel, ntfy, and server health are [modules](modules.md) you turn on when you want them.

This guide installs Life OS from its published Docker images with one setup script. You don't build anything, and you don't need the source code. (One exception, only if you choose it: AI processing with Claude Code, which the published images do not contain; `./setup.sh claude` adds it to a local copy of the processor image.)

> [!WARNING]
> **Early preview.** Life OS has no login of its own yet: whoever can open it can read and change everything in it. The setup script keeps it on `127.0.0.1`, so only the server itself can reach it, and you open it through an SSH tunnel, a VPN, or Cloudflare Access ([Access it safely](access.md)). Never expose it straight to the internet. Previews have rough edges and may change between releases, so back up before you update.

## What you need

- **A Linux server**, 64-bit x86 (amd64). ARM images (Raspberry Pi, ARM cloud servers) come later.
- **Memory:** 2 GB at least, 4 GB to be comfortable. Life OS uses about 0.5 GB when idle. Whisper can use up to 2 GB while it transcribes a voice note, and each AI run uses a few hundred MB.
- **Disk:** 10 GB free. The images take about 3 GB and the Whisper model about 0.5 GB; your data and the nightly backups grow from there.
- **Docker Engine 25 or newer with the Compose plugin** (`docker compose`, version 2.27 or newer). Install it from [docs.docker.com](https://docs.docker.com/engine/install/). The reference server runs Docker Engine 29 and Compose 5.1.
- **A user that can run Docker** (a member of the `docker` group), plus `curl`, `openssl`, and Bash 4.4 or newer, which current Linux distributions have.
- **One Life OS per machine.** Every install is the same Docker Compose project, `lifeos`, with the same `lifeos-*` volumes, so a second one in another directory would take the first one over. `setup.sh` refuses `up`, `update`, and `codex` while Docker has `lifeos` containers started from another directory, and says where they are.

## Quick start

1. **Download a release** into a new directory. Pick the version from the [releases page](https://github.com/GAMch1k/life-os/releases):

   ```sh
   mkdir ~/life-os && cd ~/life-os
   version=v0.1.0-preview.4
   release=https://github.com/GAMch1k/life-os/releases/download/$version
   for file in compose.yaml setup.sh SHA256SUMS; do curl -fsSLO "$release/$file"; done
   # GitHub serves .env.example as default.env.example
   curl -fsSL -o .env.example "$release/default.env.example"
   ```

2. **Check the download.** Every file must say `OK`:

   ```sh
   sha256sum -c SHA256SUMS
   ```

   With the [GitHub CLI](https://cli.github.com/), you can also check that the files were built by this project's release workflow: `gh attestation verify setup.sh -R GAMch1k/life-os` (and `compose.yaml`, `.env.example`). For the images, see [Verify the images](updating.md#verify-the-images).

3. **Set up:**

   ```sh
   chmod +x setup.sh
   ./setup.sh
   ```

   It checks the server (Docker, memory, disk), asks for your timezone, your currency, and the address you will open Life OS at, and writes `.env` with new passwords. Then it offers the optional [modules](#modules-and-integrations); skip them all if you just want a look: the core runs without them.

4. **Start it:**

   ```sh
   ./setup.sh up
   ```

   The first start downloads the images (about 3 GB with every module, less for the core alone) and takes a few minutes. When Life OS answers, the script prints its address and what to do next.

5. **Open it.** Life OS answers only on the server. From your own computer, open an SSH tunnel and keep it running:

   ```sh
   ssh -N -L 3000:127.0.0.1:3000 you@your-server
   ```

   Then open <http://127.0.0.1:3000>. Start with the **Status** page (`/status`): it shows what works, what is still starting, and what needs you. For everyday use from your phone and laptop, set up [a VPN or Cloudflare Access](access.md).

## What setup.sh writes

Everything stays in the directory you downloaded into:

| Path | What |
| --- | --- |
| `.env` | Your settings and every password and key, readable only by you (mode 600). **Keep a copy** in a password manager or another safe place: the backups don't include it, and without it the backups can't be opened by a new install. |
| `backups/` | The nightly backups ([Backups](backups.md)). |
| `codex-home/` | The AI sign-in, once you [turn AI processing on](ai-processing.md). It is as sensitive as a password. |

Your data itself (the database, photos you capture, voice notes) lives in Docker volumes named `lifeos-*`.

`./setup.sh` never overwrites an existing `.env`. To change one part later, run that part's command (below). To start over, `./setup.sh --force-new` writes a new `.env`, keeps a copy of the old one, and keeps its database passwords and keys, so your existing data still opens.

## Modules and integrations

Life OS works without any of them. Each optional module can be turned on during the first `./setup.sh`, or any time later: `./setup.sh modules` lists them, `./setup.sh modules on <id>` and `off <id>` switch them, and setting one up with its own command (below) turns it on. Off, a module's containers go and its data stays. After a change, `./setup.sh up` applies it. All about it: [Modules](modules.md).

| Command | What it adds (its module) | Guide |
| --- | --- | --- |
| `./setup.sh codex` | AI processing of your notes, with your ChatGPT plan (`ai`) | [AI processing](ai-processing.md) |
| `./setup.sh claude` | Or AI processing with Claude Code instead, with your Claude plan or an Anthropic API key: downloads Claude Code from Anthropic onto this server (`ai`) | [AI processing](ai-processing.md#claude-code-instead-of-codex) |
| `./setup.sh whisper` | Voice notes, with the speech-to-text model and language you choose (`voice`) | [Voice notes](voice.md) |
| `./setup.sh cloudflare` | Access from anywhere, through Cloudflare Tunnel and Access (`cloudflare`) | [Access it safely](access.md#cloudflare-tunnel-and-access) |
| `./setup.sh google` | Two-way sync with Google Calendar (`google-calendar`) | [Google Calendar](google-calendar.md) |
| `./setup.sh banking` | Your bank account in Finance, through Enable Banking (`banking`) | [Bank sync](bank-sync.md) |
| `./setup.sh ntfy` | Notifications on your phone, through your own ntfy server (`ntfy`, needs `cloudflare`) | [Notifications](notifications.md#ntfy) |
| `./setup.sh modules on server-health` | The Status page's Server card: memory, disks, and every container on the server (`server-health`; it reads the Docker socket) | [Modules](modules.md) |
| `./setup.sh webpush` | Notifications on your phone, through the Life OS app (the core) | [Notifications](notifications.md) |
| `./setup.sh feed` | A calendar feed (ICS), such as holidays or a sports team (the core) | [Calendar feeds](calendar-feeds.md) |

The phone and watch apps are on their own page: [Phone and watch apps](phone-apps.md).

## Commands

| Command | What it does |
| --- | --- |
| `./setup.sh` | First-time setup: checks, questions, `.env`, integrations. |
| `./setup.sh up` | Starts Life OS (downloading missing images), waits until it answers, prints the address. |
| `./setup.sh update [VERSION]` | Backs up, downloads the new images, restarts. See [Updating](updating.md). |
| `./setup.sh status` | What runs, and which modules are on and set up. Never prints a secret. |
| `./setup.sh modules [on\|off ID...]` | Lists the optional modules, or turns them on or off ([Modules](modules.md)). |
| `./setup.sh check` | Only checks the server. |
| `./setup.sh gateway` | Changes where Life OS listens (`--bind`, `--port`) and its address (`--public-url`). |
| `./setup.sh help` | Every command and option. |

Plain `docker compose` commands work too, from the same directory: `docker compose ps`, `docker compose logs <service>`, `docker compose stop`.

## Without questions

`--yes` (or `-y`) runs without questions, for scripts and automation: every question takes its default, or the value of an environment variable. Settings that go into `.env` use their own names; values only the script needs start with `LIFE_OS_SETUP_`. With `--yes`, the first setup turns on the modules `LIFE_OS_SETUP_MODULES` names, comma-separated (such as `voice,server-health`), and sets up each integration whose variables are present, which turns its module on.

| Question | Variable |
| --- | --- |
| Timezone, currency | `LIFE_OS_TIMEZONE`, `LIFE_OS_FINANCE_CURRENCY` |
| Address, where it listens | `LIFE_OS_PUBLIC_URL`, `LIFE_OS_GATEWAY_BIND_ADDRESS`, `LIFE_OS_GATEWAY_PORT` (or `--public-url`, `--bind`, `--port`) |
| Image version and registry | `LIFE_OS_VERSION`, `LIFE_OS_IMAGE_REGISTRY` |
| Modules to turn on | `LIFE_OS_SETUP_MODULES` (comma-separated ids; `ai`, `cloudflare`, and `ntfy` need their own settings below, or a person) |
| Whisper | `LIFE_OS_SETUP_WHISPER_MODEL` (`tiny`, `base`, `small`, `medium`, `large-v3-turbo`), `LIFE_OS_WHISPER_LANGUAGE` (`auto`, or a code such as `de`; unset keeps the current one) |
| Cloudflare | `LIFE_OS_CLOUDFLARE_TUNNEL_TOKEN` and an https `LIFE_OS_PUBLIC_URL` |
| Google Calendar | `LIFE_OS_GOOGLE_CLIENT_ID`, `LIFE_OS_GOOGLE_CLIENT_SECRET` |
| AI with Claude Code | `LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN` (your Claude plan, from `claude setup-token`) or `LIFE_OS_ANTHROPIC_API_KEY` (an Anthropic API key, billed per use), not both; `LIFE_OS_CLAUDE_MODEL` (`sonnet`, `opus`, `haiku`, or a full model name; unset keeps the current one) |
| Bank sync | `LIFE_OS_ENABLE_BANKING_APP_ID`, `LIFE_OS_ENABLE_BANKING_KEY_FILE` |
| ntfy | `LIFE_OS_NTFY_PUBLIC_URL`, `LIFE_OS_SETUP_NTFY_PHONE_PASSWORD` |
| Phone app push | `LIFE_OS_SETUP_WEBPUSH=on`, and `LIFE_OS_WEBPUSH_SUBJECT` (`mailto:you@example.com`) without an https address |
| A calendar feed | `LIFE_OS_SETUP_FEED_NAME`, `LIFE_OS_SETUP_FEED_URL` |

For example: `LIFE_OS_TIMEZONE=Europe/Berlin ./setup.sh --yes && ./setup.sh up`. The Codex sign-in always needs a person: run `./setup.sh codex` at a terminal.

## More

- [Modules](modules.md): what is optional, and turning it on and off.
- [Access it safely](access.md): SSH tunnel, VPN, Cloudflare Tunnel and Access.
- [Backups and restore](backups.md), [Updating](updating.md), [Uninstalling](uninstall.md), [Troubleshooting](troubleshooting.md).
- [Phone and watch apps](phone-apps.md).
- Running Life OS from the source code, or developing it: [SETUP.md](../../SETUP.md) and the [technical documentation](../README.md).
- Which downloads are official: [Official sources and name](../../README.md#official-sources-and-name).
