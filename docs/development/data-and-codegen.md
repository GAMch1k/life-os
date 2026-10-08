# Data, migrations, and code generation

`infra/postgres/service-databases.conf` provisions one database/login per domain. Migrations are six-digit reversible files in `services/<name>/db/migrations`; sqlc inputs are local queries and generated pgx code is committed. Normal stop/start preserves the PostgreSQL volume.

`make check` requires a clean worktree, lints OpenAPI, runs `oapi-codegen`, generates dashboard TypeScript with `openapi-typescript`, runs sqlc, and fails on drift before Go/dashboard checks. Go build outputs go to a temporary directory and the command verifies the worktree remains clean after completion. Use `make verify-clean-db` and `make e2e-smoke` for guarded disposable database coverage; use `make verify-isolation` to prove normal runtime volumes survive that lifecycle. Do not place credentials in documentation or Git.
