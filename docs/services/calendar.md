# Calendar service

Calendar owns events and calendar sources in `calendar_db` and is their only source of truth: clients (the Dashboard, MCP, the Inbox processor) use its API, never its database. Gateway base: `/api/calendar`. The contract is `services/calendar/api/openapi.yaml`; `/health` and `/ready` are available.

## Events

An event has a `title`, optional `description` (notes/details) and `location`, its time (below), optional `recurrence` and `reminders`, a `status`, and a `source`. Calendar Events are not Tasks.

**Time.** A timed event (`allDay: false`) has `startAt`/`endAt`, RFC 3339 instants: any offset is accepted, and they are stored and returned in UTC (`2026-11-17T09:30:00+01:00` comes back as `2026-11-17T08:30:00Z`); `endAt` must be after `startAt`. It may carry `timeZone`, the IANA zone it was scheduled in (for example an ICS `TZID`), as a display hint. An all-day event (`allDay: true`) has `startDate`/`endDate`, local dates with `endDate` inclusive (a one-day event has both equal; ICS and FullCalendar use an exclusive end, so adapters subtract or add a day). All-day dates mean those days in the owner's timezone and take no `timeZone`.

**Owner's timezone.** Reads that compare all-day dates with instants (listing, search) take a `timeZone` parameter and default to `LIFE_OS_TIMEZONE` (set for the Calendar container in `compose.yaml`; unset or unknown means UTC, logged at startup). An all-day event spans midnight of its first day to midnight after its last day in that zone, so daylight-saving changes are respected. The Dashboard passes the timezone it shows the owner.

**Status.** `active` or `cancelled`. Cancelling (`PATCH {"status":"cancelled"}`) keeps the event as history: it stays readable by id but leaves listings and search unless `includeCancelled=true`; `cancelledAt` records when. `PATCH {"status":"active"}` restores it. `DELETE` removes an event permanently.

