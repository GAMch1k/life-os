# Notifications service

Notifications is the one path by which Life OS lets its owner know about something. A service that needs to tell the owner something (a service went down, Inbox needs Human Review, a Calendar reminder is due) sends a notification here. Notifications stores it in its own `notifications_db`, so the Dashboard can list every notification. It keeps the read state and delivers the notification through the channels configured on its server. Gateway base: `/api/notifications`. The contract is `services/notifications/api/openapi.yaml`; `/health` and `/ready` are available.

## The one-path rule

- Producers never talk to a channel (ntfy, a phone app, email) themselves. They post to `POST /v1/notifications`, from Go through `pkg/notify`.
- A channel is a server-side concern. Swapping the first channel (a self-hosted ntfy, P12-02) for Life OS's own Android app later changes nothing for producers.
- Every notification is stored and listed, whether or not any channel delivers it. The Dashboard notification list (P12-03) reads this API.
- Notifications never reads another service's database, and no service reads `notifications_db`.

```mermaid
flowchart LR
  Calendar -- "pkg/notify" --> N[Notifications / notifications_db]
  Inbox -- "pkg/notify" --> N
  Monitor -- "pkg/notify" --> N
  Dashboard -- "GET /api/notifications" --> Gateway --> N
  N -- worker --> Channels["Channels (none yet; ntfy in P12-02)"]
  N -. record .-> Activity
```

## A notification

| Field | Meaning |
| --- | --- |
| `title` | One line, 1–200 characters, no control characters. Surrounding spaces are trimmed. |
| `body` | Optional plain text, at most 2000 characters. Line breaks and tabs are kept; no HTML or Markdown. |
| `importance` | `low`, `medium`, or `high` (below). |
| `category` | Dotted lowercase kind with at least two parts, at most 100 characters: `calendar.reminder`, `inbox.human_review`, `status.service_down`, `test.manual`. |
| `source` | The producing service, such as `calendar`. Defaults to the actor's name (below). |
| `link` | Optional Life OS path to open, starting with a single `/`, for example `/inbox` or `/calendar?date=2026-10-10`. Absolute (`https://…`) and protocol-relative (`//…`) URLs, backslashes, and spaces are rejected. At most 500 characters. |
| `dedupeKey` | Required idempotency key, 1–200 characters, unique across all notifications (below). |
| `occurredAt` | When the notified thing happened. Defaults to the time of the request. |
| `createdAt` | When Notifications received it. The list is ordered by it. |
| `readAt` | When the owner marked it read; `null` while unread. |
| `deliveries` | One entry per channel (below). Empty when no channel was configured at creation. |

### Importance

| Importance | Meaning | Examples |
| --- | --- | --- |
| `high` | Something is broken or needs action now. | A service went down. |
| `medium` | Worth knowing soon. | Inbox needs Human Review; a Calendar event reminder. |
| `low` | Informational. | Future Finance and Food messages. |

There are no quiet hours. Every notification is delivered right away; how loudly it arrives is a channel's decision (a `low` one arrives silently). The unread count reports high-importance notifications separately for a navigation badge.

### Source from the actor

Like every Life OS write, sending needs `X-Life-OS-Actor`. Without an explicit `source`, it is derived from the actor:

| Actor | Source |
| --- | --- |
| `system:calendar` | `calendar` (the name, lowercase, `_` as `-`) |
| `agent:<name>` | `<name>` |
| `user` | `dashboard` |
| `dev:<name>` | `dev` (all development tools share one source, so test notifications are easy to filter) |

### Dedupe

`dedupeKey` makes sending idempotent. The first notification with a key is stored (201). Any later request with the same key returns that original notification unchanged with 200: its fields are ignored, nothing new is stored or delivered, and no Activity event is recorded. This holds for concurrent requests too. A producer can therefore retry, or send again after a restart, without notifying twice. Namespace keys with the category and make them name the occurrence, for example `calendar.reminder:<eventId>:<startAt>:<minutes>` or `status.service_down:<service>:<since>`.

## Delivery

When a notification is created, one `pending` delivery is queued for each channel enabled on the server, in the same transaction. A worker in the service process (woken at once by the new notification, and otherwise polling every `NOTIFICATIONS_WORKER_POLL_MILLIS`) claims a due delivery with a lease, calls the channel, and records the outcome:

