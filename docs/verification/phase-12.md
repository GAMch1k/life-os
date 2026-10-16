# Phase 12 verification

Phase 12 gives Life OS one way to tell its owner something. Producers (Calendar, Tasks, Inbox, the monitor, Finance) post to the [Notifications service](../services/notifications.md). It stores every notification for the Dashboard, records it in the Event Log, and delivers it through the channels configured on the server. The [monitor](../services/monitor.md) uses this path to report a Status check that breaks or recovers.

The ticket (P12-08) was written for ntfy. Since 12 Oct 2026 the phone gets notifications through the Life OS Android app instead, so the acceptance moved to app push. This sign-off on 16 Oct 2026 uses read-only evidence from the live stack (the Notifications and Activity APIs through the gateway, read-only SQL on `notifications_db`, `docker inspect` and `docker logs`), the existing tests and their last recorded runs, and facts the owner confirmed in chat from 12 to 16 Oct. No test was run for this report: the server is small and shared, and another job was using the verify stack.

## Delivery path

`LIFE_OS_NOTIFICATIONS_CHANNELS=webpush`. Notifications sends each notification by VAPID web push to the native Life OS Android app. The app receives it through UnifiedPush with the embedded FCM distributor and shows it on a channel chosen by importance: Urgent (`lifeos.urgent`), Reminders, or Information (see the app's README in `life-os-app`).

ntfy is **paused**. Its container has been stopped since 12 Oct 23:44:50 CEST (21:44:50 UTC), and its Compose profile, data volume, and settings are kept ([ntfy → Paused](../operations/ntfy.md#paused-since-2026-10-12)). On 16 Oct the owner decided to keep it that way.

| What | Live value (16 Oct, 20:24–20:30 CEST) |
| --- | --- |
| `GET /api/notifications/v1/channels` | One channel, `webpush`, state `ok`: last success 16 Oct 19:30:27 CEST (17:30:27 UTC), no failure ever, 0 failed in 24 h, 0 retrying, 0 pending. |
| `GET /api/notifications/v1/push/config` | `configured: true`, `delivering: true`. |
| `GET /api/notifications/v1/push/subscriptions` | One device, "Life OS app on Android", push service `fcm.googleapis.com`. Subscribed 12 Oct 23:39:01 CEST (21:39:01 UTC); last success 16 Oct 19:30:27 CEST; no failure. |
| `lifeos-notifications-1` | Created 12 Oct 23:44:32 CEST (21:44:32 UTC) for the channel switch, restart count 0. At start it logged `notification channels configured` with `[webpush]`; there are no warnings or errors since. |
| `lifeos-ntfy-1` | `exited (0)` since 12 Oct 23:44:50 CEST, restart count 0. |
| Status › Notifications | **ok**, "Delivering to webpush." Task reminders and Event reminders: "Sending normally". |

## Commands

These are the existing tests. None was re-run for this report.

| Command | What it proves |
| --- | --- |
| `cd services/notifications && NOTIFICATIONS_TEST_DATABASE_URL=… GOWORK=off go test ./...` | **Idempotency:** a known `dedupeKey` returns the original and stores, delivers and records nothing; concurrent sends with one key store one notification (`TestSendStoresTheNotificationAndDedupes`, `TestConcurrentSendsWithOneKeyStoreOneNotification`). **Worker:** an expired lease is claimed again (`TestExpiredLeaseIsClaimedAgain`), concurrent workers send each delivery once (`TestConcurrentWorkersSendEachDeliveryOnce`), and the retry, permanent and maximum-age paths work. **Web push:** the RFC 8291 example byte for byte, the VAPID token, per-device outcomes, 410 pruning, and a failing device retried alone without duplicates. |
| `cd services/monitor && GOWORK=off go test ./...` | A problem is high, a warning medium and a recovery low. A restart neither repeats nor loses an episode (`TestARestartNeitherRepeatsNorLosesAnEpisode`), and Notifications being down delays a change but never loses or repeats it. |
| Calendar and Tasks dispatcher integration tests (`internal/reminders`, `internal/notifications`) | Every effective reminder fires once (`TestEveryEffectiveReminderFiresOnce`, `TestTimedTasksRemindOnceBeforeTheyAreDue`). One due while the service was down is sent late or recorded as missed. One process runs at a time. Importance follows the event's priority. |
| `make browser-e2e` → `e2e/notifications.spec.ts` | The bell, its popover and `/notifications`, read state and filters. "A capture waiting in Human Review notifies the owner" (medium, `inbox.human_review`, `/inbox#human-review`). |
| `make browser-e2e` → `e2e/runtime-status.spec.ts` | "Calendar's, Tasks', and Food's reminder senders show their last check", and "Notifications is not set up on the verification stack, and that is fine". |

Last recorded runs: P12-09 on 10 Oct had the Tasks unit and integration tests, `tools/check`, and the browser suite 73 passed / 0 failed. P12-10 on 12 Oct, on `master`, had `tools/check` and the browser suite 117 passed / 0 failed, which included `notifications.spec.ts` and the since-removed `web-push.spec.ts`. A real push to a phone cannot be tested automatically.

## Required acceptance

| # | Required | Evidence |
| --- | --- | --- |
| 1 | The phone receives notifications through the current channel | Owner-confirmed (12–16 Oct): the phone receives app push. Live: all 38 `webpush` deliveries (since 12 Oct 23:43 CEST) were sent at the first attempt, 69–280 ms after creation, and the one device's last success is 16 Oct 19:30:27 CEST. `sent` means FCM accepted the push; that the phone showed it is the owner's confirmation. |
| 2 | Medium notifications from real producers arrive | Calendar's ZAW reminder, 15 Human Review captures, 10 Task due reminders, and an evening list since the switch. Details in [Real producers](#real-producers-seen-live). |
| 3 | Low notifications from real producers arrive | The morning summary "Your day" (`summary.morning`, low) was delivered every day from 13 to 16 Oct at 08:00:27 CEST (06:00:27 UTC), and on 12 Oct by ntfy. |
| 4 | High notifications arrive, loudly | **Partly.** No real producer has sent a high notification since the switch to app push. The last one was the monitor's "Services has a problem" on 10 Oct (by ntfy). High notifications over app push so far are tests: two developer tests on 12–13 Oct, among them "Urgent test (0.3.3)", and the owner's own **Try notifications** high on 15 Oct 15:07:52 CEST (13:07:52 UTC), `6cfd7c94`, sent. Owner-confirmed: on app 0.4.2 and later, urgent notifications use the long vibration pattern on channel `lifeos.urgent`. |
| 5 | The Dashboard list and the Event Log agree | 67 notifications and 67 `notification.created` events, one each, with the same title, importance, category and source. 68 sent deliveries and 68 `notification.delivered` events, one per notification and channel. No `notification.delivery_failed`. The server-rendered `/notifications` page lists the 50 newest, all 50 ids present, each with its delivery state ("webpush: sent 19:30"). Unread count: 30, of them 0 high. |
| 6 | Restarts lose nothing and duplicate nothing | See [Restarts and duplicates](#restarts-and-duplicates): no dedupe key twice, no second delivery or push for any notification, and no missed or failed producer sends. |
| 7 | Docs | [Notifications](../services/notifications.md) (channels, web push, producers), [ntfy](../operations/ntfy.md) (setup, rotation, the route, and the pause), [Monitor](../services/monitor.md), and this report. Stale passages are listed under [Follow-ups](#follow-ups). |

## Real producers seen live

All 67 notifications, 10 Oct 16:00 to 16 Oct 19:30 CEST: Tasks 21, Inbox 16, developer tools 13, the owner's Dashboard tests 6, the monitor 6, Calendar 4, and Finance 1. Real producers sent 17 by ntfy and 31 by app push (low 4, medium 27, high 0). Every delivery has `attempts: 1`.

| Producer | Notification | Importance | Created | Delivery | Event Log |
| --- | --- | --- | --- | --- | --- |
| Calendar (ZAW) | `8ef63828-ec5d-4660-b728-88cd3341123b`: "Papier Tonnen und Container", the day-before reminder for the pickup on 15 Oct 06:00 CEST. Its event `cf37db50…` comes from the ZAW feed, and its reminder is "1 day before at 19:00". | medium (the event's priority) | 14 Oct 19:00:22 CEST (17:00:22 UTC) | webpush sent, 1 attempt, 19:00:22.8 CEST | `notification.created` `91b48469…` by `system:calendar`; `notification.delivered` `17da3036…` (webpush, attempts 1). Calendar's `/v1/reminders/status` `lastSentAt` is the same instant. |
| Calendar | 3 reminders on 10 Oct (17:39, 18:00, 19:30 CEST): one of a Google-synced event, two of one Life OS event | medium | 10 Oct | ntfy sent, 1 attempt each | created and delivered, one each |
| Tasks | Morning summary "Your day": `56d4b00f` (13 Oct), `e65f8070` (14 Oct), `a4800882` (15 Oct), `31c78cdf` (16 Oct); earlier `118b4a22` (12 Oct 08:00:08 CEST, ntfy) | low | 08:00:27 CEST (06:00:27 UTC) each day | webpush sent, 1 attempt | for 16 Oct: `878aedd3…` created by `system:tasks`, `21d4dbd4…` delivered |
| Tasks | 13 due reminders (`tasks.due`, 30 min before), the morning list, and 2 evening lists | medium | 11–16 Oct | sent, 1 attempt each | one created and one delivered event each |
| Inbox | 16 "Needs your review" (`inbox.human_review`). First `0c87c091` on 10 Oct 16:45:46 CEST (ntfy). Since the switch 15, the latest `8adcb341-b8c7-455e-ac4d-d6ad7344cc3c` on 16 Oct 13:33:41 CEST (11:33:41 UTC) | medium | 10–16 Oct | sent, 1 attempt each | Each matches exactly one `inbox.human_review.requested` for the same capture and attempt, 2–4 ms earlier. 22 requests since 10 Oct: the 6 without a notification are from before Inbox notifications went live (10 Oct 12:44–15:14 CEST). |
| Monitor | `bf0667d8` "Services has a problem" (Finance is not ready), then `a0d75735` "Services recovered" | high, then low | 10 Oct 16:36:49 and 16:38:49 CEST | ntfy sent, 1 attempt | created by `system:monitor`, delivered |
| Monitor | 4 `status.server` (the forgejo runner's restart loop, see [Phase 17](phase-17.md)) | medium, low | 11–12 Oct | ntfy sent, 1 attempt | created and delivered |
| Finance | `8544735a` low balance | medium | 12 Oct 15:53:14 CEST | ntfy sent, 1 attempt | created and delivered |

## Restarts and duplicates

- **Containers.** `lifeos-notifications-1`: restart count 0 since 12 Oct 23:44:32 CEST. The containers replaced by the deploys of 10 and 12 Oct are gone, but the database spans them. `lifeos-monitor-1` has run since 10 Oct 16:14:49 CEST with restart count 0. Its log has 6 `status changed` lines, all notified, and 4 "runtime status unreachable" warnings during Dashboard redeploys on 10 Oct; none held for two evaluations, so none was notified.
- **SQL on `notifications_db`** (read-only, as `notifications_user`):
  - 67 notifications, 67 distinct dedupe keys.
  - No notification has two deliveries for one channel.
  - Deliveries: ntfy 30 sent, webpush 38 sent and 1 `skipped` ("no device has turned on notifications", a minute before the app subscribed). Every one at attempt 1; no lease is held.
  - `push_outcomes`: 38 `sent` for one device, never twice for a notification.
  - Migrations at version 3, not dirty.
- **Content.** Only one pair of notifications has the same category, title and body within 6 hours: the owner's two medium **Try notifications** 50 s apart on 10 Oct, two clicks with two keys. Task reminders with the same title on consecutive days name different due times of one Task that was moved, for example `51169212` and `0f122dcb` for due times on 15 and 16 Oct at 19:00 CEST.
- **Logs.** Since its start, the current container answered `POST /v1/notifications` 37 times with 201 and never with 200: no producer had to replay a send.
- **Producers.** The Event Log has no `calendar.reminder.missed`, `tasks.notification.missed`, `food.notification.missed`, or `inbox.notification.failed`.

No live restart was made for this report. Restart safety rests on the tests above and on these records across the deploys of 10–12 Oct.

## Owner-confirmed facts (chat, 12–16 Oct 2026)

- The phone receives app push.
- Urgent notifications use a long vibration pattern on channel `lifeos.urgent` (app 0.4.2 and later; the channel came with `life-os-app` commit `80aeeb6` on 12 Oct).
- Push does not arrive on the owner's work Wi-Fi, because FCM is blocked there. This is known, and the owner accepts it.

## Results

- Since 12 Oct 23:44 CEST every real notification has gone to the phone through the app, at the first attempt, with no failure.
- Medium (Calendar, Inbox, Tasks) and low (morning summary) notifications from real producers were delivered live. High over app push has so far been tests only.
- The notification list, the Dashboard page, and the Event Log agree one to one.
- No duplicates: no dedupe key, delivery, or device push twice.

**Verdict: accepted with follow-ups.**

## Follow-ups

- **A real high notification over app push.** None has happened since the switch. The next real outage the monitor reports will show it. A deliberate check (stopping a service the owner can spare, as in [Monitor → Operations](../services/monitor.md#operations)) needs the owner's go, because test pings to the owner are stopped.
- **Work Wi-Fi.** FCM is blocked there, and ntfy was blocked there before. Nothing in Life OS can fix this; it is accepted. Not verified: FCM keeps a push for its TTL (high 24 h, medium 12 h, low 4 h), so a push the phone missed at work may still arrive after it leaves that network.
- **Stale docs**, from the reverted Dashboard push (P12-10, `3308f7b`):
  - [Dashboard](../frontend/dashboard.md) still describes a device list with **Send test** and **Remove** ("On Android nothing else is needed: …"), and says the service worker "shows web push notifications". `public/sw.js` has no push handler.
  - [Notifications → Operations](../services/notifications.md#operations) still cites `e2e/web-push.spec.ts`, which was removed.
  - "Life OS's own Android app later becomes another channel" is now the current channel.
- **Status wording.** The Notifications card names the channel by its key, "webpush". "App push (phone)" would read better.
- **The ticket's ntfy steps** (a test ntfy topic on the verify stack, a real-phone ntfy check) no longer apply while ntfy is paused. They return if the owner resumes ntfy (`LIFE_OS_NOTIFICATIONS_CHANNELS=ntfy,webpush`).
