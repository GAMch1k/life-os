# Continuous integration

GitHub Actions checks every push and pull request with `.github/workflows/ci.yml`: the source, then the full stack (the images, the smokes, and the browser suite on the disposable verification stack). CI never deploys. Merges are still made locally and pushed to master. A second workflow, `.github/workflows/cla.yml`, checks that the authors of a pull request have signed the [Contributor License Agreement](#cla-check).

## When it runs

- **Every push** to any branch. Dependabot's branches (`dependabot/**`) run only as their pull request.
- **Every pull request** to master, also from forks.
- **By hand**: Actions → CI → Run workflow.

A newer push to a branch or pull request cancels that ref's older run. Every push to master runs to the end.

Runs use GitHub-hosted runners (`ubuntu-24.04`) and the workflow's token is read-only (`permissions: contents: read`). No job needs a secret, and none signs in to an AI. The workflow has no `pull_request_target` or `workflow_run` trigger, so a fork's pull request runs with a read-only token and no secrets. The CLA check is the one workflow that uses `pull_request_target`, and it never runs a pull request's code ([why that is safe](#why-pull_request_target-is-safe-here)).

## Jobs

The first six jobs run in parallel. `full-stack` waits for the four that block, as a later job can: `needs: [check-generated, check-go, check-dashboard, integration]`.

| Job | What it runs | Time | Locally |
| --- | --- | --- | --- |
| `check-generated` | `tools/check --gateway --contracts --sqlc --install-bundle`: the gateway's host list, the OpenAPI lint, generated Go and TypeScript API drift, sqlc drift, and the release bundle's `deploy/compose.yaml` ([releases](releases.md#the-install-bundle)) | about 1 min | `tools/check --gateway --contracts --sqlc --install-bundle` |
| `check-go` | `tools/check --go --setup`: gofmt, `go vet`, `go test`, and the build of every module under `pkg/`, `services/`, and `templates/` (`GOWORK=off`); then shellcheck and `tools/test-setup`, the tests of the install bundle's `setup.sh` with `docker` and `curl` stubbed (about 15 s) | about 1 min | `tools/check --go --setup` |
| `check-dashboard` | `tools/check --dashboard`: Prettier, ESLint, the unit tests, the typecheck, and `next build` | about 1.5 min | `tools/check --dashboard` |
| `integration` | `tools/test-integration` against a PostgreSQL service container | about 2 min | [Integration tests](#integration-tests) |
| `check-cla` | `node --test .github/cla/cla.test.js`: the [CLA check](#cla-check)'s logic against a fake GitHub API | seconds | `node --test .github/cla/cla.test.js` (Node 22, nothing to install) |
| `govulncheck` | `tools/check-vulnerabilities`: govulncheck on every Go module. A finding fails the run. | about 2 min | `tools/check-vulnerabilities` |
| `full-stack` | The images, then the smokes and the browser suite (without the `@codex` tests) on the verification stack | 14 to 19 min: 6.5 to 10 to build, 7 to 8 to test | [Full-stack tests](#full-stack-tests) |

The times are with warm caches; without them, each job takes up to a minute longer. The checks take about as long as the slowest of them, about 2 minutes; with `full-stack` after them, a whole run takes 16 to 21 minutes.

`tools/check` without flags runs all of its parts, as before. Like `make check`, it needs a clean working tree. `check-generated` and `check-dashboard` run `npm ci --prefix apps/dashboard` first.

### Tool versions

CI installs the versions the repository already uses:

- **Go** from `go.work` (`go 1.27.3`). Changing Go means `go.work`, every `go.mod`, and the Dockerfiles' `golang:` images.
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

In CI, the job's PostgreSQL is a service container of the image `compose.yaml` deploys (`LIFE_OS_POSTGRES_IMAGE`'s default, `postgres:18.6-alpine` pinned by digest). Its first step fails if the two differ. After a PostgreSQL upgrade or a new digest ([Dependabot](#dependabot)), change `services.postgres.image` in `ci.yml` to match.

## Full-stack tests

The `full-stack` job runs the images, the smokes, and the browser suite on the disposable verification stack (`tools/verify-compose`, project `lifeos-verify`, the gateway on `127.0.0.1:13001`). Its steps are a composite action, `.github/actions/full-stack`, which the [release](releases.md) runs too before it scans and publishes the images it built; composite steps have no `timeout-minutes`, so `timeout` bounds each long one (30 minutes for the build and the browser suite, 10 for each smoke). It starts once `check-generated`, `check-go`, `check-dashboard`, and `integration` have passed, on the same pushes and pull requests, on a GitHub-hosted runner (`ubuntu-24.04`: 4 CPUs, 16 GB of memory). It needs no secret and signs in to no AI.

In order:

1. **Unknown service names answered at once.** The runner's systemd-resolved answers SERVFAIL for a single-label name it does not know, such as a service that is not running, and the Alpine images' C library (musl) waits 10 s on SERVFAIL before it gives up. Home and Status probe services that are not running here (the processor, and the monitor, backup, and host-health, which `make life-smoke` does not start), so the Dashboard's DNS lookups queued behind those waits until its requests to running services timed out too: Home showed no Task. `ResolveUnicastSingleLabel=yes` sends such names to the upstream DNS, which answers NXDOMAIN at once, as on a usual host.
2. **A throwaway `.env`**: `tools/ci-env "$RUNNER_TEMP/lifeos-ci"` writes `.env.example` with a new random password for every database login, `LIFE_OS_TIMEZONE=Europe/Berlin` (the browser tests' zone), every integration unset (Google, Enable Banking, ntfy, Cloudflare, web push, the ZAW feed; `tools/verify-compose` refuses them anyway), the backup directory under the scratch directory, and the runner's user as the owner of the backup directory, the Codex home, and the bank key. The Codex home is an empty directory. The file lives in the job's checkout only: Docker builds leave it out (`.dockerignore`), and nothing uploads it. `tools/ci-env` refuses to replace an existing `.env`.
3. **The Whisper model** (`ggml-small.bin`, 466 MB, the `.env.example` default) from the Actions cache, under a key made of its file name and URL; on a miss it is downloaded from Hugging Face and saved at once, so a failed run keeps it too. `LIFE_OS_VERIFY_OVERLAYS=tools/fixtures/whisper-models.compose.yaml` mounts it read-only into the verification stack's Whisper, which would otherwise download it into a fresh volume at every start (each smoke starts its own stack).
4. **The images**: `docker compose build --pull`, all of them at once, as `make life-up` would.
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
| `docker compose build --pull` | 6.5 to 10 min. whisper.cpp's compile (`-j2`) finishes last; the 18 Go builds and the Dashboard's `npm ci` share the 4 CPUs with it |
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
docker compose build --pull
npm ci --prefix apps/dashboard && (cd apps/dashboard && npx playwright install --with-deps chromium firefox)
make life-smoke && tools/smoke-e2e && make verify-clean-db && tools/smoke-action-idempotency \
  && tools/smoke-gateway-recreate && tools/smoke-voice-lease-recovery
tools/browser-e2e --grep-invert @codex
```

Without `LIFE_OS_VERIFY_OVERLAYS`, Whisper downloads the model itself at each start. If Home misses its Tasks there too, check how the machine answers an unknown name from a container (`docker network create t && docker run --rm --network t alpine:3.22 getent hosts nosuchservice; docker network rm t`): 10 s is the SERVFAIL wait above, and the job's `ResolveUnicastSingleLabel=yes` fixes it on systemd-resolved.

### Not cached: the images

The images are built from scratch in every run. The Go services' Dockerfiles copy the whole repository before `go build`, so any commit changes that layer and a build cache (buildx with `type=gha`) could reuse none of their work. It could reuse only whisper.cpp's build and the Dashboard's `npm ci`, a few minutes of the build, at the cost of a second builder and of loading every image into Docker.

## govulncheck

`tools/check-vulnerabilities` runs govulncheck on every Go module, the fixtures generator `tools/fixtures/documents` included. It lists the findings in the code's call paths, with their IDs per module at the end.

The job is blocking: a finding fails the run. govulncheck checks against today's vulnerability database, so a newly published advisory can turn CI red on any branch, even one that changed nothing. Fix it promptly: update Go (`go.work`, every `go.mod`, and the Dockerfiles' `golang:` images) or the module (`GOWORK=off go get <module>@<fixed version>` and `go mod tidy` in every module that uses it), then rebuild and redeploy the Go services. Dependabot's weekly pull requests may fix it too, but don't wait for them.

When the job was added (9 November 2026), every module except the fixtures generator had findings. Go 1.27.3, `golang.org/x/net` v0.62.0 (with `golang.org/x/text` v0.43.0), `github.com/klauspost/compress` v1.20.2, and `github.com/andybalholm/brotli` v1.2.7 fixed them the same day:

| Where | Findings | Fixed in |
| --- | --- | --- |
| Go 1.27.1's standard library | GO-2026-6605, GO-2026-6607, GO-2026-6608, GO-2026-6613, and the bundled HTTP/2's GO-2026-6603, -6610, -6611, -6612, -6617 | Go 1.27.2 |
| Go 1.27.1's standard library | GO-2026-6809 (`crypto/x509`) | Go 1.27.3 |
| `golang.org/x/net` v0.57.0 | GO-2026-6603, -6610, -6611, -6612, -6617 (HTTP/2) | v0.60.0 |
| `golang.org/x/net` v0.57.0 (recipe-import) | GO-2026-6860 (`html`) | v0.62.0 |
| `golang.org/x/text` v0.40.0 | GO-2026-6629 | v0.41.0 |
| `github.com/klauspost/compress` v1.19.2 | GO-2026-6865 | v1.20.2 |
| `github.com/andybalholm/brotli` v1.2.2 | GO-2026-6888 | v1.2.7 |

govulncheck also notes GO-2026-5932 in `golang.org/x/crypto` (its unmaintained `openpgp` package). No module imports that package, so it is not a finding.

## CLA check

`.github/workflows/cla.yml` checks that everyone who wrote a commit in a pull request has signed the [Contributor License Agreement](../../CLA.md) (X-13; a CLA rather than a DCO, so that Life OS can also be offered under other licenses). Its result is the commit status **`CLA`** on the pull request's head commit, which the default branch's ruleset requires. The logic is `.github/cla/cla.js`, run by `actions/github-script`. No third-party CLA action is used: the usual one, contributor-assistant/github-action, was archived in March 2026. life-os-app has the same workflow and the same files.

### What it does

- It runs when a pull request is opened, reopened, or pushed to (`pull_request_target`), and on a new comment on a pull request that is a signature or `recheck` (`issue_comment`). Edited and deleted comments, and comments on issues, start nothing.
- Who must sign: the pull request's author, and every commit's author and committer that is a GitHub account. Exempt: `GAMch1k`, GitHub's own committer `web-flow` (commits made on github.com), and every bot (a login ending in `[bot]`, such as `dependabot[bot]`).
- A commit whose author email is linked to no GitHub account fails the check, since nobody can sign for it; the comment asks its author to link the email or rewrite the commits. A pull request of more than 250 commits fails too: the API lists no more.
- To sign, the contributor comments exactly `I have read the CLA Document and I hereby sign the CLA` (the sentence the archived action used; whitespace at the ends and line breaks don't matter, case does). A comment signs only for its own author. Every run reads all of the pull request's comments, so a signature that a cancelled or failed run missed is recorded by the next one.
- When something is missing, the workflow posts one comment that names who still has to sign, with the sentence and a link to `CLA.md` on the default branch, and edits that same comment (found again by its hidden marker `<!-- life-os-cla -->`) as things change, down to a thank-you once everyone has signed. A pull request that needs nothing, such as the maintainer's or Dependabot's, gets the status and no comment.
- The status is `success` or `failure`, and its link opens the comment. A run that ends in an error sets no status, so the required check stays missing and nothing merges.
- The runs of one pull request wait for each other (`concurrency`), so an older result never overwrites a newer one. The job is called `Check signatures`, so that its check run is not a second `CLA` on the commit.

### Why `pull_request_target` is safe here

`pull_request_target` gives a fork's pull request a token that can write, so running the pull request's code with it would hand that token over; `ci.yml` never uses it. The CLA workflow is the one exception, and it is safe only because it never checks out, builds, or runs the pull request's code:

- `actions/checkout` has no `ref`, so it checks out the commit the event names, which for `pull_request_target` and `issue_comment` is the base or default branch, never the pull request's head. It fetches only `.github/cla`, to load `cla.js`.
- `cla.js` reads the pull request through the API only (its commits' authors and its comments) and writes only the `CLA` status, its own comment, and the `cla-signatures` branch.
- There is no `run:` step, and no expression hands the pull request's title, body, branch name, or comments to a script.
- The top-level `permissions: {}` grants nothing; the job gets `contents: write` (the signatures branch), `pull-requests: write` and `issues: write` (the comment), and `statuses: write`.

`cla.test.js` fails if the workflow gains a `ref:`, a `run:` step, a reference to the pull request's head, or an action that is not an official one pinned to a commit SHA. Changes to `cla.yml` or `cla.js` apply to pull requests only once they are on master, because both events run the default branch's copy; a pull request that changes them is tested by `check-cla`, never by running its own copy.

Since it runs from the base branch, `pull_request_target` needs no approval: a fork's first pull request gets its `CLA` status at once, while `ci.yml` waits until the maintainer approves the run.

### Signatures

The signatures are in `signatures/cla.json` on the branch `cla-signatures`. The workflow creates it at the first signature, as an orphan branch (no history of the code, only that file and a README). The ruleset protects only the default branch, so the workflow's token can push there; a push with that token starts no workflow, and the branch has none anyway. A record:

```json
{
  "login": "octocat",
  "id": 583231,
  "version": "1.0",
  "commentUrl": "https://github.com/GAMch1k/life-os/pull/1#issuecomment-1234567890",
  "pullRequest": 1,
  "repository": "GAMch1k/life-os",
  "signedAt": "2026-11-10T12:34:56.000Z"
}
```

`id` is the GitHub user's numeric ID, which the check compares: a login can be renamed, the ID stays. `version` is the version of `CLA.md` they signed, `signedAt` the time of their comment, in UTC. When two runs write at once, the contents API refuses the second write (409, or 422 when the first run has just created the file or the branch), and that run reads the file again and appends to it, up to five times. Each repository keeps its own record: life-os-app's is on its own `cla-signatures` branch.

Don't delete or force-push the branch. To remove a signature recorded by mistake, edit the file on the branch by hand, then comment `recheck` on the pull requests concerned.

### Allowlist, recheck, new versions

- **Allowlist**: add the login to `CONFIG.allowlist` in `.github/cla/cla.js` (compared case-insensitively), in both repositories, and merge. Bots need no entry. It is meant for accounts that need to grant no license, such as another account of the maintainer; everyone else signs.
- **Recheck**: comment `recheck` on the pull request (anyone can), or push to it. A pull request opened before the workflow was on master gets its first status this way.
- **A new version of the CLA**: change `CLA.md` (its "Version" line) and `CONFIG.version` together; `cla.test.js` checks that they match. Earlier signatures keep counting (`CLA.md`, section 13). Making everyone sign again would need the check to compare versions, which it doesn't today.

## Dependabot

`.github/dependabot.yml` asks for version updates every Monday at 06:00 (Europe/Berlin). Dependabot reads it from master, so it starts once the file is merged there:

- **Go modules** in `pkg/`, every `services/*` and `templates/*` module, and `tools/fixtures/documents`.
- **npm** in `apps/dashboard`.
- **Docker base images** of every Dockerfile (`apps/dashboard`, `infra/*`: the gateway's nginx, the provisioning's and the migrations' Alpine and Go, `services/*`, `services/voice/whisper`, `templates/*`).
- **Docker Compose images**: PostgreSQL, cloudflared, and ntfy in `compose.yaml` (the `${VAR:-default}` defaults too), and the browser tests' fixture nginx in `tools/fixtures`.
- **GitHub Actions** in the workflows and in the composite action `.github/actions/full-stack`: the pinned SHA and its version comment together.

Each ecosystem's updates come as one grouped pull request a week; all action updates come as one pull request. At most 5 pull requests are open at once for Go and npm, and 3 for Docker, Docker Compose, and Actions.

**No major versions (the owner's decision, 2026-11-10).** Every ecosystem ignores semver-major version updates: a major upgrade (Node 24, PostgreSQL 19, a new Go module major) is a ticket of its own, done on purpose. Security updates still come, majors included, as Dependabot security pull requests. Calendar-versioned images move a "major" at the turn of the year (cloudflared 2026.x to 2027.x), so that update is done by hand too. The gateway's nginx stays on its stable branch: Dependabot refreshes `1.30-alpine` but proposes no newer minor (1.31 is nginx's mainline); moving to the next stable branch (1.32) is a ticket as well.

**Images are pinned by digest** (`name:tag@sha256:…`, the multi-platform index): every Dockerfile's `FROM` and every image in `compose.yaml`. A build or deployment therefore uses exactly the image the repository names, and when upstream rebuilds a tag (an Alpine or OpenSSL fix under the same `nginx:1.30-alpine`), Dependabot's pull request moves the digest, and CI tests it before it reaches the server. Builds add `--pull` (`docker compose build --pull`, the Makefile, CI), so an image named by tag alone, such as `LIFE_OS_POSTGRES_IMAGE` set in `.env`, is fetched fresh rather than taken from an old local copy. Two places Dependabot does not edit, kept equal by hand: `services/backup/Dockerfile`'s `POSTGRES_IMAGE` default (compose passes its own) and the integration job's PostgreSQL in `ci.yml`. A Dependabot pull request that moves PostgreSQL fails that job's first step until `services.postgres.image` in `ci.yml` is changed to match on its branch.

CI checks each Dependabot pull request like any other. To take one, merge it locally as usual, or close it.