| Status | Meaning |
| --- | --- |
| `pending` | Queued and not tried yet, or being sent now. |
| `retrying` | An attempt failed; the next one is due at `nextAttemptAt`. `lastError` says why. |
| `sent` | The channel accepted it (`sentAt`). |
| `failed` | A permanent error, or no success within the maximum delivery age. |
| `skipped` | Deliberately not sent, for example because its channel is no longer configured. |

- **Retries.** A failed attempt is retried after an exponential backoff: `NOTIFICATIONS_RETRY_BASE_SECONDS` (10 s), then twice that each time, capped at `NOTIFICATIONS_RETRY_MAX_SECONDS` (15 min). A channel marks an error as permanent (a rejected configuration, for example) to fail the delivery at once.
- **Maximum age.** A delivery whose notification is older than `NOTIFICATIONS_DELIVERY_MAX_AGE_SECONDS` (24 h), or whose next retry would fall beyond it, fails instead, so a stale reminder never arrives a day late. It stays in the list.
- **Crash safety.** A claim holds a lease of `NOTIFICATIONS_LEASE_SECONDS` (60 s). If the service stops mid-send, the delivery becomes claimable again when the lease expires; the interrupted attempt still counts. Delivery is therefore at least once: in that rare case a channel may receive a notification twice. Each attempt is bounded by `NOTIFICATIONS_SEND_TIMEOUT_SECONDS` (15 s, shorter than the lease).
- **Several workers.** Claims use `FOR UPDATE SKIP LOCKED`, so more than one process could share the queue; today there is one.

### Channels

A channel implements `Name()` and `Send(ctx, notification) error` (`services/notifications/internal/delivery`). An error is retryable unless wrapped with `delivery.Permanent`; `delivery.Skip` ends the delivery as skipped. Channels are configured on the server only, in `NOTIFICATIONS_CHANNELS` (compose: `LIFE_OS_NOTIFICATIONS_CHANNELS`), comma-separated. An unknown name stops the service at startup.

- Configured today: **none**. Notifications are stored and listed in the Dashboard, and their `deliveries` are empty.
- `log` writes each delivery's metadata (never the body) to the service log. It is for development and verification of the delivery path.
- `ntfy` (a self-hosted ntfy server) comes in P12-02. Life OS's own Android app replaces it later.

A delivery belongs to the channels enabled when its notification was created. Enabling a channel later does not deliver older notifications; removing one skips its pending deliveries.

## Activity

Notifications records, through `pkg/activity`, with subject `notification/<id>`:

| Type | Actor | Data |
| --- | --- | --- |
| `notification.created` | The sender (for example `system:calendar`) | `title`, `importance`, `category`, `source`, `link`, `channels`; the title is also in the summary. Never the body. |
| `notification.delivered` | `system:notifications` | `channel`, `attempts`, `importance`, `category`, `source` |
| `notification.delivery_failed` | `system:notifications` | the same, plus `error` |

A notification keeps the sender's correlation id, so its delivery events join the producer's workflow; their causation is `notification.created`. Retries and skips record nothing. The Event Log already lists these events with generic labels (it humanizes an unknown domain, entity, or type); a Notifications domain filter and labels come with the Dashboard work (P12-03).

## API

- `POST /v1/notifications`: send. `201` with the new notification; `200` with the original for a known `dedupeKey`; `400` `bad_request` for invalid input or `actor_required` without a valid actor.
- `GET /v1/notifications`: newest first by `createdAt`, then `id`. Filters: `importance`, `category` (a category or a prefix: `calendar` matches `calendar` and every `calendar.*`), `source`, `unread` (`true` unread only, `false` read only), plus `cursor` and `limit` (1–100, default 50). Pass `nextCursor` back as `cursor`, with the same filters, for the next page.
- `GET /v1/notifications/{id}`: one notification with its deliveries; `404` `not_found`.
- `POST /v1/notifications/{id}/read`: mark read; idempotent, an already read notification keeps its `readAt`.
- `POST /v1/notifications/read-all`: mark every unread notification read, or with an optional body `{"before": "<RFC 3339>"}` only those created at or before that instant (so one that arrives while the owner looks at the list stays unread). Returns `{"marked": n}`.
- `GET /v1/notifications/unread-count`: `{"count": n, "highCount": m}`, cheap enough for a navigation badge.

