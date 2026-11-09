# Continuous integration

GitHub Actions checks every push and pull request with `.github/workflows/ci.yml`. CI checks the source; it never deploys. Merges are still made locally and pushed to master. The full-stack tests (images, the verification stack, smokes, Playwright) are not part of it yet.

## When it runs

- **Every push** to any branch. Dependabot's branches (`dependabot/**`) run only as their pull request.
- **Every pull request** to master, also from forks.
- **By hand**: Actions → CI → Run workflow.

A newer push to a branch or pull request cancels that ref's older run. Every push to master runs to the end.

Runs use GitHub-hosted runners (`ubuntu-24.04`) and the workflow's token is read-only (`permissions: contents: read`). No job needs a secret. The workflow has no `pull_request_target` or `workflow_run` trigger, so a fork's pull request runs with a read-only token and no secrets.

## Jobs

The five jobs run in parallel. A later job can wait for them with `needs: [check-generated, check-go, check-dashboard, integration]`.

| Job | What it runs | Time | Locally |
| --- | --- | --- | --- |
| `check-generated` | `tools/check --gateway --contracts --sqlc`: the gateway's host list, the OpenAPI lint, generated Go and TypeScript API drift, and sqlc drift | about 1 min | `tools/check --gateway --contracts --sqlc` |
| `check-go` | `tools/check --go`: gofmt, `go vet`, `go test`, and the build of every module under `pkg/`, `services/`, and `templates/` (`GOWORK=off`) | about 1 min | `tools/check --go` |
| `check-dashboard` | `tools/check --dashboard`: Prettier, ESLint, the unit tests, the typecheck, and `next build` | about 1.5 min | `tools/check --dashboard` |
| `integration` | `tools/test-integration` against a PostgreSQL service container | about 2 min | [Integration tests](#integration-tests) |
| `govulncheck` | `tools/check-vulnerabilities`: govulncheck on every Go module. **Not blocking yet.** | about 2 min | `tools/check-vulnerabilities` |

The times are with warm caches; without them, each job takes up to a minute longer. A whole run takes about as long as its slowest job, about 2 minutes.

`tools/check` without flags runs all of its parts, as before. Like `make check`, it needs a clean working tree. `check-generated` and `check-dashboard` run `npm ci --prefix apps/dashboard` first.

### Tool versions

CI installs the versions the repository already uses:

- **Go** from `go.work` (`go 1.27.3`). Changing Go means `go.work`, every `go.mod`, and the Dockerfiles' `golang:` images.
- **Node 22**, as the Dashboard's image (`node:22-alpine`).
- **sqlc 1.31.1**, the release binary, checked against the SHA-256 in the workflow's `SQLC_SHA256`. The version is the generated code's `sqlc v1.31.1` header (see `pkg/database/README.md`).
- **oapi-codegen v2.8.0**, as `tools/check-contracts` requires.
- **govulncheck v1.9.0**, pinned in `tools/check-vulnerabilities`.

Every action is pinned to a full commit SHA, with its version in a comment. Dependabot keeps both up to date.

Each Go job caches the module and build caches under its own key (the Go version and the hash of every `go.sum`). The npm download cache and Next.js's build cache (`.next/cache`) are cached too.

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
