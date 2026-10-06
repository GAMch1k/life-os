# PostgreSQL service conventions

`pkg/database` contains connection infrastructure only. Every service owns its
schema and generated query code inside that service module:

```text
services/<service>/
├── db/
│   ├── migrations/
│   │   ├── 000001_create_<resource>.up.sql
│   │   └── 000001_create_<resource>.down.sql
│   └── queries/
│       └── <resource>.sql
├── sqlc.yaml
└── internal/repository/
    └── generated/                 # committed sqlc output
```

Migration versions are six-digit, monotonically increasing sequence numbers.
Every change has one `*.up.sql` and one reversible `*.down.sql`; names use
lowercase snake case and describe one schema change. Queries are grouped by
resource, use sqlc's `-- name: VerbResource :one|:many|:exec` annotation, and
use parameters rather than interpolated SQL. Generated code is never edited by
hand. Domain tables, migrations, and queries must not be added to `pkg`.

Each service's `sqlc.yaml` points `schema` at `db/migrations`,
`queries` at `db/queries`, and emits pgx/v5 code to
`internal/repository/generated`. The neutral executable example under
`testdata/example` is a convention test fixture, not an application schema.

## Commands

Install the pinned tools on `PATH`, then use the root targets. The migrate CLI
is built with PostgreSQL support. `DATABASE_URL` must identify only the selected
service's database.

```sh
go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
go install -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@v4.20.1
```

```sh
make db-migration-new SERVICE=tasks NAME=create_items
make db-generate SERVICE=tasks
make db-migrate-up SERVICE=tasks DATABASE_URL='postgres://...'
make db-migrate-down SERVICE=tasks DATABASE_URL='postgres://...'
make db-test SERVICE=tasks
```

`db-migrate-down` rolls back exactly one migration. Review its down file and
take any required backup before running it outside a disposable environment.
The Make targets reject a missing service directory, migrations directory,
configuration, or database URL before invoking a tool.

At runtime, open and verify a pool, then register it with the HTTP lifecycle:

```go
pool, err := database.Open(ctx, database.Config{
    URL:             cfg.DatabaseURL,
    ApplicationName: "tasks",
})
if err != nil {
    return err
}
app.OnShutdown(database.ClosePool(pool))
```

Use native `pgx.BeginFunc` or `pgx.BeginTxFunc` for transactions. A shared
transaction wrapper would add no safety or behavior beyond pgx and is therefore
intentionally omitted.

The fixture can be regenerated with
`sqlc generate -f pkg/database/testdata/example/sqlc.yaml`. Its integration
test requires a freshly migrated, disposable database in `DATABASE_TEST_URL`;
normal `go test ./...` skips only that external-database test.
