# Getting started and testing

Install Docker Compose v2, Go 1.27.1+, Node 22+, `sqlc`, `oapi-codegen`, and `migrate`. Copy `.env.example` to ignored `.env`, then run:

```sh
make life-up
make life-smoke
make e2e-smoke
make check
```

`make life-up` builds/starts all containers. `make infra-down` preserves data; `make infra-reset` removes persistent Compose volumes and is destructive. Use `docker compose ps` and `docker compose logs <service>` for diagnosis.

Create a service with `make create-service SERVICE=<kebab-name>`. To add/change an endpoint: edit that service OpenAPI first, regenerate with `make check`, implement its adapter/use case/repository, add tests, expose a gateway route only when appropriate, and update these docs. Create migrations with `make db-migration-new SERVICE=<name> NAME=<name>`; they are service-owned, ordered, and tested against disposable stacks.

Ticket workflow: branch from primary, make small logical commits, run relevant end-of-ticket verification, update docs/Notion, merge, verify primary clean, then mark Done. See [data and code generation](data-and-codegen.md).
