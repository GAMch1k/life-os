# Continuous integration

GitHub Actions checks every push and pull request with `.github/workflows/ci.yml`: the source, then the full stack (the images, the smokes, and the browser suite on the disposable verification stack). CI never deploys. Merges are still made locally and pushed to master.

## When it runs

- **Every push** to any branch. Dependabot's branches (`dependabot/**`) run only as their pull request.
- **Every pull request** to master, also from forks.
- **By hand**: Actions → CI → Run workflow.

A newer push to a branch or pull request cancels that ref's older run. Every push to master runs to the end.

Runs use GitHub-hosted runners (`ubuntu-24.04`) and the workflow's token is read-only (`permissions: contents: read`). No job needs a secret, and none signs in to an AI. The workflow has no `pull_request_target` or `workflow_run` trigger, so a fork's pull request runs with a read-only token and no secrets.

## Jobs

The first five jobs run in parallel. `full-stack` waits for the four that block, as a later job can: `needs: [check-generated, check-go, check-dashboard, integration]`.

| Job | What it runs | Time | Locally |
| --- | --- | --- | --- |
| `check-generated` | `tools/check --gateway --contracts --sqlc`: the gateway's host list, the OpenAPI lint, generated Go and TypeScript API drift, and sqlc drift | about 1 min | `tools/check --gateway --contracts --sqlc` |
| `check-go` | `tools/check --go`: gofmt, `go vet`, `go test`, and the build of every module under `pkg/`, `services/`, and `templates/` (`GOWORK=off`) | about 1 min | `tools/check --go` |
| `check-dashboard` | `tools/check --dashboard`: Prettier, ESLint, the unit tests, the typecheck, and `next build` | about 1.5 min | `tools/check --dashboard` |
| `integration` | `tools/test-integration` against a PostgreSQL service container | about 2 min | [Integration tests](#integration-tests) |
| `govulncheck` | `tools/check-vulnerabilities`: govulncheck on every Go module. **Not blocking yet.** | about 2 min | `tools/check-vulnerabilities` |
| `full-stack` | The images, then the smokes and the browser suite (without the `@codex` tests) on the verification stack | 14 to 19 min: 6.5 to 10 to build, 7 to 8 to test | [Full-stack tests](#full-stack-tests) |

The times are with warm caches; without them, each job takes up to a minute longer. The checks take about as long as the slowest of them, about 2 minutes; with `full-stack` after them, a whole run takes 16 to 21 minutes.

`tools/check` without flags runs all of its parts, as before. Like `make check`, it needs a clean working tree. `check-generated` and `check-dashboard` run `npm ci --prefix apps/dashboard` first.

### Tool versions

CI installs the versions the repository already uses:

- **Go** from `go.work` (`go 1.27.1`). Changing Go means `go.work`, every `go.mod`, and the Dockerfiles' `golang:` images.
- **Node 22**, as the Dashboard's image (`node:22-alpine`).
- **sqlc 1.31.1**, the release binary, checked against the SHA-256 in the workflow's `SQLC_SHA256`. The version is the generated code's `sqlc v1.31.1` header (see `pkg/database/README.md`).
- **oapi-codegen v2.8.0**, as `tools/check-contracts` requires.
- **govulncheck v1.9.0**, pinned in `tools/check-vulnerabilities`.

Every action is pinned to a full commit SHA, with its version in a comment. Dependabot keeps both up to date.

Each Go job caches the module and build caches under its own key (the Go version and the hash of every `go.sum`). The npm download cache and Next.js's build cache (`.next/cache`) are cached too. `full-stack` caches only the Whisper model; it builds the images from scratch ([why](#not-cached-the-images)).

## Integration tests

The integration tests (the `*_integration_test.go` files, the backup tests, and `pkg/database`'s) skip unless their variable names a PostgreSQL database. `tools/test-integration` prepares one server for all of them and runs them:

- It takes a throwaway server's superuser URL in `INTEGRATION_TEST_DATABASE_URL`. It refuses a server that has a database from `infra/postgres/service-databases.conf`, so it never runs against Life OS's own server, and it refuses a login that is not a superuser.
- Each variable a test reads gets a fresh, empty database of its own (services whose tests install pgcrypto do it once per database, each under its own advisory lock, so two services' tests never race on one database), dropped and created again on every run: `TASKS_TEST_DATABASE_URL` gets `it_tasks`, `BACKUP_TEST_DATABASE_URL` gets `it_backup`, and so on, plus `RESTORE_TEST_DATABASE_URL` for `tools/test-restore-database`. `DATABASE_TEST_URL` (`pkg/database`) gets `it_database` with its example migration.
- The tests need nothing more. Each service's tests create a private schema per test, apply the service's migrations to it, and drop it afterwards. The backup and restore tests create their own roles and databases, which is why the login must be a superuser. Google Calendar's tests also build and start the real Calendar service on a schema of their own.
- `psql`, `pg_dump`, and `pg_restore` run from the PostgreSQL image (the wrappers in `services/backup/internal/backup/testdata`), so they always match the server's version. That needs Docker. To use client programs on `PATH` instead, set `INTEGRATION_TEST_PG_BIN` to their directory.
- It finds the modules by the variables they read. Then it runs `GOWORK=off go test -count=1 -v ./...` in each module with every variable set, and finally `tools/test-restore-database`.
- It prints the passed, skipped, and failed counts per module. It fails when a test fails, or when a test still skips with "… is not set": a new variable it does not know yet.

Locally, with a throwaway PostgreSQL that is deleted when it stops (one heavy job at a time on a small server):

```sh
docker run --rm -d --name lifeos-it-pg -p 127.0.0.1:55442:5432 -e POSTGRES_PASSWORD=test postgres:18.6-alpine
INTEGRATION_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55442/postgres tools/test-integration
docker stop lifeos-it-pg
```

`tools/test-integration services/finance` runs one module's tests and skips the restore tool's test.

In CI, the job's PostgreSQL is a service container of `postgres:18.6-alpine`. Its first step fails if that is not the image `compose.yaml` deploys (`LIFE_OS_POSTGRES_IMAGE`'s default). After a PostgreSQL upgrade, change `services.postgres.image` in `ci.yml` to match.

## Full-stack tests

The `full-stack` job runs the images, the smokes, and the browser suite on the disposable verification stack (`tools/verify-compose`, project `lifeos-verify`, the gateway on `127.0.0.1:13001`). It starts once `check-generated`, `check-go`, `check-dashboard`, and `integration` have passed, on the same pushes and pull requests, on a GitHub-hosted runner (`ubuntu-24.04`: 4 CPUs, 16 GB of memory). It needs no secret and signs in to no AI.

In order:

1. **Unknown service names answered at once.** The runner's systemd-resolved answers SERVFAIL for a single-label name it does not know, such as a service that is not running, and the Alpine images' C library (musl) waits 10 s on SERVFAIL before it gives up. Home and Status probe services that are not running here (the processor, and the monitor, backup, and host-health, which `make life-smoke` does not start), so the Dashboard's DNS lookups queued behind those waits until its requests to running services timed out too: Home showed no Task. `ResolveUnicastSingleLabel=yes` sends such names to the upstream DNS, which answers NXDOMAIN at once, as on a usual host.
2. **A throwaway `.env`**: `tools/ci-env "$RUNNER_TEMP/lifeos-ci"` writes `.env.example` with a new random password for every database login, `LIFE_OS_TIMEZONE=Europe/Berlin` (the browser tests' zone), every integration unset (Google, Enable Banking, ntfy, Cloudflare, web push, the ZAW feed; `tools/verify-compose` refuses them anyway), the backup directory under the scratch directory, and the runner's user as the owner of the backup directory, the Codex home, and the bank key. The Codex home is an empty directory. The file lives in the job's checkout only: Docker builds leave it out (`.dockerignore`), and nothing uploads it. `tools/ci-env` refuses to replace an existing `.env`.
3. **The Whisper model** (`ggml-small.bin`, 466 MB, the `.env.example` default) from the Actions cache, under a key made of its file name and URL; on a miss it is downloaded from Hugging Face and saved at once, so a failed run keeps it too. `LIFE_OS_VERIFY_OVERLAYS=tools/fixtures/whisper-models.compose.yaml` mounts it read-only into the verification stack's Whisper, which would otherwise download it into a fresh volume at every start (each smoke starts its own stack).
4. **The images**: `docker compose build`, all of them at once, as `make life-up` would.
5. **Playwright**: `npm ci` and `npx playwright install --with-deps chromium firefox`, so the voice test records in Firefox too.
6. **The smokes**, each on a stack of its own that it removes when it ends: `make life-smoke`, `tools/smoke-e2e`, `make verify-clean-db`, `tools/smoke-action-idempotency`, `tools/smoke-gateway-recreate`, and `tools/smoke-voice-lease-recovery` (the real whisper.cpp). Every smoke runs, even after another one failed.
7. **The browser suite**: `tools/browser-e2e --grep-invert @codex`, Chromium and Firefox, with Playwright's two workers.
8. **Teardown**, whatever happened: `tools/verify-compose down --volumes --remove-orphans`.

### Without Codex

The processor's Codex home is empty, so its entrypoint stops it at start ("missing Codex authentication") and Docker restarts it again and again; Status shows "The Inbox processor is not running", and captures wait in Inbox. Left out because they need the real, signed-in Codex CLI:

- the browser tests tagged `@codex` in their title: a browser capture that becomes a Task (`text-capture.spec.ts`), a quotation that lands in Human Review (`human-review-dismiss.spec.ts`), and the Event Log's trace of a capture through the agent (`activity-log.spec.ts`). Every other browser test that involves Human Review asks for the review itself, as the processor would;
- `make processor-smoke`, `processor-codex-actions-smoke`, `human-review-smoke`, `activity-trace-smoke`, `relative-dates-smoke`, `cross-language-smoke`, `calendar-ai-smoke`, `runtime-status-smoke` (it also signs Codex out inside the processor), `retry-schedule-smoke`, `codex-runtime-smoke`, `phase8-acceptance`, and `tools/benchmark-ai-runtime`. The Codex simulator they use (`tools/fixtures/codex-limit-simulator`) only stands in for a usage limit, a failing run, or a slow start, and hands every other call to the real CLI.

Also left out: `make verify-isolation`, which proves that the verification stack leaves a deployed Life OS's volumes alone and needs one, and the deployed-only browser tests (`calendar-capture`, `finance-capture`, `localized-capture`), which skip themselves without their `LIFE_OS_E2E_LIVE_*` settings. The forced-problem tests of `runtime-status.spec.ts` and `phase8-acceptance.spec.ts` skip too; their smokes run them.

### When it fails

With `LIFE_OS_VERIFY_LOGS_DIR` set, `tools/verify-compose down` first saves the stack's `docker compose ps --all` and every container's log, one file per stack. A failed run uploads two artifacts, kept 7 days:

- `compose-logs`: a directory per smoke (`life-smoke`, `smoke-e2e`, …, `browser-e2e`, and `left-up` for a stack a failed smoke left running), with a file per stack it brought up.
- `playwright`: `test-results/` (the failed tests' traces and page snapshots) and `playwright-report/` (the HTML report).

```sh
gh run download <run-id> -R GAMch1k/life-os -n playwright -D /tmp/lifeos-playwright
cd apps/dashboard
npx playwright show-report /tmp/lifeos-playwright/playwright-report
npx playwright show-trace /tmp/lifeos-playwright/test-results/<test>/trace.zip
```

### How long it takes

Measured in two green runs on 9 and 10 November 2026, with the Whisper model in the cache: 14 and 19 minutes for the job, 16 and 21 for the whole run from the push. The runners differ mostly in how fast they build.

| Part | Time |
| --- | --- |
| Setup, the `.env`, the Whisper model | under 10 s (without the cache, the download takes about 5 s and saving it 4 s) |
| `docker compose build` | 6.5 to 10 min. whisper.cpp's compile (`-j2`) finishes last; the 18 Go builds and the Dashboard's `npm ci` share the 4 CPUs with it |
| `npm ci` and Playwright's browsers | under 1 min |
| The smokes | about 4 min: `make life-smoke` 20 s, `tools/smoke-e2e`, `make verify-clean-db`, and `tools/smoke-action-idempotency` about 25 s each, `tools/smoke-gateway-recreate` 40 s, `tools/smoke-voice-lease-recovery` 2 min |
| The browser suite | 3 to 4 min: 114 tests pass and 30 skip, 2.7 to 3.8 min of it Playwright's |

### Run it again, or elsewhere

To run it again on GitHub: **Re-run failed jobs** on the run, or `gh workflow run CI -R GAMch1k/life-os --ref <branch>`.

Never on the server: `compose.yaml` names the project `lifeos`, so `docker compose build` and `up` there act on the deployed Life OS. On a machine that only tests Life OS (a throwaway VM with Docker, Node 22, and `jq`), from a clone:

```sh
tools/ci-env /tmp/lifeos-ci
mkdir -p /tmp/whisper-models && curl -fL -o /tmp/whisper-models/ggml-small.bin \
  https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin
export LIFE_OS_WHISPER_MODELS_DIR=/tmp/whisper-models \
  LIFE_OS_VERIFY_OVERLAYS=tools/fixtures/whisper-models.compose.yaml
docker compose build
npm ci --prefix apps/dashboard && (cd apps/dashboard && npx playwright install --with-deps chromium firefox)
make life-smoke && tools/smoke-e2e && make verify-clean-db && tools/smoke-action-idempotency \
  && tools/smoke-gateway-recreate && tools/smoke-voice-lease-recovery
tools/browser-e2e --grep-invert @codex
```

Without `LIFE_OS_VERIFY_OVERLAYS`, Whisper downloads the model itself at each start. If Home misses its Tasks there too, check how the machine answers an unknown name from a container (`docker network create t && docker run --rm --network t alpine:3.22 getent hosts nosuchservice; docker network rm t`): 10 s is the SERVFAIL wait above, and the job's `ResolveUnicastSingleLabel=yes` fixes it on systemd-resolved.

### Not cached: the images

The images are built from scratch in every run. The Go services' Dockerfiles copy the whole repository before `go build`, so any commit changes that layer and a build cache (buildx with `type=gha`) could reuse none of their work. It could reuse only whisper.cpp's build and the Dashboard's `npm ci`, a few minutes of the build, at the cost of a second builder, loading every image into Docker, and a whisper.cpp kept at whatever version the cache first saw (its Dockerfile clones the newest one).

## govulncheck

`tools/check-vulnerabilities` runs govulncheck on every Go module, the fixtures generator `tools/fixtures/documents` included. It lists the findings in the code's call paths, with their IDs per module at the end. The job is marked `continue-on-error`: it shows red, but the run passes.

When the job was added (9 November 2026), every module except the fixtures generator had findings:

| Where | Findings | Fixed in |
| --- | --- | --- |
| Go 1.27.1's standard library | GO-2026-6605, GO-2026-6607, GO-2026-6608, GO-2026-6613, and the bundled HTTP/2's GO-2026-6603, -6610, -6611, -6612, -6617 | Go 1.27.2 |
| Go 1.27.1's standard library | GO-2026-6809 (`crypto/x509`) | Go 1.27.3 |
| `golang.org/x/net` v0.57.0 | GO-2026-6603, -6610, -6611, -6612, -6617 (HTTP/2) | v0.60.0 |
| `golang.org/x/net` v0.57.0 (recipe-import) | GO-2026-6860 (`html`) | v0.62.0 |
| `golang.org/x/text` v0.40.0 | GO-2026-6629 | v0.41.0 |
| `github.com/klauspost/compress` v1.19.2 | GO-2026-6865 | v1.20.2 |
| `github.com/andybalholm/brotli` v1.2.2 | GO-2026-6888 | v1.2.7 |

Once they are fixed, remove `continue-on-error` from the job so that a new finding fails the run.

## Dependabot

`.github/dependabot.yml` asks for version updates every Monday at 06:00 (Europe/Berlin). Dependabot reads it from master, so it starts once the file is merged there:

- **Go modules** in `pkg/`, every `services/*` and `templates/*` module, and `tools/fixtures/documents`.
- **npm** in `apps/dashboard`.
- **Docker base images** of every Dockerfile (`apps/dashboard`, `services/*`, `services/voice/whisper`, `templates/*`). The images in `compose.yaml` are not covered; change those by hand.
- **GitHub Actions** in the workflows: the pinned SHA and its version comment together.

Minor and patch updates of an ecosystem come as one grouped pull request, and a major update as a pull request of its own. All action updates come as one pull request. At most 5 pull requests are open at once for Go and npm, and 3 for Docker and Actions.

CI checks each Dependabot pull request like any other. To take one, merge it locally as usual, or close it.
