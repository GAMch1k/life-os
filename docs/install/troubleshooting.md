# Troubleshooting

## Where to look

1. **The Status page** (`/status` in Life OS) checks every part that runs: the services, backups, notifications, and each [module](modules.md) that is on (AI processing, voice, the server's memory and disk, each integration). Each problem comes with what to do.
2. **On the server**, from the directory with `setup.sh`:

   ```sh
   ./setup.sh status                       # what runs, and what is set up
   docker compose ps --all                 # every container, and its state
   docker compose logs --tail 100 <name>   # what a container said, such as tasks or processor
   ```

   Life OS keeps passwords and keys out of its logs, but the logs can contain what you captured: read them before you share them.

## Common problems

| Problem | What to do |
| --- | --- |
| `./setup.sh` says a `.env` already exists | It never overwrites one. Change a part with its own command (`./setup.sh gateway`, `./setup.sh google`, …), or start over with `./setup.sh --force-new`, which keeps a copy and your database passwords. |
| `up`, `update`, or `codex` says another Life OS runs on this machine | Docker has `lifeos` containers started from another directory, and running Compose here would recreate them with this directory's `.env`. Manage that install from its own directory. If you moved it here, remove the old containers (`docker ps -a --filter label=com.docker.compose.project=lifeos` lists them; your data stays in the `lifeos-*` volumes) and run the command again, with the old `.env`. |
| "cannot talk to Docker" | Add your user to the `docker` group (`sudo usermod -aG docker $USER`), log out and back in. |
| `up` cannot find the images ("manifest unknown", "no matching manifest") | The version in `.env` (`LIFE_OS_VERSION`) doesn't exist, or the server isn't x86_64: the preview has no ARM images yet. |
| `up` fails: the port is in use | Something else listens on port 3000. Choose another: `./setup.sh gateway --port 3100`, then `./setup.sh up`. |
| The browser can't connect through the SSH tunnel | Is `ssh -N -L 3000:127.0.0.1:3000 …` still running? Use the same port in the browser, and `http://`, not `https://`. |
| The browser gets an empty page or "connection closed" | Life OS answers only the names it knows. Open it at `127.0.0.1`, `localhost`, the address it listens on, or its address's hostname; to add a name, `./setup.sh gateway --public-url http://<name>:3000`. |
| Captures stay in Inbox as written | AI processing is off: it is a [module](modules.md). Turn it on by signing in, `./setup.sh codex` (or `./setup.sh claude`), then `./setup.sh up`. |
| Status says the Inbox processor is not running | AI processing is on, but the processor is not: `docker compose logs processor`. Without a sign-in it stops at start: `./setup.sh codex`, or turn AI off with `./setup.sh modules off ai`. |
| A page or a phone app says a module is turned off (`module_off`) | That [module](modules.md) is off on this server: `./setup.sh modules` lists them; `./setup.sh modules on <id>`, then `./setup.sh up`. |
| The microphone, the Google Calendar button, the bank connection, or the Server card is missing | Their modules are off: `voice`, `google-calendar`, `banking`, `server-health` ([Modules](modules.md)). |
| Status says Codex is signed out | Sign in again: `./setup.sh codex`. |
| Status says the Codex usage limit is reached | Captures wait and are processed after the reset time Status shows. |
| Voice notes wait; Status says Whisper is not ready | On the first start it downloads its model (`docker compose logs whisper`). If it keeps restarting, the server lacks memory: choose a smaller model (`./setup.sh whisper`). If its log says whisper.cpp does not know the language, set `auto` or a valid code with `./setup.sh whisper` ([Language](voice.md#language)). |
| The server runs out of memory | Status's Server card names the biggest users. Choose a smaller Whisper model, or give the server more memory. |
| Google: `redirect_uri_mismatch` | The redirect URI in Google must be exactly what `./setup.sh google` printed. See [Google Calendar](google-calendar.md). |
| The bank connection fails | See [Bank connection troubleshooting](../operations/banking.md#troubleshooting). |
| Cloudflare shows error 1033, 530, or 502 | The tunnel isn't running, or its route is wrong. `docker compose ps cloudflared` must say healthy, and the route must point to HTTP `gateway:80`. See [Cloudflare: diagnose](../operations/cloudflare.md#diagnose). |
| Status's Server card cannot read the containers | `LIFE_OS_DOCKER_GID` in `.env` must be the group of `/var/run/docker.sock` (`stat -c %g /var/run/docker.sock`). Fix it and run `./setup.sh up`. |
| Backups fail | Status says why. Common causes: a full disk, or a `backups/` directory that isn't yours (`ls -ld backups`: it must belong to you, mode `drwx------`). |
| Something else | The [operations guide](../operations/deployment.md) and the [Self-hosting guide's troubleshooting](../../SETUP.md#16-troubleshooting) cover more. They are written for a server built from source, but the services are the same. |

## Report a problem

Open an issue on [GitHub](https://github.com/GAMch1k/life-os/issues) with the version (`LIFE_OS_VERSION`), what you did, what happened, the output of `./setup.sh status`, and the logs of the containers involved. Never paste `.env`.
