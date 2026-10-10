# Banking service

The bank integration. Banking connects the owner's bank accounts through
[Enable Banking](https://enablebanking.com/docs/), a licensed PSD2 provider,
with read-only account information consent (never payments). It owns
everything bank-side: the provider adapter, the consent and the sealed
provider session, the sync schedule within the banks' daily limits, and the
consent reminders. It pushes accounts and transactions into Finance only
through Finance's bank import endpoint (`POST /v1/imports/bank`, and
`DELETE /v1/imports/bank/{connectionId}` on disconnect). It never reads
Finance's database, and services never read `banking_db`.

See [`docs/services/banking.md`](../../docs/services/banking.md) for the
design, the API, and the sync rules, and
[`docs/operations/banking.md`](../../docs/operations/banking.md) for setup,
reconnecting, and revoking.

## Layout

```text
api/openapi.yaml               # Banking API contract (source of truth)
cmd/server/                    # process entrypoint: HTTP API and the scheduler
db/migrations/                 # bank_connections and bank_auth_states
db/queries/                    # sqlc input
internal/api/                  # HTTP adapter
internal/banking/              # connect, sync, consent, disconnect; mapping to Finance
internal/enablebanking/        # Enable Banking client (JWT, PSU headers, errors)
internal/enablebanking/fakebank/  # in-memory Enable Banking API for tests
internal/finance/              # Finance bank import client (mirrors Finance's contract)
internal/finance/fakefinance/  # Finance stand-in for tests
internal/testdb/, internal/testsupport/  # integration test wiring
```

## Tests

Unit tests need nothing. Integration tests run against the throwaway Postgres
named by `BANKING_TEST_DATABASE_URL` (each test creates and drops its own
schema) and skip when it is unset. They never call the real Enable Banking
API: `fakebank` checks each request's JWT against a test key.

```sh
BANKING_TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:55441/postgres?sslmode=disable' \
  GOWORK=off go test -count=1 ./...
```
