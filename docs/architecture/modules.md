# Modules

Life OS is a core that always runs and optional modules that an install turns on or off (P20-01). This page is how that works and how a module is described; turning modules on and off as an owner is in [docs/install/modules.md](../install/modules.md).

| Module | `install` | Compose services |
| --- | --- | --- |
| `core` | core | PostgreSQL and its provisioning; Finance, Tasks, Food, Inbox, Calendar, calendar-feeds, recipe-import, Activity, Notifications (each with its migrations); the Dashboard, the monitor, backups, and the gateway |
| `ai` | optional | `mcp`, `processor` |
| `voice` | optional | `voice-migrate`, `voice`, `voice-worker`, `whisper` |
| `google-calendar` | optional | `google-calendar-migrate`, `google-calendar` |
| `banking` | optional | `banking-migrate`, `banking` |
| `cloudflare` | optional | `cloudflared` |
| `ntfy` | optional (needs `cloudflare`) | `ntfy` |
| `server-health` | optional | `host-health`, `host-health-docker` |

The owner chose the split on 2026-11-10: the core is what Life OS is without any account elsewhere or access to the host; every module that sends data out (AI, Google, the bank, the tunnel, ntfy), needs much memory (Whisper), or reads the host (server health) is opt-in, and a new install runs none of them.

## One switch: COMPOSE_PROFILES

Each optional module is the Compose profile of its id: every service of the module has `profiles: [<id>]` in `compose.yaml`, and the core's have none. `COMPOSE_PROFILES` in `.env` lists the modules that run, so `docker compose up -d` starts the core and those, `./setup.sh` and plain Compose agree, and a module that is off has no containers. Compose passes the same list to the Dashboard and the gateway as `LIFE_OS_MODULES` (`${COMPOSE_PROFILES:-}`); there is no second list to keep in step.

- **Unset `LIFE_OS_MODULES`** (a Dashboard started without Compose, such as `npm run dev`, or an older `compose.yaml`) means every module, as before modules. Empty means the core alone.
- **Dependencies.** A service depends only on its own module's services, the core's, or those of the modules its module requires (`tools/modules` checks it); the gateway depends on the core alone and resolves every upstream per request anyway. So the core starts, and no service waits, whatever is off.
- **Settings Compose requires** (`${NAME:?}`) are interpolated for every service, even one that is off, so a module's required settings stay few: `setup.sh` writes `LIFE_OS_CODEX_RUNTIME_UID` and `LIFE_OS_CODEX_HOME_HOST_PATH` (a path; the directory comes when AI processing is turned on) at every first setup.
- **Databases** are provisioned for every module (`infra/postgres/service-databases.conf`), on or off, and backed up every night. A module that is off keeps its data, and turning it on again runs its migrations and picks it up.

