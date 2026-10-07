# Services and APIs

All services expose `/health` and `/ready`; through the gateway these are available below their domain prefix (for example `/api/tasks/ready`). Error responses follow the shared HTTP envelope. OpenAPI files in `services/*/api/openapi.yaml` remain the exact schema reference.

## Finance

Finance owns accounts, categories, transactions, CSV import records, and monthly summaries. It does not create tasks or manage calendar events. Gateway base: `/api/finance`.

| Method and path | Purpose |
| --- | --- |
| `GET /v1/accounts`, `GET /v1/categories` | List account/category reference data |
| `PATCH /v1/categories/{categoryId}` | Edit a category |
| `GET/POST /v1/transactions` | List or record transactions |
| `GET /v1/summaries/monthly` | Monthly totals and balance view |
| `POST /v1/imports/csv` | Validate and import a CSV |

## Tasks

Tasks owns actionable work, dates, priority, completion, and notes. It does not interpret Inbox captures. Gateway base: `/api/tasks`.

`GET/POST /v1/tasks` lists or creates work; `GET/PATCH /v1/tasks/{taskId}` retrieves/edits it; `POST /v1/tasks/{taskId}/complete` completes it. Invalid state or malformed data returns a client error; missing IDs return not found.

## Inbox

Inbox is a capture queue: it stores content and source, then records resolution or discard. It does not create a downstream task/event itself. Gateway base: `/api/inbox`.

`GET/POST /v1/inbox-items` lists/captures an item; `GET /v1/inbox-items/{itemId}` retrieves it; `/resolve` and `/discard` transition it. This boundary intentionally leaves future AI/MCP triage replaceable.

## Calendar

Calendar owns events and date/time/all-day semantics. Gateway base: `/api/calendar`. `GET/POST /v1/events`, `GET/PATCH/DELETE /v1/events/{eventId}` form its API. It does not synchronize external calendars yet.

For every endpoint, see the owning OpenAPI file for required JSON fields, response schema, validation, and status codes. Gateway clients prepend `/api/<domain>`; internal callers use the service's `/v1` root.
