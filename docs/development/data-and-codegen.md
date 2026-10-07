# Data, migrations, and code generation

`infra/postgres/service-databases.conf` provisions one database/login per domain. Migrations are six-digit reversible files in `services/<name>/db/migrations`; sqlc inputs are local queries and generated pgx code is committed. Normal stop/start preserves the PostgreSQL volume.

`make check` lints OpenAPI, runs `oapi-codegen`, generates dashboard TypeScript with `openapi-typescript`, runs sqlc, and fails on drift before Go/dashboard checks. Use `make verify-clean-db` and `make e2e-smoke` for disposable database coverage. Do not place credentials in documentation or Git.
