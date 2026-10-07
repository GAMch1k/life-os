# API overview

Internal service paths begin `/v1`; external/browser paths prepend the gateway base. Every service provides `GET /health` and `GET /ready`. Successful JSON is the entity/list schema in its OpenAPI file; malformed bodies, invalid UUID/date/query values, and invalid state return 400; an absent entity returns 404; unexpected failures return a safe 5xx error envelope.

| Owner (gateway base) | API |
| --- | --- |
| Finance (`/api/finance`) | `GET /v1/accounts`, `GET /v1/categories`, `PATCH /v1/categories/{id}`, `GET/POST /v1/transactions`, `GET /v1/summaries/monthly?month=YYYY-MM`, `POST /v1/imports/csv` |
| Tasks (`/api/tasks`) | `GET/POST /v1/tasks`, `GET/PATCH /v1/tasks/{id}`, `POST /v1/tasks/{id}/complete` |
| Inbox (`/api/inbox`) | `GET/POST /v1/inbox-items`, `GET /v1/inbox-items/{id}`, `POST /resolve`, `POST /discard` |
| Calendar (`/api/calendar`) | `GET/POST /v1/events`, `GET/PATCH/DELETE /v1/events/{id}` |

Examples:

```sh
curl -X POST http://127.0.0.1:3000/api/tasks/v1/tasks \
  -H 'content-type: application/json' -d '{"title":"Pay invoice"}'
curl -X POST http://127.0.0.1:3000/api/inbox/v1/inbox-items \
  -H 'content-type: application/json' -d '{"content":"Call insurer","source":"manual"}'
curl -X POST http://127.0.0.1:3000/api/calendar/v1/events \
  -H 'content-type: application/json' -d '{"title":"Holiday","allDay":true,"startDate":"2026-12-24","endDate":"2026-12-24"}'
```

Finance manual transaction bodies require `accountId`, `{amount,currency}`, and `occurredOn`; category is optional. CSV requests use `text/csv`. Task creation requires `title`; Calendar creation requires `title` and `allDay`; Inbox creation requires `content` and `source`. Exact request/response schemas, pagination, filtering, and validation are versioned in `services/*/api/openapi.yaml`.
