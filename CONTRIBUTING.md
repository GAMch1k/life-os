# Contributing to Life OS

Contributions are welcome: bug reports, ideas, documentation, and code. This page explains the process. The technical details are in the pages it links to.

Everyone who takes part follows the [Code of Conduct](CODE_OF_CONDUCT.md). Security problems are never reported in public: see [SECURITY.md](SECURITY.md).

## Before you start

- **Bugs and ideas**: [open an issue](https://github.com/GAMch1k/life-os/issues/new/choose) with the matching form.
- **Bigger changes** (a new feature or service, a change to an API, the database, the deployment, or the architecture): open an issue first and describe what you want to change and how. Agreeing on the approach before you write the code saves you work.
- **Small fixes** (a typo, a broken link, an obvious bug) can go straight to a pull request.

## Pull requests

Contributions from outside are welcome.

1. Fork the repository, branch from `master`, and keep a pull request to one coherent change, with its documentation (see [Guidelines](#guidelines)).
2. Run the checks that cover your change: `tools/check` (or a part of it, such as `tools/check --go`), and `tools/test-integration` if you touched a service's database code. [Continuous integration](docs/operations/ci.md) lists what runs and how to run each part locally. Every check must pass on the pull request before it can be merged.
3. Sign the [Contributor License Agreement](CLA.md), once: comment on your pull request with exactly `I have read the CLA Document and I hereby sign the CLA`. You keep your copyright, your work stays available under the AGPL, and the maintainer may also offer it under other licenses. The `CLA` check comments on your pull request with what is missing and turns green once everyone who wrote a commit in it has signed. Commit with an email address that is linked to your GitHub account, so the check can tell who wrote each commit.
4. CI on a pull request from a fork may wait until the maintainer approves the run (GitHub asks for this for first-time contributors), so the checks may take a while to start. The `CLA` check runs at once.
5. The maintainer reviews the pull request and may ask for changes. Push them to the same branch; there is no need to squash or rebase. Once the checks pass, the maintainer merges it.

## Checks

These checks must pass before a pull request can be merged into `master`:

- **Gateway hosts, contracts, sqlc**: the gateway's host list, the OpenAPI lint, and that the generated API code, the sqlc code, and the release bundle's `deploy/compose.yaml` are up to date.
- **Go modules**: gofmt, `go vet`, the tests, and the build of every Go module, and the tests of `setup.sh`.
- **Dashboard**: Prettier, ESLint, the unit tests, the typecheck, and the production build.
- **Integration tests**: the services' database tests against a real PostgreSQL.
- **govulncheck**: known vulnerabilities in the Go code and its modules.
- **Full stack**: builds every image, then runs the smoke tests and the browser suite on a disposable stack. It starts once the generated-code, Go, Dashboard, and integration checks pass, and takes 14 to 19 minutes.
- **CLA**: everyone who wrote a commit has signed the [CLA](CLA.md).

To run them locally:

| Check | Command |
| --- | --- |
| Generated code, Go, `setup.sh`, Dashboard | `tools/check`, or one part: `--gateway`, `--contracts`, `--sqlc`, `--install-bundle`, `--go`, `--setup`, `--dashboard`. It needs a clean working tree. |
| Integration tests | `tools/test-integration`, against a throwaway PostgreSQL ([how](docs/operations/ci.md#integration-tests)) |
| govulncheck | `tools/check-vulnerabilities` |
| The CLA check's own tests | `node --test .github/cla/cla.test.js` |
| Full stack | Only on a machine that runs no Life OS of its own ([how](docs/operations/ci.md#run-it-again-or-elsewhere)) |

`tools/check` names any tool it is missing. The versions CI uses are in [Continuous integration](docs/operations/ci.md#tool-versions).

## Guidelines

- **Small, focused commits** with messages that say what changed and why.
- **No secrets or real data.** Never commit a `.env` file, passwords, tokens, keys, an AI sign-in, or anyone's personal data (real bank transactions, calendars, notes). Tests and examples use made-up data. Remove secrets and personal data from logs before you paste them anywhere.
- **Generated code** is never edited by hand: change the OpenAPI document or the SQL queries, and generate again ([data and code generation](docs/development/data-and-codegen.md)). The checks fail when generated code is out of date.

Documentation is part of the definition of done. Any change to architecture, API contracts, configuration, networking, persistence, deployment/operations, or user-visible behavior must update the relevant English Markdown page under [`docs/`](docs/README.md) in the same branch. Keep links relative and readable in GitHub and Obsidian. Generated OpenAPI output supplements, never replaces, the human explanation.

Services own their own database, migrations, and domain types. Do not access another service's tables; use APIs. Do not publish individual service ports, modify host Nginx/firewall/DNS, commit secrets, or run destructive data operations without explicit authorization.

## How changes ship

1. A pull request is checked by CI, reviewed, and merged into `master` by the maintainer.
2. From time to time the maintainer tags a version of `master`, such as `v0.1.0-preview.2`.
3. The tag starts the release workflow: it builds every image, runs the full-stack tests against those images, scans them for vulnerabilities, and only then publishes them to `ghcr.io/gamch1k`, signed and with build provenance and an SBOM. It ends with a GitHub Release that carries the install bundle.

[Releases](docs/operations/releases.md) explains versions, the release workflow, and how to verify an image. The maintainer's own changes pass the same checks: they work one ticket per branch, merge into `master` locally once it is healthy, and push.

## Development tools and live data

Development tools and agents (Claude Code and its subagents, scripts, test runners) are neither the owner nor the Life OS AI, and the Event Log must never show their work as either. Every service write must say who makes it in `X-Life-OS-Actor`; a write without one is rejected with `400 actor_required` (see [Activity](docs/services/activity.md#the-actor-gate)).

- Any write a development tool or agent makes against the live stack sends `X-Life-OS-Actor: dev:<tool>`. Claude Code and its subagents use `dev:claude-code`, for example `curl -H 'X-Life-OS-Actor: dev:claude-code' -H 'content-type: application/json' -d '{...}' http://127.0.0.1:3000/api/tasks/v1/tasks`. The Event Log then shows "Developer tool (claude-code)".
- Never send `user` from a development tool: that is the owner's own actor, used by the Dashboard. `agent:` and `system:` are internal and the gateway drops them.
- `tools/` scripts send `dev:<script name>`; Playwright specs that write through the `request` fixture import `test` from `apps/dashboard/e2e/fixtures.ts`, which sends `dev:e2e`, while the browser keeps the Dashboard's `user`.
- Acceptance, smoke, and benchmark runs belong on the disposable `lifeos-verify` stack (`tools/verify-compose`), not on live data. Touch live data only when a task requires it, and then only with a `dev:` actor.

## Dashboard UI components

The Dashboard uses [shadcn/ui](https://ui.shadcn.com/docs/components) (Base UI flavour) and reuses what it offers instead of building controls by hand. Check the component list first and add components only with the CLI, from `apps/dashboard`: `npx shadcn add <name>`, then format with Prettier. Never write or edit files in `apps/dashboard/src/components/ui/` by hand, and never copy shadcn code elsewhere; put Life OS styling and behaviour in wrapper components (such as `src/components/date-picker.tsx`). `npx shadcn add <name> --diff` shows whether a file still matches the CLI. See [Dashboard](docs/frontend/dashboard.md).