`setup.sh` turns modules on and off (`./setup.sh modules`): it edits `COMPOSE_PROFILES`, refuses to turn off a module another one needs, removes the containers of a module that goes off (`docker compose rm --stop --force <its services>`, its volumes untouched), and `up` and `update` remove those of any module that is off. In a clone of the repository it changes `.env` and prints the Compose commands instead ([Deployment](../operations/deployment.md#modules)).

## A module's manifest

Each module is described once, in `modules/<id>.yaml`, and the rest of Life OS reads that description instead of naming modules:

```yaml
id: voice                      # the file's name, and the Compose profile
name: Voice notes
summary: record notes; Whisper transcribes them on this server (up to 2 GB of memory while it does)
caution: …                     # optional: what turning it on means (setup.sh says it)
install: optional              # core (always on; exactly one module, core) or optional
order: 20                      # where setup.sh lists it
requires: []                   # other optional modules that must be on
setup: whisper                 # the setup.sh command that sets it up (optional)
compose:
  services: [voice-migrate, voice, voice-worker, whisper]
  volumes: [voice-audio, whisper-models]
databases: [voice]             # infra/postgres/service-databases.conf names
gateway:
  routes: [/api/voice/]        # the gateway locations that proxy to its services
dashboard:
  nav: []                      # navigation entries (hrefs) it brings
  services: [voice]            # Dashboard service keys it brings (src/config/services.ts)
  checks: [voice]              # Status checks it brings
  features: [capture.voice]    # Dashboard features it turns on
mcp-tools: []                  # MCP tools it contributes (names or prefixes ending in *)
settings: [LIFE_OS_VOICE_*, LIFE_OS_WHISPER_*]   # its .env settings (database passwords come with its databases)
```

`tools/modules` checks every manifest against what it describes, and fails on anything that is not claimed by exactly one module: every service and named volume of `compose.yaml` (with the profiles and dependencies above), every database, every gateway location that proxies to a service, every Dashboard service key and navigation entry, every Status check it names, every MCP tool of `services/mcp`, and every `LIFE_OS_*` setting of `.env.example` and `compose.yaml`. Then it writes what reads the manifests where they cannot be read directly:

| Written file | Read by |
| --- | --- |
| `infra/nginx/module-routes` | the gateway image (`modules.envsh`) |
| `apps/dashboard/src/config/modules.generated.ts` | the Dashboard (`src/lib/modules.ts`) |
| the block between `# BEGIN generated by tools/modules` and `# END generated by tools/modules` in `setup.sh` | `setup.sh`, which the release ships alone |
| `tools/lib/modules.bash` | the tools: `tools/verify-compose`, `tools/check-ingress`, `make life-up`, CI's build |

`tools/modules --check` (in `tools/check --modules`, CI's `check-generated`) fails when one of them is stale; `tools/modules` writes them; `tools/modules --list` lists the modules.

## The gateway

At every start the gateway's `modules.envsh` reads `LIFE_OS_MODULES` and `module-routes` and renders one `map` entry per route of a module that is off; such a request is answered `404` with `{"error":{"code":"module_off",…}}` at once, before the gateway would resolve the service's name. A route of a module that is on is passed on as before (a stopped service is a `502`). `tools/test-gateway-modules` checks both, in throwaway containers.

## The Dashboard

`src/lib/modules.ts` reads `LIFE_OS_MODULES` and the manifests' Dashboard part and answers with parts, never modules: `featureOn("capture.voice")`, `serviceOn("banking")`, `checkOn("server")`, and the navigation entries. The root layout hands them to the client components (`ModulesProvider`, `useFeature`). So:

- **Navigation** lists the entries of the modules that are on (today all of them are the core's).
- **Status and Home** build only the checks of modules that are on, and probe only their services: a module that is off is never asked, so a slow lookup of a name that is not there (BUG-50) cannot happen. Without `ai`, Inbox's check says captures are kept as written instead of a backlog.
- **Features** show with their module: the voice recorder (`capture.voice`), the Google Calendar control (`calendar.google`), the bank connection (`finance.bank`), Plan my budget and Payday (`finance.ai-plan`), Get ideas (`food.ideas`); without `ai.processing` a new capture is not followed.
- **Calls fail softly.** The Dashboard's own proxies to a module's service answer `404 module_off` while it is off, like the gateway; its pages (the Google and bank callbacks) are `404`.

## Tests

- `tools/test-setup`: `./setup.sh modules`, the first setup's offer, integration setups turning their module on, removing the containers of modules that are off, the migration of an `.env` from before modules.
- `tools/test-gateway-modules` and `tools/check-gateway-hosts`: the gateway's module routes.
- The Dashboard's unit tests (`src/lib/modules.test.ts`, `runtime-status.test.ts`) and the browser suite: tests of a module are tagged with its id (`@voice`, `@ai`, `@banking`, `@google-calendar`, `@server-health`) and skip themselves while it is off; `e2e/modules.spec.ts` checks both ways.
- CI's full-stack job runs every module the verification stack can (all but `cloudflare` and `ntfy`, which never run there); the `Full stack, core only` job runs the core alone, without the workaround for slowly resolving names ([CI](../operations/ci.md)).

## Where this is going

This is the install level of modules. The design leads to three more steps without changing what a manifest is:

- **Plug in, plug out.** Installing a module later from the Dashboard is turning its profile on and starting its services; uninstalling is turning it off, which keeps its data. Deleting a module's data is a separate, explicit step, after a backup. A module's services could come with their own Compose file (`include:`) next to its manifest.
- **Space and person levels.** The product plan's module switches per space and per person sit on top of the install level: a module must be installed to be on for a space, and the Dashboard already asks about parts, so a per-space or per-person filter narrows the same parts.
- **Third-party modules** are not supported, but nothing names a module outside its manifest, `setup.sh`'s setup steps, and the Dashboard's own UI for it, so one could bring its manifest, services, and routes.
