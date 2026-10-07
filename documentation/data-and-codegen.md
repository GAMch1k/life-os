# Data, migrations, and code generation

PostgreSQL provisioning declares one database/login per service in `infra/postgres/service-databases.conf`. Credentials belong in ignored `.env`, never source control. PostgreSQL persistence survives normal `make infra-down`/`make life-up` cycles through the named Compose volume.

Migrations are ordered six-digit files in each service's `db/migrations`; apply them with the service migration container in Compose or `make db-migrate-up SERVICE=<name> DATABASE_URL=...`. Add one with `make db-migration-new SERVICE=<name> NAME=<name>`. Keep rollback SQL meaningful and test a clean database with `make verify-clean-db`.

`sqlc` generates service-local pgx repository code from `db/queries`. `oapi-codegen` generates Go transport bindings, while the dashboard uses `openapi-typescript`. Run `make check` after changing an OpenAPI contract, query, or migration; generated output is committed and drift is a failure.
