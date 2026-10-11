# food

Food (P15-01): one shopping list grouped by aisle, with an optional store per
item, the stores, the aisle rules that place new items, and the pantry (what is
at home and when it expires). It owns its database, `food_db`. See
[docs/services/food.md](../../docs/services/food.md).

## Layout

```text
api/openapi.yaml          # the Food API (/v1/shopping-items, /v1/stores, /v1/aisle-rules, /v1/pantry-items)
cmd/server/               # the service
db/migrations/            # food_db, with the built-in aisle rules
db/queries/               # sqlc input
internal/domain/          # name normalisation, aisles, aisle rule matching
internal/api/             # handlers, idempotent writes, Activity events
internal/testdb/          # a migrated throwaway schema for integration tests
```

## Commands

```sh
make generate                     # sqlc and the OpenAPI server code
make test                         # unit tests; integration tests need FOOD_TEST_DATABASE_URL
make migrate-up DATABASE_URL='postgres://...'
```

The integration tests run against a throwaway Postgres named by
`FOOD_TEST_DATABASE_URL`, each in its own schema.
