# Getting started and testing

Install Docker Compose v2, Go 1.27.1+, Node 22+, `sqlc`, `oapi-codegen`, and `migrate`. Copy `.env.example` to ignored `.env`, then run:

```sh
make life-up
make life-smoke
make verify-isolation
make check
```

`make life-up` builds/starts all containers. `make infra-down` preserves data; `make infra-reset` removes persistent Compose volumes and is destructive. Use `docker compose ps` and `docker compose logs <service>` for diagnosis.

`make life-smoke` writes a few small records to the running stack; like every `tools/` script, it sends `X-Life-OS-Actor: dev:<script>`, so the Event Log shows them as a developer tool, never as the owner. Any write a development tool or agent makes against live data must do the same; see [Development tools and live data](../../CONTRIBUTING.md#development-tools-and-live-data).

`make verify-clean-db` and `make e2e-smoke` use the explicitly guarded
`lifeos-verify` project. Its PostgreSQL, Voice-audio, Whisper-model, network,
and container resources are separate from the running `lifeos` project and are
destroyed after the command. `make verify-isolation` additionally places
temporary sentinels in the normal runtime volumes and proves they survive the
complete disposable lifecycle. These commands are safe to run while the normal
stack is deployed; they never use normal runtime volume names. They still
create disposable test records, so do not point other tooling at port 13001
while they run.

`make browser-e2e` and `make processor-codex-actions-smoke` additionally use
the processor container's read-only bind of the already authenticated local
Codex session. They execute `codex exec` from that same runtime context; no API
key or session material is copied into the repository. See
[Phase 7 runtime verification](../verification/phase-7.md) for the exact
browser, Codex, retry, and recovery checks, and
[Phase 8 verification](../verification/phase-8.md) for the Phase 8 smokes and
the `make phase8-acceptance` sign-off run.

The `apps/dashboard/e2e/calendar-capture.spec.ts` and
`finance-capture.spec.ts` files are separate, deliberately opt-in tests for an
actual deployed gateway. The calendar test requires
`LIFE_OS_E2E_LIVE_CALENDAR=1`; the finance test additionally requires an
explicit `LIFE_OS_E2E_LIVE_FINANCE_ACCOUNT_ID` for a dedicated test account.
They create real records and should not be pointed at personal production data.

Create a service with `make create-service SERVICE=<kebab-name>`. A service whose writes are user-facing mounts its API behind `activity.RequireActor` (see `services/tasks/internal/api/routes.go`), so every write must name its actor. To add/change an endpoint: edit that service OpenAPI first, regenerate with `make check`, implement its adapter/use case/repository, add tests, expose a gateway route only when appropriate, and update these docs. Create migrations with `make db-migration-new SERVICE=<name> NAME=<name>`; they are service-owned, ordered, and tested against disposable stacks.

Ticket workflow: branch from primary, make small logical commits, run relevant end-of-ticket verification, update docs/Notion, merge, verify primary clean, then mark Done. See [data and code generation](data-and-codegen.md).
