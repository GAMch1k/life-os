# API overview

Internal service paths begin `/v1`; external/browser paths prepend the gateway base. Every service provides `GET /health` and `GET /ready`. Successful JSON is the entity/list schema in its OpenAPI file; malformed bodies, invalid UUID/date/query values, and invalid state return 400; an absent entity returns 404; unexpected failures return a safe 5xx error envelope.

Every write (`POST`, `PUT`, `PATCH`, `DELETE`) to Finance, Tasks, Inbox, Calendar, Calendar feeds, Voice, or Notifications must say who makes it in the `X-Life-OS-Actor` header: `user` (the owner; the Dashboard sends it), `agent:<name>` (the Life OS AI), `system:<name>` (a Life OS component), or `dev:<name>` (a development tool or agent, for example `dev:claude-code`). Names are 1–64 letters, digits, `.`, `_`, or `-`. A write without a valid actor returns 400 with error code `actor_required`; nothing is recorded as the owner by default. Reads need no actor. Through the gateway only `user` and `dev:<name>` are accepted; `agent:`/`system:` claims are dropped. See [Activity](../services/activity.md#actors).

| Owner (gateway base) | API |
| --- | --- |
| Finance (`/api/finance`) | `GET /v1/accounts`, `GET /v1/categories`, `PATCH /v1/categories/{id}`, `GET/POST /v1/transactions`, `GET /v1/summaries/monthly?month=YYYY-MM`, `POST /v1/imports/csv` |
| Tasks (`/api/tasks`) | `GET/POST /v1/tasks`, `GET/PATCH /v1/tasks/{id}`, `POST /v1/tasks/{id}/complete` |
| Inbox (`/api/inbox`) | `GET/POST /v1/inbox-items`, `GET /v1/inbox-items/{id}`, `POST /resolve`, `POST /discard` |
| Calendar (`/api/calendar`) | `GET/POST /v1/events`, `GET /v1/events/search?q=…`, `GET/PATCH/DELETE /v1/events/{id}`, `GET /v1/sources`, `GET/PATCH/DELETE /v1/sources/{id}` (the owner's settings of a source); see [Calendar](../services/calendar.md) |
| Calendar feeds (`/api/integrations/calendar-feeds`) | `GET/POST /v1/feeds`, `GET/PATCH/DELETE /v1/feeds/{id}`, `POST /v1/feeds/{id}/refresh`: the ICS feeds (URL, filter, refresh state); see [Calendar feeds](../services/calendar-feeds.md) |
| Voice (`/api/voice`) | `POST /v1/audio-jobs` (multipart audio, returns 202), `GET /v1/audio-jobs/{id}` |
| Activity (`/api/activity`, read-only) | `GET /v1/activity-events` (filters: `correlationId`, `subjectType`/`subjectId`, `type`, `source`, `outcome`, `from`/`to`), `GET /v1/activity-events/{id}`; recording is internal only — see [Activity](../services/activity.md) |
| Notifications (`/api/notifications`) | `GET/POST /v1/notifications` (filters: `importance`, `category` prefix, `source`, `unread`; `dedupeKey` makes sending idempotent), `GET /v1/notifications/{id}`, `POST /v1/notifications/{id}/read`, `POST /v1/notifications/read-all`, `GET /v1/notifications/unread-count`; see [Notifications](../services/notifications.md) |
| Dashboard (`/api/status`) | `GET /api/status`: the Status page's checks as JSON, never cached; the [monitor](../services/monitor.md) polls it |
| MCP (`/api/mcp`) | Streamable HTTP endpoint at `/mcp`; health and readiness probes |

Examples, run as a development tool (`dev:curl`; never send `user` from a tool):

```sh
curl -X POST http://127.0.0.1:3000/api/tasks/v1/tasks -H 'X-Life-OS-Actor: dev:curl' \
  -H 'content-type: application/json' -d '{"title":"Pay invoice"}'
curl -X POST http://127.0.0.1:3000/api/inbox/v1/inbox-items -H 'X-Life-OS-Actor: dev:curl' \
  -H 'content-type: application/json' -d '{"content":"Call insurer","source":"manual"}'
curl -X POST http://127.0.0.1:3000/api/calendar/v1/events -H 'X-Life-OS-Actor: dev:curl' \
  -H 'content-type: application/json' -d '{"title":"Holiday","allDay":true,"startDate":"2026-12-24","endDate":"2026-12-24"}'
```

Finance manual transaction bodies require `accountId`, `{amount,currency}`, and `occurredOn`; category is optional. CSV requests use `text/csv`. Task creation requires `title`; Calendar creation requires `title` and `allDay` (plus `startAt`/`endAt` instants or `startDate`/`endDate` dates), and Calendar reads compare all-day dates in the `timeZone` parameter, defaulting to `LIFE_OS_TIMEZONE`; Inbox creation requires `content` and `source`. A Voice upload is multipart field `audio` plus optional `source` and JSON `sourceMetadata`; it is queued rather than transcribed inline, and the upload's `sourceMetadata` is carried over to the resulting Inbox item. An Inbox item's `sourceMetadata.timeZone` (an IANA name, sent by the Dashboard with text and voice captures) is the timezone its relative dates are planned in. Inbox's optional `dedupeKey` makes retried Voice handoffs idempotent. Exact request/response schemas, pagination, filtering, and validation are versioned in `services/*/api/openapi.yaml`.
