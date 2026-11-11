# Modules

Life OS is a core that always runs, plus optional modules that run only when you turn them on. A new install runs the core alone: nothing it doesn't need, no AI sign-in, no bank or Google settings, no speech-to-text model to download.

**The core:** the Dashboard, Tasks, Calendar (with calendar feeds), Inbox for typed notes and photos, Food (shopping list, pantry, recipes), Finance (manual entries and CSV imports), notifications in Life OS (and on the phone app, with [web push](notifications.md)), and the nightly [backups](backups.md).

**The optional modules:**

| Module | What it adds | Set up with | Guide |
| --- | --- | --- | --- |
| `ai` | AI processing: files each note you capture as a task, event, shopping item, or expense; recipe ideas, AI categories, and budget plans. Your notes go to OpenAI or Anthropic. | `./setup.sh codex` or `./setup.sh claude` | [AI processing](ai-processing.md) |
| `voice` | Voice notes, transcribed on your server by Whisper. Up to 2 GB of memory while it transcribes. | `./setup.sh whisper` (optional: the model and language) | [Voice notes](voice.md) |
| `google-calendar` | Two-way sync with Google Calendar. | `./setup.sh google` | [Google Calendar](google-calendar.md) |
| `banking` | Your bank account in Finance, read-only, through Enable Banking. | `./setup.sh banking` | [Bank sync](bank-sync.md) |
| `cloudflare` | Reach Life OS from anywhere through a Cloudflare Tunnel, behind Cloudflare Access. | `./setup.sh cloudflare` | [Access it safely](access.md#cloudflare-tunnel-and-access) |
| `ntfy` | Push notifications through your own ntfy server. Needs `cloudflare`. | `./setup.sh ntfy` | [Notifications](notifications.md#ntfy) |
| `server-health` | The Status page's Server card: memory, disks, load, and every container on the server. It reads the Docker socket, through a read-only gateway of its own. | none | |

## Turn modules on and off

```sh
./setup.sh modules              # each module, on or off, and what it has set up
./setup.sh modules on voice     # turn one on (or several: on voice server-health)
./setup.sh modules off voice    # turn one off
./setup.sh up                   # apply it
```

- **Setting a module up turns it on.** `./setup.sh google`, `./setup.sh banking`, `./setup.sh whisper`, `./setup.sh codex`, and the others each switch their module on, so you rarely need `modules on` at all.
- **Some modules are set up as they are turned on.** `modules on cloudflare` runs `./setup.sh cloudflare`, which checks that Cloudflare Access protects your hostname first; `modules on ntfy` runs `./setup.sh ntfy`; `modules on ai` asks whether to sign in to Codex or Claude Code (with `--yes`, it refuses until one is signed in). Google Calendar and bank sync can be on before they are set up: they say "not configured" until you run their setup.
- **Off removes the module's containers at once, and keeps its data.** Its database, its volumes (such as `lifeos-voice-audio` and the Whisper model), and its settings in `.env` stay, and the nightly backups keep backing its database up. Turning it on again picks up where it was. `ntfy` off also takes `ntfy` out of the notification channels.
- **What needs another module:** `ntfy` needs `cloudflare`. `modules off cloudflare` refuses while `ntfy` is on: `./setup.sh modules off ntfy cloudflare`.
- **A module that is off leaves no trace in Life OS.** Its pages, buttons, and Status checks are gone (the microphone next to the capture box, the Google Calendar button, the bank connection, Plan my budget, Get ideas, the Server card), Life OS never asks for it, and its addresses answer `404` with `module_off`. Without AI processing, your notes stay in Inbox as you wrote them.

`./setup.sh status` lists the modules too.

## The first setup

`./setup.sh` lists the modules and asks which to turn on now; each one you pick is set up on the spot. Say no to all of them to start with the core and add modules later.

Without questions (`--yes`), it turns on the modules `LIFE_OS_SETUP_MODULES` names, comma-separated, and those whose settings are in the environment (see [Without questions](README.md#without-questions)):

```sh
LIFE_OS_TIMEZONE=Europe/Berlin LIFE_OS_SETUP_MODULES=voice,server-health ./setup.sh --yes
```

## In .env

The modules that run are `COMPOSE_PROFILES` in `.env`, comma-separated: each module is the Compose profile of its name, so plain `docker compose` commands start the same modules as `./setup.sh up`. Empty is the core alone:

```sh
COMPOSE_PROFILES=ai,voice,cloudflare
```

Edit it by hand if you like; then `./setup.sh up` starts what is on and removes the containers of what is off. Life OS's Dashboard and gateway read the same list.

## Updating an install from before modules

Before modules (up to `v0.1.0-preview.5`), every module ran. The first `./setup.sh update` (or `./setup.sh up`) with a release that has modules turns on what your install used, so nothing you relied on stops:

- `voice` and `server-health`, which needed nothing set up;
- `cloudflare` and `ntfy`, as your `COMPOSE_PROFILES` had them;
- `ai`, when Codex is signed in or Claude Code is set up. Without a sign-in, the AI processor only restarted about once a minute, so it stays off now;
- `google-calendar` and `banking`, when they are set up.

It says what it kept and what is off, and adds `LIFE_OS_MODULES_LISTED=yes` to `.env` so that it happens once. Turn anything else on or off with `./setup.sh modules`.

## Deleting a module's data

Turning a module off never deletes anything. To delete its data for good, first take a backup (`./setup.sh update` does, or the Status page's Back up now), then remove its volumes with `docker volume rm`, such as `lifeos-voice-audio lifeos-whisper-models` for voice notes; a module's database stays in PostgreSQL, empty of use, until you [uninstall](uninstall.md) Life OS. Deleting a module's database from Life OS itself comes later.
