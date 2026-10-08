# Getting started and testing

Install Docker Compose v2, Go 1.27.1+, Node 22+, `sqlc`, `oapi-codegen`, and `migrate`. Copy `.env.example` to ignored `.env`, then run:

```sh
make life-up
make life-smoke
make verify-isolation
make check
```

`make life-up` builds/starts all containers. `make infra-down` preserves data; `make infra-reset` removes persistent Compose volumes and is destructive. Use `docker compose ps` and `docker compose logs <service>` for diagnosis.

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
browser, Codex, retry, and recovery checks.

Create a service with `make create-service SERVICE=<kebab-name>`. To add/change an endpoint: edit that service OpenAPI first, regenerate with `make check`, implement its adapter/use case/repository, add tests, expose a gateway route only when appropriate, and update these docs. Create migrations with `make db-migration-new SERVICE=<name> NAME=<name>`; they are service-owned, ordered, and tested against disposable stacks.

Ticket workflow: branch from primary, make small logical commits, run relevant end-of-ticket verification, update docs/Notion, merge, verify primary clean, then mark Done. See [data and code generation](data-and-codegen.md).