Errors use the shared envelope `{"error": {"code", "message"}, "request_id"}`. Through the gateway only `user` and `dev:<name>` actors pass, as for every service.

```sh
curl -X POST "$GATEWAY/api/notifications/v1/notifications" -H 'X-Life-OS-Actor: dev:curl' \
  -H 'content-type: application/json' \
  -d '{"title":"Test notification","importance":"low","category":"test.manual","dedupeKey":"test.manual:1"}'
curl "$GATEWAY/api/notifications/v1/notifications?unread=true&limit=20"
curl "$GATEWAY/api/notifications/v1/notifications/unread-count"
```

## Sending from a service (`pkg/notify`)

```go
notifier := notify.FromEnv("calendar") // NOTIFICATIONS_SERVICE_URL; empty = no-op
err := notifier.Send(ctx, notify.Notification{
	Title:      "Dentist in 30 minutes",
	Importance: notify.Medium,
	Category:   "calendar.reminder",
	Link:       "/calendar?date=2026-10-10",
	DedupeKey:  "calendar.reminder:" + eventID + ":" + startAt + ":30",
})
```

`Send` posts as `system:<service>` with a 5 s timeout (`Client.Timeout`), refuses a notification without a dedupe key, title, valid importance, or category (`notify.ErrInvalid`), retries network errors and 5xx twice (safe thanks to the dedupe key), and returns a `*notify.StatusError` for a rejection. It forwards the correlation and causation of an `activity.Trace` in the context. With an empty `NOTIFICATIONS_SERVICE_URL` it sends nothing and returns nil, so a service runs unchanged where Notifications is not configured.

### Producers

The service that owns a state change sends its notification, so every path into that state is covered once. Compose gives each producer `NOTIFICATIONS_SERVICE_URL: http://notifications:8080`; it does not depend on Notifications starting first, because sends are best effort.

| Producer | Category | Importance | When | Link | Dedupe key |
| --- | --- | --- | --- | --- | --- |
| Inbox | `inbox.human_review` | `medium` | A capture moves to Human Review, once per review round. | `/inbox#human-review` | `inbox.human_review:<itemId>:<attemptId>` |
| Inbox | `inbox.failed` | `medium` | Automatic processing gave up on a capture (attempt `failed`). Not for retries or successes. | `/inbox#failed` | `inbox.failed:<itemId>:<attemptId>` |

See [Inbox](inbox.md#notifications) for the payloads and failure handling.

## Operations

- Database: `notifications_db` / `notifications_user`, password `LIFE_OS_NOTIFICATIONS_DB_PASSWORD` in `.env`. Fresh volumes are provisioned automatically; for an existing deployment see [Operations](../operations/deployment.md#adding-the-notifications-service-to-an-existing-deployment).
- Configuration (compose sets the first three): `DATABASE_URL`, `ACTIVITY_SERVICE_URL`, `NOTIFICATIONS_CHANNELS` (from `LIFE_OS_NOTIFICATIONS_CHANNELS`, empty by default), and the delivery settings above with their defaults.
- The Dashboard reaches the service through `NOTIFICATIONS_SERVICE_URL` (server rendering) and `/api/notifications` (browser); the Status page lists it under Services.
- Retention: notifications are kept indefinitely for now.
- Tests: unit tests (validation, actor to source, cursor, backoff, maximum age, error classes, configuration) and integration tests against the Postgres named by `NOTIFICATIONS_TEST_DATABASE_URL` (each test creates and drops its own schema; skipped when unset): sending, concurrent dedupe, filters and paging, read state, the unread count, migrations down and up, and the worker's sent, retry, permanent, maximum-age, skipped, expired-lease, and concurrent-worker paths. `pkg/notify` is tested against an in-process server.

```sh
cd services/notifications
NOTIFICATIONS_TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:55441/postgres?sslmode=disable' GOWORK=off go test -count=1 ./...
```