**Source.** `source.kind` is `manual` (created in Life OS), `ics` (an ICS/iCal feed), or `google` (Google Calendar). Imported events also carry `source.sourceId` (the Calendar source they came from, see [Sources](#sources-ics-feeds)) and `source.externalUid` (their identity there: the ICS `UID`, or `<UID>#<RECURRENCE-ID>` for one occurrence of a recurring ICS event); a source holds each `externalUid` once, so refreshes update in place. The API only creates `manual` events (a `source` in a request body is ignored) and updates never change the source. **Imported events are read-only:** a `PATCH` that names anything but `reminders`, and any `DELETE`, return 409 `managed_by_source`; only their source changes, cancels (`removedFromSourceAt`), and restores them. The legacy `externalReference` (`provider`/`externalId`) is still accepted and returned but deprecated.

**Reminders** are stored intent only; Calendar delivers no notifications. Each reminder is either `{"kind":"relative","minutesBefore":30}` (before the start; an all-day event starts at owner-local midnight) or `{"kind":"localTime","daysBefore":1,"localTime":"19:00"}` (at 19:00 on the owner's wall clock, one calendar day before the event's start date, resolved in the owner's timezone when due, so a pickup on 17 November at 06:00 reminds on 16 November at 19:00, whatever daylight saving does). At most 10 per event. `reminders: null` (the default) means "follow the defaults" — for imported events their source's `defaultReminders` — while `[]` means explicitly none. Every returned event also has `effectiveReminders`: its own reminders when set, else its source's defaults, else none.

**Recurrence** (`daily`/`weekly`/`monthly`/`yearly`, `interval`, optional `until`) is stored once with the event and follows iCalendar (RFC 5545) rules: a timed event keeps its wall-clock time in its `timeZone` (else the owner's) across daylight-saving changes, a monthly event on the 31st skips shorter months, and `until` is the latest start of an occurrence. Listing and search **expand** recurring events (below); reading, updating, or deleting by id works on the stored recurring event, so a change applies to every occurrence.

## Listing, search, and occurrences

`GET /v1/events?from&to` returns the events overlapping `[from, to)`, soonest start first. A recurring event appears once per occurrence that overlaps the range, in its place in the order: the item is the recurring event (same `id`, title, recurrence, and so on) with that occurrence's `startAt`/`endAt` (or `startDate`/`endDate`) and `occurrence: {seriesId, recurrenceId}`, where `recurrenceId` is the occurrence's start (a UTC instant, or a date for all-day events) and is unique within the series. Clients that key items should use `id` plus `occurrence.recurrenceId`. Non-recurring events are returned as stored, without `occurrence`. Paging (`limit`, `nextCursor`) runs over occurrences and events alike, so Home, MCP, and the Calendar UI see the same items.

`GET /v1/events/search?q` matches every word of `q` in the title, location, or description. A matching recurring event is returned as occurrences too: every occurrence in the range when both `from` and `to` are given, otherwise one item, its next occurrence that is not over yet (or its last one, if it has ended).

Recurring ICS events are not stored as rules: each occurrence is imported as its own event (see below), so they list like any other event.

## Sources (ICS feeds)

A **source** is an external feed whose events Calendar imports and keeps refreshed. Sources belong to the Calendar domain (registry plus adapter inside this service; Google Calendar will follow the same pattern). Today the only kind is `ics`.

A source has a `name` (the label shown with its events, such as "ZAW"), the feed `url` (https; `webcal://` is accepted and means https), `enabled`, a type `filter`, optional `defaultReminders`, and its read-only `refresh` state: `nextRefreshAt`, `lastAttemptAt`, `lastSuccessAt`, `lastOutcome` (`imported`, `not_modified`, `failed`), `lastError`, `consecutiveFailures`, `lastCounts` (feed events, imported, inserted, updated, removed, restored, unchanged, skipped), the `etag`/`lastModified` validators, and `refreshing`. Everything is stored in `calendar_sources`, so the schedule and state survive restarts.

**Filter.** A term matches an event when it equals one of its `CATEGORIES` or occurs in its `SUMMARY` (case-insensitive). With `filter.include` terms only matching events are imported; then events matching `filter.exclude` are dropped; empty lists import everything. For example a waste feed whose summaries name the waste type can keep `["Bioabfall", "Gelber Sack", "Papier"]`.

**Refresh.** Calendar refreshes every enabled source daily at a quiet hour in the owner's timezone (`CALENDAR_SOURCE_REFRESH_AT`, default 04:30), right away when a source is added, re-enabled, or gets a new URL or filter, and on demand with `POST /v1/sources/{id}/refresh` (the Dashboard's "Refresh now", which returns the source with the outcome). The scheduler inside the Calendar service checks for due sources every minute and refreshes one at a time. One refresh per source runs at a time: concurrent requests share it, and a lease in the database (`refreshing_until`) keeps other processes out until it is released or expires, so a crash mid-refresh cannot block a source. When the server sent an `ETag` or `Last-Modified`, the next refresh is conditional and a `304 Not Modified` changes nothing (`lastOutcome: not_modified`).

**Reconciliation** (one transaction per refresh, idempotent):

- an event whose UID is new is inserted (active, or cancelled if the feed says `STATUS:CANCELLED`);
- an event whose feed entry changed is updated in place (same id; the owner's reminder override stays);
- a **future** event that is no longer in the feed (or that the filter now excludes) is cancelled and marked `removedFromSourceAt`: it leaves active views but stays as history — never deleted, never silently kept active;
- a removed event whose UID comes back is restored (`active`, `removedFromSourceAt` and `cancelledAt` cleared);
- past events (started before the refresh) are left as they are, so feeds that list only upcoming dates do not cancel history.

**Failures** keep the last good data: an unreachable feed, an HTTP error, a response that is not a complete iCalendar document (an HTML error page, a cut-off download), or an empty calendar where there were upcoming events records `lastOutcome: failed` and `lastError`, and the source is retried after 15 minutes, doubling up to 6 hours but never later than the next daily slot. The next good refresh resets `consecutiveFailures`.

**Activity.** Each refresh records `calendar.source.refreshed` or `calendar.source.refresh_failed` in Activity (when `ACTIVITY_SERVICE_URL` is set), attributed to the source as `system:ics-<name>` (for example `system:ics-zaw`), with the counts or the error. The feed URL, which can carry personal data such as a street, is never put in Activity, logs, or `lastError`.

**ICS normalization.** Parsing uses [golang-ical](https://github.com/arran4/golang-ical) for the document structure and [rrule-go](https://github.com/teambition/rrule-go) for recurrence rules:

- `DTSTART;VALUE=DATE` events are all-day; the exclusive `DTEND` becomes an inclusive `endDate` (`DURATION` and a missing end are handled).
- `TZID` times are resolved as an IANA zone, through the `VTIMEZONE`'s `X-LIC-LOCATION`, as a Windows zone name, from an IANA name at the end of a longer identifier (`/mozilla.org/…/Europe/Berlin`), or from the `VTIMEZONE` rules themselves; the IANA name becomes the event's `timeZone`. UTC times are taken as is; floating times are in the calendar's `X-WR-TIMEZONE`, else the owner's timezone.
- A timed event without an end gets a nominal one-minute duration; `DURATION` days are nominal (same wall-clock time).
- A recurring event (`RRULE`, `RDATE`, `EXDATE`) is imported as one event per occurrence from a year back to a year ahead (at most 1,000 per event; the window moves with each refresh), keyed `<UID>#<RECURRENCE-ID>` with the occurrence's original start (`20261017T040000Z`, or `20261017` for all-day). An override (`RECURRENCE-ID`) replaces its occurrence; `STATUS:CANCELLED` cancels it.
- Unreadable events (no start, a repeated UID) are skipped and counted; an event without a `UID` is keyed by its start and summary. A feed may hold at most 10,000 events.

**Safe fetching.** Feeds are downloaded only over https and only from public addresses: after DNS resolution, every connection to a loopback, private (including the Docker networks), link-local (including cloud metadata at 169.254.169.254), unique-local, or other special-purpose address is refused, and every redirect (at most 5) is checked again. Downloads are capped (`CALENDAR_ICS_MAX_BYTES`, default 5 MiB, also after decompression) and time-limited (`CALENDAR_ICS_TIMEOUT_SECONDS`, default 30), and never go through an environment proxy. `CALENDAR_ICS_ALLOW_PRIVATE_URLS=true` lifts the address and https rules for local development and verification against a local feed only.

**Deleting** a source deletes the events imported from it; set `enabled: false` instead to stop refreshing and keep them.

## API

| Request | Behavior |
| --- | --- |
| `POST /v1/events` | Creates an active manual event (201). Requires `title` and `allDay` plus that kind's start and end. `Idempotency-Key`: an identical repeat returns the original event; reuse with a different body is 409. |
| `GET /v1/events/{id}` | The event, whatever its status. |
| `PATCH /v1/events/{id}` | Changes only the named fields; `null` clears a nullable field (`description`, `location`, `timeZone`, `recurrence`, `reminders`, and the start/end fields). Changing `allDay` switches kind: the other kind's fields are cleared and the new kind's start and end must be given. The merged event is validated as a whole; a concurrent change makes the update re-read (409 if it keeps changing). Imported events: only `reminders` (else 409 `managed_by_source`). |
| `DELETE /v1/events/{id}` | Deletes a manual event permanently (204); imported events return 409 `managed_by_source`. |
| `GET /v1/events?from&to` | Events and occurrences overlapping `[from, to)` (`to` must be after `from`), soonest start first. Optional `timeZone`, `includeCancelled`, `sourceKind`, `sourceId`, `limit` (1–100, default 50), and `cursor`. |
| `GET /v1/events/search?q` | Events whose title, description, or location contain every word of `q` (case-insensitive substring, up to 10 words), across all dates. Optional `from`/`to` (either may be open), `timeZone`, `includeCancelled`, `sourceKind`, `sourceId`, `limit` (1–100, default 20), and `cursor`. |
| `GET /v1/sources` | Every source with its refresh state. |
| `POST /v1/sources` | Adds a source (201): `name`, `url`, optional `enabled` (default true), `filter`, `defaultReminders`. Its first import is due right away. |
| `GET /v1/sources/{id}` | The source. |
| `PATCH /v1/sources/{id}` | Changes the named fields (`defaultReminders: null` removes them). A new `url` or `filter` drops the validators and schedules a full refresh now. |
| `DELETE /v1/sources/{id}` | Deletes the source and its events (204). |
| `POST /v1/sources/{id}/refresh` | Refreshes now and returns the source (200, whatever the outcome); 409 `source_disabled`, `refresh_in_progress`, or `conflict` (the source changed meanwhile). |

Search ranks a word found in the title 3, in the location 2, in the description 1, most relevant first; among equals, events not over yet come first (soonest first), then past ones (most recent first). Listing and search page with an opaque `nextCursor`: pass it back as `cursor` with the same parameters; no `nextCursor` means the last page. A search cursor also keeps the moment that split upcoming from past, so paging stays consistent while time passes.

Every write needs `X-Life-OS-Actor` (see [API overview](../api/overview.md)). Invalid requests return 400 with an error message saying what to fix; unknown ids return 404.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `LIFE_OS_TIMEZONE` | `UTC` | The owner's timezone (all-day matching, recurrence wall clock, refresh schedule, floating ICS times). |
| `CALENDAR_SOURCE_REFRESH_AT` | `04:30` | Daily source refresh time, HH:MM owner-local (compose passes `LIFE_OS_CALENDAR_SOURCE_REFRESH_AT`). |
| `ACTIVITY_SERVICE_URL` | empty | Activity service for refresh outcomes; empty records nothing. |
| `CALENDAR_ICS_MAX_BYTES` | `5242880` | Largest feed download. |
| `CALENDAR_ICS_TIMEOUT_SECONDS` | `30` | Time limit of one download. |
| `CALENDAR_ICS_ALLOW_PRIVATE_URLS` | `false` | Development only: allow http and private/loopback feed addresses. |

## Tests

`internal/api`, `internal/repository`, `internal/ics`, `internal/recurrence`, and `internal/sources` hold unit tests (parser edge cases on a sanitized waste-collection feed in `internal/ics/testdata`, time zones, the address guard, filters, reconciliation, schedule and backoff, recurrence across daylight saving) and integration tests. The integration tests run against the Postgres named by `CALENDAR_TEST_DATABASE_URL` (a throwaway database: each test creates and drops its own schema and applies every migration) and skip when it is unset; the source tests serve the feed from an in-process HTTP server. For example:

```sh
docker run -d --rm --name calendar-test-pg -e POSTGRES_PASSWORD=test -p 127.0.0.1:55441:5432 --tmpfs /var/lib/postgresql postgres:18.6-alpine
cd services/calendar
CALENDAR_TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:55441/postgres?sslmode=disable' GOWORK=off go test -count=1 ./...
docker rm -f calendar-test-pg
```
