# Notifications service

Notifications is the one path by which Life OS lets its owner know about something. A service that needs to tell the owner something (a service went down, Inbox needs Human Review, a Calendar reminder or a Task is due) sends a notification here. Notifications stores it in its own `notifications_db`, so the Dashboard can list every notification. It keeps the read state and delivers the notification through the channels configured on its server. Gateway base: `/api/notifications`. The contract is `services/notifications/api/openapi.yaml`; `/health` and `/ready` are available.

## The one-path rule

- Producers never talk to a channel (ntfy, web push, a phone app, email) themselves. They post to `POST /v1/notifications`, from Go through `pkg/notify`.
- A channel is a server-side concern. Swapping the first channel (a self-hosted ntfy that reaches the owner's Android phone) for Life OS's own Android app later changes nothing for producers.
- Every notification is stored and listed, whether or not any channel delivers it. The Dashboard notification list (P12-03) reads this API.
- Notifications never reads another service's database, and no service reads `notifications_db`.

```mermaid
flowchart LR
  Calendar -- "pkg/notify" --> N[Notifications / notifications_db]
  Tasks -- "pkg/notify" --> N
  Inbox -- "pkg/notify" --> N
  Monitor -- "pkg/notify" --> N
  Dashboard -- "GET /api/notifications" --> Gateway --> N
  N -- "worker (ntfy channel)" --> Ntfy["ntfy (self-hosted)"] -- "ntfy Android app" --> Phone["Owner's phone"]
  N -- "worker (webpush channel)" --> Push["Push service (FCM)"] -- "Life OS Android app (UnifiedPush)" --> Phone
  N -. record .-> Activity
```

## A notification

| Field | Meaning |
| --- | --- |
| `title` | One line, 1–200 characters, no control characters. Surrounding spaces are trimmed. |
| `body` | Optional plain text, at most 2000 characters. Line breaks and tabs are kept; no HTML or Markdown. |
| `importance` | `low`, `medium`, or `high` (below). |
| `category` | Dotted lowercase kind with at least two parts, at most 100 characters: `calendar.reminder`, `inbox.human_review`, `status.processing`, `test.manual`. |
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

There are no quiet hours. Every notification is delivered right away; how loudly it arrives is a channel's decision (a `low` one arrives silently; see [ntfy](#ntfy) and [web push](#web-push) for the priorities). The unread count reports high-importance notifications separately for a navigation badge.

### Source from the actor

Like every Life OS write, sending needs `X-Life-OS-Actor`. Without an explicit `source`, it is derived from the actor:

| Actor | Source |
| --- | --- |
| `system:calendar` | `calendar` (the name, lowercase, `_` as `-`) |
| `agent:<name>` | `<name>` |
| `user` | `dashboard` |
| `dev:<name>` | `dev` (all development tools share one source, so test notifications are easy to filter) |

### Dedupe

`dedupeKey` makes sending idempotent. The first notification with a key is stored (201). Any later request with the same key returns that original notification unchanged with 200: its fields are ignored, nothing new is stored or delivered, and no Activity event is recorded. This holds for concurrent requests too. A producer can therefore retry, or send again after a restart, without notifying twice. Namespace keys with the category and make them name the occurrence, for example `calendar.reminder:<eventId>:<startAt>:<minutes>` or the monitor's `status:<check>:<state>:<since>`.

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

A channel implements `Name()` and `Send(ctx, notification) error` (`services/notifications/internal/delivery`). An error is retryable unless wrapped with `delivery.Permanent`; `delivery.Skip` ends the delivery as skipped. Channels are configured on the server only, in `NOTIFICATIONS_CHANNELS` (compose: `LIFE_OS_NOTIFICATIONS_CHANNELS`), comma-separated. An unknown name, or a channel missing its settings, stops the service at startup.

- Empty (the default): notifications are stored and listed in the Dashboard, and their `deliveries` are empty.
- `ntfy` pushes each notification to the owner's Android phone through the self-hosted ntfy server (below).
- `webpush` pushes each notification to every subscribed device through its push service ([web push](#web-push)). It needs the VAPID keys. The Dashboard has no browser notifications (the owner's decision, 2026-10-12); the subscriber is the Android app (P18-05, from 0.3.0), which registers its UnifiedPush endpoint through `POST /v1/push/subscriptions`.
- `log` writes each delivery's metadata (never the body) to the service log. It is for development and verification of the delivery path.

The deployment uses `LIFE_OS_NOTIFICATIONS_CHANNELS=webpush` since 2026-10-12: notifications reach the phone through the Life OS Android app's push (Google's FCM). The owner's work Wi-Fi blocks FCM as it blocked ntfy, which the owner accepts ([Phase 12](../verification/phase-12.md)). ntfy is **paused**, not removed: its container is stopped, its profile and data stay ([ntfy → Paused](../operations/ntfy.md#paused-since-2026-10-12)); with `ntfy,webpush` both channels deliver again.

A delivery belongs to the channels enabled when its notification was created. Enabling a channel later does not deliver older notifications; removing one skips its pending deliveries.

### ntfy

The `ntfy` channel publishes to one private topic on the ntfy server in the Life OS stack ([setup and operations](../operations/ntfy.md)), over the internal `notify-internal` network (which only ntfy and this service share) with ntfy's JSON publish API: `POST http://ntfy:80/` with `Authorization: Bearer <publisher token>` and

| ntfy field | From the notification |
| --- | --- |
| `topic` | `NOTIFICATIONS_NTFY_TOPIC` (default `lifeos`) |
| `title` | `title` |
| `message` | `body`, or the title when the body is empty; cut at 3500 bytes (on a character boundary, with "…"), because ntfy turns a longer message into an attachment, which this server refuses |
| `priority` | `importance`: `low` → 2 (silent, no sound or vibration), `medium` → 3 (default), `high` → 5 (max; the owner may let it through Do Not Disturb) |
| `tags` | the category's domain as an emoji: `calendar` → 📅 `calendar`, `inbox` → 📥 `inbox_tray`, `status` → ⚠️ `warning`, `test` → 🧪 `test_tube`, `tasks` → ☑️ `ballot_box_with_check`, `finance` → 💰 `moneybag`; none for other domains |
| `click` | `NOTIFICATIONS_LINK_BASE_URL` + `link`, for example `https://lifeos.example.com/calendar?date=2026-10-10`; without a link, `/notifications`. Tapping the notification opens that page (through Cloudflare Access) |

Markdown stays off: bodies are plain text. Outcomes: 2xx is sent. ntfy refusing the request (400, 401, 403, 404, 413, a redirect, any other 4xx) is permanent: the configuration or the message is wrong, and the delivery fails at once with ntfy's error, for example `ntfy refused the notification: 401 unauthorized (ntfy code 40101)`. 408, 429, 5xx, and an unreachable ntfy (stopped, restarting, or its profile off) are retried with the usual backoff. The token is never logged or put in an error.

Settings (compose sets them): `NOTIFICATIONS_NTFY_URL` (`http://ntfy:80`), `NOTIFICATIONS_NTFY_TOPIC` (`LIFE_OS_NTFY_TOPIC`), `NOTIFICATIONS_NTFY_TOKEN` (`LIFE_OS_NTFY_PUBLISHER_TOKEN`, a secret), and `NOTIFICATIONS_LINK_BASE_URL` (`LIFE_OS_PUBLIC_URL`, which compose requires, such as `https://lifeos.example.com`).

To send yourself a test notification (it arrives silently, with 🧪):

```sh
curl -X POST "$GATEWAY/api/notifications/v1/notifications" -H 'X-Life-OS-Actor: dev:curl' \
  -H 'content-type: application/json' \
  -d '{"title":"Test notification","body":"From Life OS","importance":"low","category":"test.manual","link":"/notifications","dedupeKey":"test.manual:'"$(date +%s)"'"}'
```

### Web push

The `webpush` channel delivers with the Web Push protocol (RFC 8030): a subscriber registers its push service subscription with Life OS (an endpoint URL and two keys), and Notifications posts each notification to that endpoint. The subscriber is the Life OS Android app (P18-05), through UnifiedPush with its embedded FCM distributor; the Dashboard has no browser notifications (the owner's decision, 2026-10-12). The push service is Google's FCM, which the phone keeps a connection to anyway, so the app shows a push even while it is closed. The payload is end-to-end encrypted for the subscriber (RFC 8291); the push service sees only its size and the request's urgency and lifetime.

```mermaid
sequenceDiagram
  participant A as Life OS Android app
  participant N as Notifications
  participant P as Push service (FCM)
  A->>N: GET /v1/push/config (VAPID public key)
  A->>P: subscribe (UnifiedPush / Web Push)
  P-->>A: endpoint + keys
  A->>N: POST /v1/push/subscriptions
  Note over N: a notification is created
  N->>P: POST endpoint (encrypted payload, VAPID token)
  P->>A: push; the app shows it
```

**Signing and encryption.** `internal/webpush` implements the protocol with Go's standard library: each push is encrypted for the subscription with the `aes128gcm` content coding (RFC 8291 over RFC 8188; one record, a fresh ephemeral key and salt each time; checked against the RFC's example byte for byte) and signed with a VAPID token (RFC 8292: an ES256 JWT for the push service's origin, valid 12 hours, `sub` the subject below, and the public key in `Authorization: vapid t=…, k=…`). No third-party Web Push library: the standard library has every primitive (P-256 ECDH and ECDSA, HKDF, AES-GCM), and the channel needs the push service's status codes itself.

**The payload** is JSON for the subscriber to turn into a notification:

| Field | From the notification |
| --- | --- |
| `title`, `body` | `title` and `body`; a body too long for one push is cut on a character boundary with "…", so the whole payload stays within the 4,096 bytes push services accept (at most 3,993 bytes of JSON) |
| `link` | `link`, or `/notifications` without one. A tap opens it in Life OS |
| `tag` | `lifeos-<id>`: a notification with the tag of one already shown replaces it, so a repeated delivery never shows twice, while two notifications of one kind (two reminders) both stay |
| `importance`, `category`, `id` | as stored; `low` shows silently, as on ntfy |
| `timestamp` | `createdAt` in milliseconds, the time the phone shows |

The request asks the push service for an urgency and a lifetime (`TTL`, how long it keeps the push for a phone that is offline) by importance: `high` → `high`, 24 hours; `medium` → `normal`, 12 hours; `low` → `low` (may wait until the phone wakes by itself), 4 hours. Its `Topic` is the notification's id, so a repeated push replaces one the push service still holds.

**Several devices.** A notification has one `webpush` delivery, whatever the number of devices, and the channel keeps each device's outcome for it in `push_outcomes`:

- An attempt sends to the devices that were subscribed when the notification was created (a device added later does not get older notifications) and have no outcome for it yet, up to four at once within the send timeout.
- A device's push service accepting it (2xx) records `sent` and the device's latest success. 404 or 410 (the browser unsubscribed, its data was cleared, or the subscription expired) deletes the subscription. Any other refusal (400, 401, 403 such as a key that changed, 413, a redirect) records `failed` for that device and notification. Each failure is also the device's latest failure and error.
- A device that failed for now (unreachable, 408, 429, 5xx) gets no outcome. The delivery is then retried with the usual backoff, and the retry sends only to the devices still without one: a failing device never blocks the others or makes them get the notification twice. The delivery's error names the devices, such as `1 of 2 devices failed for now: Chrome on Android: push service answered 503 Service Unavailable`.
- Once every device has its outcome, the delivery is `sent` when at least one device accepted it, `failed` when every device refused it or every one is gone, and `skipped` ("no device has turned on notifications in Life OS") when there was none.

Delivery stays at least once: a crash between a push service accepting a push and its outcome being recorded sends it again to that device, where the tag replaces the first. The maximum delivery age applies to the whole delivery: past it, devices still failing do not get the notification.

**Devices.** `push_subscriptions` holds each device: its endpoint (unique; subscribing again with a known endpoint updates its keys and label and keeps its id), its keys (`p256dh`, `auth`), a label read from the user agent of the browser that subscribed ("Chrome on Android", "Firefox on Windows"; the Life OS Android app, whose user agent is `LifeOS-Android/<versionName> (<versionCode>)`, is "Life OS app on Android"), when it was added, and its latest success and failure. The endpoint must be an https URL on a host name (no address, no single-label name such as a container, no credentials), since the server posts to it; requests never follow redirects. Errors and logs name only the push service's host, never the endpoint's path, which identifies the subscription.

**Configuration.** `NOTIFICATIONS_WEBPUSH_VAPID_PUBLIC_KEY` and `NOTIFICATIONS_WEBPUSH_VAPID_PRIVATE_KEY` (compose: `LIFE_OS_WEBPUSH_VAPID_PUBLIC_KEY` and `LIFE_OS_WEBPUSH_VAPID_PRIVATE_KEY`, empty by default) are the VAPID key pair, base64url: the public key an uncompressed P-256 point (87 characters), the private key its 32-byte scalar (43 characters, a secret: only in `.env`, never logged). `NOTIFICATIONS_WEBPUSH_SUBJECT` (`LIFE_OS_WEBPUSH_SUBJECT`) is how a push service's operator can reach the sender, a `mailto:` address or an https URL; empty uses `NOTIFICATIONS_LINK_BASE_URL`. With the keys set, devices can subscribe and receive test pushes; `webpush` in `NOTIFICATIONS_CHANNELS` also delivers every notification to them. A key without its pair, a pair that does not match, an invalid subject, or `webpush` listed without keys stops the service at startup. Replacing the keys invalidates every subscription (the push services refuse the new signature with 401 or 403): turn notifications on again on each device, which replaces its subscription.

**Network.** Pushes go from the service to the push services over HTTPS (`fcm.googleapis.com`, `updates.push.services.mozilla.com`, `web.push.apple.com`, `*.notify.windows.com`). The service is on the `backend` network, which has outbound access (as the integrations have), so nothing changes there; `notify-internal` stays internal for ntfy. The phone never connects to Life OS for a push.

**Cloudflare Access.** A subscriber calls the API through the public hostname (`LIFE_OS_PUBLIC_HOSTNAME`), behind Access; pushes themselves go from the server to the push service, so the phone never connects to Life OS to receive one.

**Set it up** (once; see [Operations](#operations) for the deploy):

1. Generate the key pair. It prints two `.env` lines; paste them into the root `.env`:

   ```sh
   cd services/notifications && GOWORK=off go run ./cmd/vapid-keys
   ```

2. Optionally set `LIFE_OS_WEBPUSH_SUBJECT=mailto:<your address>` (default: `LIFE_OS_PUBLIC_URL`). Add `webpush` to `LIFE_OS_NOTIFICATIONS_CHANNELS` (`ntfy,webpush`) only once a subscriber exists.
3. Deploy (migration `000003_push_subscriptions`, then the service).
4. Turn notifications on in the Life OS Android app, which subscribes. The Dashboard has no browser notifications (the owner's decision, 2026-10-12).

To send a test push to every device from the command line:

```sh
curl -X POST "$GATEWAY/api/notifications/v1/push/test" -H 'X-Life-OS-Actor: dev:curl'
```

### Channel health

`GET /v1/channels` lists each configured channel, in the configured order, with what its deliveries show: `lastSuccessAt` (the latest sent delivery), `lastFailureAt` and `lastError` (the latest delivery that failed or is waiting for a retry), `recentFailures` (deliveries that failed or wait for a retry, last changed in the past 24 hours), `retrying` and `pending` now, and a `state`: `ok` when the latest outcome was a success, `failing` when it was a failure, `idle` before the first outcome. Nothing is probed, so a broken channel shows only once a delivery has tried it, and stays failing until one succeeds (send a test notification after a fix). An empty list means no channel is configured. The Dashboard Status page shows it as the Notifications check.

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
- `GET /v1/channels`: the configured delivery channels and their health ([above](#channel-health)).
- `GET /v1/push/config`: `{"configured", "publicKey", "delivering"}`: whether web push has its VAPID keys, the public key browsers subscribe with (null without keys), and whether `webpush` is a delivery channel.
- `GET /v1/push/subscriptions`: the subscribed devices, oldest first: `id`, `deviceLabel`, `endpoint`, `pushService` (its host), `createdAt`, `lastSuccessAt`, `lastFailureAt`, `lastError`, and `deviceKeyId` (below).
- `POST /v1/push/subscriptions`: subscribe a device with the browser's `PushSubscription.toJSON()` (`endpoint`, `keys.p256dh`, `keys.auth`; `expirationTime` is ignored). `201` for a new endpoint, `200` with the device for a known one (its keys and label updated); `400` for an invalid one; `409` `push_not_configured` without VAPID keys. The label comes from the request's `User-Agent`.
- `DELETE /v1/push/subscriptions/{id}`: remove a device (`204`, or `404`). The browser stays subscribed until it unsubscribes; its pushes simply stop.
- `DELETE /v1/push/device-keys/{keyId}`: delete every subscription a paired device's key made (`204`, also with none). For the Dashboard on the private network, when the key is revoked; a request with an identity token, which every request through the gateway has, gets `403 internal_only` (P21-07 makes it the Dashboard's service assertion).
- `POST /v1/push/test`: send a test notification ("Test notification", opening `/notifications`, tag `lifeos-test`) to every device, or with `{"subscriptionId": "…"}` to one, right away and outside the queue. It answers each device's outcome: `sent`, `removed` (its push service said 404 or 410, so it was deleted), or `failed` with the error, and records it as the device's latest success or failure. Nothing is stored as a notification. `404` for an unknown device; `409` `push_not_configured` without keys.

**Device keys** (P21-06, [the phone apps' API](../api/devices.md#push-notifications-and-the-key)). A subscription made with a paired phone's or watch's device key (the identity token's `via: device`) stores the key's id (`device_key_id`, migration `000004`); subscribing an endpoint again moves it to the key that sent it, and a browser's subscription (the TWA) has none. When the key is revoked (Settings → Devices, Sign out everywhere, `./setup.sh sign-out-all`, the key's own sign-out, a phone's new watch key), the Dashboard notes it in `auth_db` in the same transaction and asks `DELETE /v1/push/device-keys/{keyId}` within a minute, again every minute while Notifications does not answer.

Errors use the shared envelope `{"error": {"code", "message"}, "request_id"}`. Writes (`POST`, `DELETE`) need `X-Life-OS-Actor`. Through the gateway only `user` and `dev:<name>` actors pass, as for every service.

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

`Latest(ctx, category)` returns the newest notification the client's own service sent in a category (and below it), or nil when there is none. A producer that keeps its state in memory reads its own history with it after a restart; the monitor does, so a restart neither repeats nor loses a status episode.

### Producers

The service that owns a state change sends its notification, so every path into that state is covered once. Compose gives each producer `NOTIFICATIONS_SERVICE_URL: http://notifications:8080`; it does not depend on Notifications starting first, because sends are best effort.

| Producer | Category | Importance | When | Link | Dedupe key |
| --- | --- | --- | --- | --- | --- |
| Inbox | `inbox.human_review` | `medium` | A capture moves to Human Review, once per review round. | `/inbox#human-review` | `inbox.human_review:<itemId>:<attemptId>` |
| Inbox | `inbox.failed` | `medium` | Automatic processing gave up on a capture (attempt `failed`). Not for retries or successes. | `/inbox#failed` | `inbox.failed:<itemId>:<attemptId>` |
| [Monitor](monitor.md) | `status.<check>`, such as `status.processing` or `status.services`; `status.dashboard` while the Status checks cannot be read | `high` for a problem, `medium` for a warning, `low` for a recovery | A Status check holds a new state for the confirmations (two evaluations a minute apart). | `/status` | `status:<check>:<state>:<since>` |
| Calendar | `calendar.reminder` | the event's effective priority (`medium` unless set) | An effective reminder of an active event or occurrence is due: every one of them, exactly once. One due while Calendar was down is sent within `CALENDAR_REMINDER_GRACE_MINUTES` (2 h) with "(late)", and recorded in Activity as missed after that. A failed send is retried at the next run. | `/calendar?date=<start date>` | `calendar:reminder:<eventId>:<recurrenceId or single>:<start>:<reminder>` |
| [Tasks](tasks.md#notifications) | `tasks.due` | `medium` | `TASKS_TIMED_LEAD_MINUTES` (30) before an open Task with a due time is due, once per Task and due time. | `/tasks?task=<taskId>` | `tasks:due:<taskId>:<dueAt>` |
| Tasks | `tasks.morning` | `medium` | Daily at `TASKS_MORNING_TIME` (09:00): one message with the date-only Tasks due today and every overdue Task; not sent when empty. | `/tasks` | `tasks:morning:<local date>` |
| Tasks | `tasks.evening` | `medium` | Daily at `TASKS_EVENING_TIME` (19:00): one message with the date-only Tasks due tomorrow; not sent when empty. | `/tasks` | `tasks:evening:<local date>` |
| Tasks | `summary.morning` | `low` (`medium` when merged with the morning list and naming its Tasks) | Daily at `TASKS_SUMMARY_TIME` (08:00): today's events (from Calendar), Tasks, and overdue Tasks; not sent when there is nothing. At the morning-list time it replaces the morning list. | `/` | `summary:morning:<local date>` |
| [Food](food.md#expiry-reminders) | `food.expiry` | `medium` when something expires that day, else `low` | Daily at `FOOD_EXPIRY_TIME` (08:00): one message with the in-stock pantry food that expires today or tomorrow ("Today: Milch", "Tomorrow: Joghurt (2)"); not sent when there is none. | `/food?tab=pantry` | `food:expiry:<local date>` |
| [Finance](finance.md#monthly-budgets) | `finance.budget` | `low` | A category of the current month's budget reaches 80%, and again 100% or over, of its planned amount (booked and pending spending), once per category and month. | `/finance?tab=budget` | `finance.budget:<YYYY-MM>:<categoryId>:80` or `:100` |
| Finance | `finance.budget.summary` | `low` | The 1st at 09:00 owner-local, for the previous month when it had a budget: "October: spent X of Y, saved Z"; sent up to a week late after an outage. | `/finance?tab=budget&month=<YYYY-MM>` | `finance.budget:<YYYY-MM>:summary` |
| [Finance](finance.md#ai-budget-plans) | `finance.ai_budget_plan.ready` | `medium` | An AI budget plan the owner did not ask for is ready: the one the salary asked for ([Payday](finance.md#payday)) or the 1st's: "Your budget plan for November is ready", with the AI's headline; once per plan, up to a week late after an outage, not when the owner asked for it. | `/finance?tab=budget&month=<YYYY-MM>` | `finance.ai_budget_plan.ready:<planId>` |
| [Finance](finance.md#alerts) | `finance.subscription` | `low` | The renewal reminders' lead days (3) before a confirmed subscription's next payment: "Netflix renews in 3 days: €13.99". | `/finance?tab=subscriptions` | `finance.subscription:<id>:<YYYY-MM-DD>` |
| Finance | `finance.large_debit` | `medium` | Money out of at least the large-debit threshold (€250), booked or pending, booked in the last three days, not in a transfer category; once per transaction. | `/finance` | `finance.large_debit:<transactionId>` |
| Finance | `finance.low_balance` | `medium` | A bank account's balance with its pending card payments (from the bank connection; never Cash or other manual accounts) falls below the low-balance threshold (€100); again only after it went back above. | `/finance` | `finance.low_balance:<accountId>:<episode>` |

For all of Tasks' messages, one due while Tasks was down is sent within `TASKS_NOTIFY_GRACE_MINUTES` (2 h) and recorded in Activity as missed after that; a failed send is retried at the next run. Food's expiry reminder works the same way, within `FOOD_NOTIFY_GRACE_MINUTES` (2 h). Finance claims each budget and alert notification in its database before sending and gives the claim back when a send fails for now, so it is retried at the next check (every 5 minutes) and never repeated after a restart. See [Inbox](inbox.md#notifications), [Monitor](monitor.md), [Calendar](calendar.md#reminder-delivery), [Tasks](tasks.md#notifications), [Food](food.md#expiry-reminders), and [Finance](finance.md#monthly-budgets) for the payloads and failure handling.

## Operations

- Database: `notifications_db` / `notifications_user`, password `LIFE_OS_NOTIFICATIONS_DB_PASSWORD` in `.env`. Fresh volumes are provisioned automatically; for an existing deployment see [Operations](../operations/deployment.md#adding-the-notifications-service-to-an-existing-deployment).
- Configuration (compose sets these): `DATABASE_URL`, `ACTIVITY_SERVICE_URL`, `NOTIFICATIONS_CHANNELS` (from `LIFE_OS_NOTIFICATIONS_CHANNELS`, empty by default), the [ntfy settings](#ntfy), and the [web push settings](#web-push) (`LIFE_OS_WEBPUSH_*`, empty by default); the delivery settings above keep their defaults.
- Enabling web push on a running deployment: add the two key lines and `webpush` in `LIFE_OS_NOTIFICATIONS_CHANNELS` to `.env` ([setup](#web-push)); build the service (`docker compose build --pull notifications`; the Dashboard needs nothing, as it has no push of its own); apply migration `000003_push_subscriptions` with `docker compose up --no-build --no-deps notifications-migrate` (it exits when done); then `docker compose up -d --no-build --no-deps notifications`. The gateway needs nothing: `/api/notifications/` already routes every method, and passes the user agent on. The service logs `notification channels configured` with `webpush`; `GET /api/notifications/v1/push/config` says `configured` and `delivering`. Then turn notifications on in the Life OS Android app. Rolling back the migration (`000003…down.sql`) drops the devices and their outcomes; remove `webpush` from the channels first.
- The verification stack never receives the web push keys (`tools/verify-compose` blanks them and refuses a plan with a private key), so web push is not set up there and no browser test covers it (the Dashboard has no push; `e2e/web-push.spec.ts` went with the reverted browser push). The unit and integration tests below cover the channel and its API.
- The ntfy server is an opt-in container in the same stack (Compose profile `ntfy`); see [ntfy](../operations/ntfy.md) for the owner's setup, token rotation, and troubleshooting. The verification stack never receives the owner's channels or ntfy settings (`tools/verify-compose`).
- The Dashboard reaches the service through `NOTIFICATIONS_SERVICE_URL` (server rendering) and `/api/notifications` (browser); the Status page lists it under Services and shows channel health as the Notifications check.
- Retention: notifications are kept indefinitely for now.
- Tests: unit tests (validation, actor to source, cursor, backoff, maximum age, error classes, configuration, the channel state, the ntfy channel against a fake ntfy server: JSON request and token, priorities, tags, click links, truncation, refusals and retries, deadline; and web push: the RFC 8291 example reproduced byte for byte, VAPID keys and token, the payload and its cut, urgency and TTL, device labels, and sends to `webpushtest`, a fake TLS push service that verifies the VAPID signature and decrypts each payload with the subscription's private key, as the browser does) and integration tests against the Postgres named by `NOTIFICATIONS_TEST_DATABASE_URL` (each test creates and drops its own schema; skipped when unset): sending, concurrent dedupe, filters and paging, read state, the unread count, migrations down and up, the worker's sent, retry, permanent, maximum-age, skipped, expired-lease, and concurrent-worker paths, channel health, and web push through the worker (every device reached, 410 pruning, 429 and an unreachable device retried alone without duplicates, a refusal final for one device, no devices skipped, a long body cut) and its API (config, idempotent subscribe, refused endpoints and keys, remove, test pushes). A real push to a phone cannot be tested automatically. `pkg/notify` is tested against an in-process server.

```sh
cd services/notifications
NOTIFICATIONS_TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:55441/postgres?sslmode=disable' GOWORK=off go test -count=1 ./...
```
