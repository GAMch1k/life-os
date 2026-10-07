# Phase 0–3 functional verification

This report records the active verification of the pre-AI Life OS baseline.
It complements automated checks with tests against a running Docker Compose
stack.

## Environment

- Go 1.27.1, Node 22.19.0, npm 11.21.0, sqlc 1.31.1, Docker Compose 5.1.3
- Dashboard published on the configured WireGuard address
- Core service ports used only for this Phase 4 local verification

## Results

| Area | Result | Evidence |
| --- | --- | --- |
| Contract and generated-code checks | Pass | `make check` validates OpenAPI, regenerates Go and TypeScript artifacts, runs sqlc and Go/dashboard checks. |
| Clean database and migrations | Pass | `make verify-clean-db` creates an isolated network and disposable `lifeos-verify-postgres-data` volume, applies all four migrations, verifies database isolation, then removes the verification stack. |
| Service health and readiness | Pass | `/health` and `/ready` returned success for Finance, Tasks, Inbox, and Calendar. |
| Tasks HTTP workflow | Pass | Created, updated, retrieved, and completed a task. |
| Inbox HTTP workflow | Pass | Captured an item, retrieved it, and resolved it. |
| Calendar HTTP workflow | Pass | Created, updated, retrieved, and deleted an all-day event. |
| Finance read workflow | Pass | Listed accounts/categories and retrieved the current monthly summary. CSV behavior is covered by P4-02. |
| Validation | Mixed | Inbox, Calendar, and Finance invalid requests return 400. Tasks accepts `limit=0` despite the OpenAPI minimum of 1; tracked for P4-03. |
| Dashboard against live services | Pass | `tools/smoke-dashboard` creates live data and confirms Finance, Task, Inbox, and Calendar content renders. |
| Dashboard browser API proxy | Pass | Successful task completion and Inbox capture were exercised through the WireGuard-published dashboard `/api/*` routes. |
| Restart and persistence | Pass | Completed Tasks and discarded Inbox items remained in their expected state after their containers restarted; the dashboard served successfully after recovery. |
| Database isolation | Pass | `make db-check-isolation` passed for all four service databases. |

## Regressions found

1. `GET /v1/tasks?limit=0` returns `200`, while the OpenAPI contract declares
   `limit` with a minimum of `1`. Reproduce with:

   ```sh
   curl -i 'http://127.0.0.1:8082/v1/tasks?limit=0'
   ```

2. When Finance is stopped, rendering the dashboard root returns `500` instead
   of showing its intended Finance-unavailable state. The service is restored
   immediately after the test. This violates the Phase 3 unavailable-service
   behavior and is tracked for P4-03.

## Verification command notes

`tools/smoke-dashboard` discovers the dashboard address with `docker compose
port dashboard 3000`. It therefore works for the default loopback binding and
for an explicitly configured WireGuard address, rather than assuming
`127.0.0.1`.

`make verify-clean-db` intentionally never reuses the normal Compose network
or PostgreSQL volume. It is safe to run while the normal development stack is
up; it removes only the explicitly named disposable verification resources.
