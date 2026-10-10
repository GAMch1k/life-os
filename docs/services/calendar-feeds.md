# Calendar feeds service

`calendar-feeds` is the integration that subscribes to ICS/iCal feeds, the owner's [ZAW waste calendar](#zaw-waste-collection) among them, and keeps their events in Calendar (P11-08). It is a separate, replaceable service: it owns everything feed-side, and it hands the events to Calendar only through Calendar's [integration API](calendar-integration-api.md). Calendar owns the events and the owner's settings of each feed's source; it downloads nothing itself.

- Code: `services/calendar-feeds`. Database: `calendar_feeds_db` (user `calendar_feeds_user`, password `LIFE_OS_CALENDAR_FEEDS_DB_PASSWORD`).
- Contract: `services/calendar-feeds/api/openapi.yaml`. Gateway base: `/api/integrations/calendar-feeds`, and the Dashboard's proxy route of the same path. `/health` and `/ready` are available.
- Actor towards Calendar: `system:calendar-feeds`.

## Who owns what

| calendar-feeds (`calendar_feeds_db.feeds`) | Calendar (`calendar_sources`, `events`) |
| --- | --- |
| The feed URL (it can name a street: never logged), the type filter, the preset (`zaw`) and the preset configuration last applied | The source: its id, `externalId` (the feed's id), provider label (`ZAW`, `ICS`), read-only |
| The refresh schedule and its lease, the conditional-request validators (`ETag`, `Last-Modified`), the last outcome, counts, error, and consecutive failures | The owner's settings: name, `enabled`, default reminders, default priority (`PATCH /v1/sources/{id}`) |
| The download guard and limits, parsing, and the filter | The events (`source.kind` `external`), the owner's reminders and priority on them, reconciliation |
| Activity: one `calendar.source.refreshed` / `refresh_failed` per refresh | Nothing in Activity for a sync |

Each feed is one Calendar source, registered as `PUT /v1/integrations/calendar-feeds/sources/{feedId}` with `readOnly: true` and provider `ZAW` for the ZAW preset, `ICS` otherwise. A feed from before this service (handed over, see [Handoff](#handoff-from-calendar)) keeps its Calendar source's id as its own id, so `externalId` equals the source id; a feed added since has its own id, and `sourceId` says which Calendar source it is.

The owner's switch (`enabled`) and the source's name are Calendar's. calendar-feeds mirrors them: once a minute its scheduler lists the integration's sources in Calendar and records each feed's name and `enabled`, so a source switched back on is refreshed within a minute, and a disabled one is not downloaded. A sync Calendar refuses because the source was disabled meanwhile (409 `source_disabled`) is not a failure of the feed. A source deleted in Calendar (`DELETE /v1/sources/{id}`, which deletes its events) takes its feed with it at the next refresh; a preset feed comes back at the next start.

## API

| Request | Behavior |
| --- | --- |
| `GET /v1/feeds` | Every feed, oldest first, with its configuration and refresh state. |
| `POST /v1/feeds` | `{name, url, filter?, defaultReminders?, defaultPriority?}`: subscribes (201). The source is registered in Calendar first, with the default reminders and priority given here (then the owner's settings there); nothing is added when Calendar cannot register it (502 `calendar_unavailable`) or refuses the settings (400). The first refresh is due right away. |
| `GET /v1/feeds/{feedId}` | The feed. |
| `PATCH /v1/feeds/{feedId}` | `{url?, filter?}`: a new URL or filter drops the validators and is due right away; events the filter now excludes are reconciled like events that left the feed. The name, enabled, and defaults are changed in Calendar (`PATCH /api/calendar/v1/sources/{sourceId}`). |
| `DELETE /v1/feeds/{feedId}` | Unsubscribes (204): deletes the Calendar source with its events, then the feed; nothing is deleted while Calendar cannot be reached (502). |
| `POST /v1/feeds/{feedId}/refresh` | "Refresh now": asks Calendar for the source's current name and `enabled` first, refreshes, and returns the feed (200 whatever the outcome; read `refresh.lastOutcome`). 409 `source_disabled`, `refresh_in_progress`, or `conflict` (the feed changed meanwhile); 404 when the source was deleted in Calendar. |

A feed has `id`, `sourceId`, `name` and `enabled` (as Calendar last reported them), `url` (https; `webcal://` is stored as https), `filter`, `preset`, and its read-only `refresh` state: `nextRefreshAt`, `lastAttemptAt`, `lastSuccessAt`, `lastOutcome` (`imported`, `not_modified`, `failed`), `lastError`, `consecutiveFailures`, `lastCounts` (feed events, imported, inserted, updated, removed, restored, unchanged, skipped), `etag`/`lastModified`, and `refreshing`. Every write needs `X-Life-OS-Actor`.

**Filter.** A term matches an event when it equals one of its `CATEGORIES` or occurs in its `SUMMARY` (case-insensitive). With `filter.include` terms only matching events are imported; then events matching `filter.exclude` are dropped; empty lists import everything.

## Refresh

calendar-feeds refreshes every enabled feed daily at a quiet hour in the owner's timezone (`CALENDAR_FEEDS_REFRESH_AT`, default 04:30), right away when a feed is added, re-enabled, or gets a new URL or filter, and on demand. Its scheduler checks for due feeds every minute and refreshes one at a time. One refresh per feed runs at a time: concurrent requests share it, and a lease in the database (`refreshing_until`) keeps other processes out until it is released or expires, so a crash mid-refresh cannot block a feed. When the server sent an `ETag` or `Last-Modified`, the next refresh is conditional and a `304 Not Modified` syncs nothing (`lastOutcome: not_modified`).

A refresh downloads the feed, parses it (the ZAW preset strips the location suffix from titles first), applies the filter, and sends Calendar one **snapshot** of the kept events (`POST /v1/integrations/calendar-feeds/sources/{feedId}/sync`):

- `window: {"upcomingOnly": true}`: feeds often list only upcoming dates, so a stored event that already started is neither removed nor rewritten unless the feed moves it into the future;
- `emptyGuard: true` when the feed has no `VEVENT` at all: an empty calendar where there were upcoming events is far more likely an upstream fault, so Calendar refuses it (409 `empty_snapshot`) and the refresh fails with "the feed contains no events; keeping the events from the last refresh";
- items carry no reminders and no priority, so the owner's own stay; an item Calendar's validation would refuse (such as a UID over 2,048 bytes) is left out and counted as skipped, so one odd event cannot fail the whole feed.

Calendar reconciles the snapshot as it did the feed itself: a new UID is inserted, a changed one updated in place (same id), a future event no longer in the feed (or now filtered out) cancelled and marked `removedFromSourceAt`, a removed one that comes back restored, past events left alone. `lastCounts` adds the feed's own counts (events in the feed, kept by the filter, skipped) to what the sync changed.

If the feed's URL or filter changes while a refresh runs, the result is not synced and the feed is due again at once (409 `conflict` for "Refresh now").

**Failures** keep the last good data: an unreachable feed, an HTTP error, a response that is not a complete iCalendar document, an empty calendar where there were upcoming events, or Calendar not taking the events (unreachable, or still refreshing the feed itself before its handover migration) records `lastOutcome: failed` and `lastError`, and the feed is retried after 15 minutes, doubling up to 6 hours but never later than the next daily slot. The next good refresh resets `consecutiveFailures`. A failed refresh keeps the previous validators, so the next one downloads in full.

**ICS normalization** is unchanged from Calendar's: [golang-ical](https://github.com/arran4/golang-ical) for the document, [rrule-go](https://github.com/teambition/rrule-go) for rules; `DTSTART;VALUE=DATE` events are all-day with an inclusive `endDate`; `TZID` times resolve to an IANA zone (through `X-LIC-LOCATION`, Windows names, a trailing IANA name, or the `VTIMEZONE` rules) that becomes the event's `timeZone`; floating times are in `X-WR-TIMEZONE`, else the owner's; a timed event without an end gets one minute; a recurring event is imported as one event per occurrence from a year back to a year ahead (at most 1,000 per event), keyed `<UID>#<RECURRENCE-ID>`; an event without a `UID` is keyed by its start and summary; a feed may hold at most 10,000 events. The external uids are exactly those Calendar used, so the handover imports nothing twice.

**Safe fetching.** Feeds are downloaded only over https and only from public addresses: after DNS resolution, every connection to a loopback, private (including the Docker networks), link-local (including cloud metadata at 169.254.169.254), unique-local, or other special-purpose address is refused, and every redirect (at most 5) is checked again. Downloads are capped (`CALENDAR_FEEDS_MAX_BYTES`, default 5 MiB, also after decompression) and time-limited (`CALENDAR_FEEDS_TIMEOUT_SECONDS`, default 30), and never go through an environment proxy. `CALENDAR_FEEDS_ALLOW_PRIVATE_URLS=true` lifts the address and https rules for local development and the verification stack's feed fixture only.

## Activity

Each refresh records `calendar.source.refreshed` or `calendar.source.refresh_failed` in Activity (when `ACTIVITY_SERVICE_URL` is set), with source `calendar-feeds`, subject `calendar_source` and the feed's Calendar source id, attributed to the feed as `system:ics-<name>` (for example `system:ics-zaw`), with the counts or the error, `sourceName`, `feedId`, `trigger` (`scheduled` or `manual`), and `requestedBy` for "Refresh now". It is one summary event per refresh, also when nothing changed, so the Event Log (domain "Calendar feeds") shows that the daily refresh ran; the imported events get no events of their own, and Calendar records nothing for a sync. The feed URL is never put in Activity, logs, or `lastError`.

## ZAW waste collection

ZAW's Abfallkalender is served by MyMüll, which exports a personal iCal feed per street (`https://service.mymuell.de/api.php?m=zaw&r=ical&city_id=<city>&area_id=<area>&reminder=0&time=&city_name=<town>&street_name=<street>`): one timed event per pickup at 06:00 Europe/Berlin, the waste type only in `SUMMARY`, followed by the address (`Bioabfall - <town>, <street>`), which is also the `LOCATION`. The server sends no `ETag` or `Last-Modified`, so every daily refresh is a full download. ZAW is an ordinary feed plus a preset.

**Preset.** When `LIFE_OS_ZAW_ICS_URL` is set (the URL names a street, so it lives only in `.env` and is never logged; the service reads exactly this variable, which the verification stack blanks), calendar-feeds ensures at every start, before its scheduler runs, that exactly one feed has `preset: "zaw"`:

- if there is none, a feed added through the API with the same URL is taken over; otherwise a new one is added, due right away;
- the preset sets the URL and `filter.include` `["Bioabfall", "Gelber Sack", "Papier", "Restmüll Tonnen und Container 14-täglich"]` here, and registers the source in Calendar with the name "ZAW", provider `ZAW`, and default reminders `[{"kind":"localTime","daysBefore":1,"localTime":"19:00"}]` (the evening before each pickup);
- the configuration last applied is stored with the feed (`preset_config`). A later start changes only what the configuration changed since (a new URL in `.env`, or a release that changes the types or reminders); a new URL or filter makes the feed due right away. A changed name or reminders is registered again, and Calendar applies only what the integration changed, so the owner's own name, reminders, and priority (set in Calendar) stay, as does `enabled`;
- concurrent starts are serialized (an advisory lock and a unique index on `preset`), so restarts never add a second feed; and a new ZAW feed is never registered while Calendar still has feeds this service does not know (the handoff has not run), so the pickups are never imported twice.

Unset, calendar-feeds leaves an existing ZAW feed as it is. Deleting it while the variable is set brings it back at the next start. For this feed only, the trailing ` - <town>, <street>` is removed from each title when it repeats the location (before filtering), so the events read `Bioabfall`, `Gelber Sack`, `Papier Tonnen und Container`, `Restmüll Tonnen und Container 14-täglich`.

## Handoff from Calendar

Until P11-08 Calendar refreshed the feeds itself. The release hands them over:

1. Calendar's migration 000015 (`calendar_feeds_handover`) turns the feeds' sources and events into kind `external`, out of process. The legacy feed columns of `calendar_sources` (URL, filters, preset, refresh state) stay until P11-09.
2. `calendar-feeds handoff` (once) reads those legacy columns from `calendar_db`, read-only, and adds each feed to `calendar_feeds_db` under its Calendar source's id, with its configuration and refresh state (the lease excepted), marked to be registered again before its first refresh. It reads the legacy columns, not `integration_config`, because only they hold the URL, filters, and refresh state. It is idempotent (a feed already present stays as it is), writes in one transaction, replaces a ZAW feed this service added too early and never registered, refuses a second registered one, and prints counts only.

The order, commands, and checks are in [Calendar feeds handoff](../operations/calendar-feeds-handoff.md).

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `DATABASE_URL` | required | `calendar_feeds_db`. |
| `CALENDAR_SERVICE_URL` | required | Calendar (compose: `http://calendar:8080`). |
| `LIFE_OS_TIMEZONE` | `UTC` | The owner's timezone: the daily refresh time and floating feed times. |
| `CALENDAR_FEEDS_REFRESH_AT` | `04:30` | Daily refresh time, HH:MM owner-local (compose passes `LIFE_OS_CALENDAR_SOURCE_REFRESH_AT`, the variable Calendar used). |
| `CALENDAR_FEEDS_MAX_BYTES` | `5242880` | Largest feed download (1024 to 104857600). |
| `CALENDAR_FEEDS_TIMEOUT_SECONDS` | `30` | Time limit of one download (1 to 300). |
| `CALENDAR_FEEDS_ALLOW_PRIVATE_URLS` | `false` | Development and the verification overlay only: allow http and private/loopback feed addresses. |
| `LIFE_OS_ZAW_ICS_URL` | empty | The owner's MyMüll iCal feed for the ZAW preset; empty configures none. |
| `ACTIVITY_SERVICE_URL` | empty | Activity for refresh outcomes; empty records nothing. |
| `HANDOFF_CALENDAR_DATABASE_URL` | empty | `calendar-feeds handoff` only: Calendar's database, read only. |

`CALENDAR_ICS_MAX_BYTES`, `CALENDAR_ICS_TIMEOUT_SECONDS`, `CALENDAR_ICS_ALLOW_PRIVATE_URLS`, `CALENDAR_SOURCE_REFRESH_AT`, and `CALENDAR_ZAW_ICS_URL` were Calendar's and are gone.

## Tests

Unit tests: `internal/ics` (the parser on a sanitized waste-collection feed, time zones, recurrence, the address guard, size and time limits, conditional requests, filters, the ZAW title clean-up), `internal/feeds` (schedule and backoff, the source actor, the ZAW type selection), `internal/config`. Integration tests run against the Postgres named by `CALENDAR_FEEDS_TEST_DATABASE_URL` (each test in its own schema) and an in-process fake of Calendar's integration API (`internal/calendar/fakecalendar`, which registers and reconciles like Calendar does for a feed); they skip without the variable:

- `internal/api`: import and refresh (counts, schedule, updates in place, removal, past events kept, restore, Activity without the URL, every call as `system:calendar-feeds`), failures keeping the last good data (HTTP, HTML, empty feed, truncated, Calendar unreachable), conditional and restart-safe refresh with the lease, configuration (filter, rename and disable in Calendar, validation, delete), Calendar unreachable or refusing on create, a source deleted in Calendar, the ZAW preset (restarts, owner settings kept, a new URL, a release changing types and reminders, taking over a hand-added feed, URLs never in errors), and the handover order (no second ZAW source before the handoff, no sync while Calendar still refreshes a feed);
- `internal/handoff`: Calendar's migrations up to 000014 in a throwaway schema with seeded feeds, the handoff, a second run, Calendar's handover migration, and the replacement and refusal of early ZAW feeds;
- `internal/testdb`: the migrations down and up.

```sh
cd services/calendar-feeds
CALENDAR_FEEDS_TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:55441/postgres?sslmode=disable' GOWORK=off go test -p 2 -count=1 ./...
```
