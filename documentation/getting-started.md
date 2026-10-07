# Getting started

## Prerequisites

Install Docker Engine with Compose v2, Go 1.27.1+, Node 22+, `sqlc`, `oapi-codegen`, and `migrate`. Copy the environment template before starting:

```sh
cp .env.example .env
make life-up
make life-smoke
```

The default browser endpoint is `http://127.0.0.1:3000`. Set `LIFE_OS_GATEWAY_BIND_ADDRESS` and `LIFE_OS_GATEWAY_PORT` in `.env` for a different private-network listener; for example WireGuard uses `10.66.66.1`.

`make infra-down` stops containers and preserves data. `make infra-reset` intentionally removes the Compose volume and is destructive.

For the complete non-mutating quality gate run `make check`. It regenerates contracts/database code and rejects drift, then runs Go and dashboard checks.
