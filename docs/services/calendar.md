# Calendar service

Calendar owns events in `calendar_db` and is their only source of truth: clients (the Dashboard, MCP, the Inbox processor) use its API, never its database. Gateway base: `/api/calendar`. The contract is `services/calendar/api/openapi.yaml`; `/health` and `/ready` are available.

## Events

An event has a `title`, optional `description` (notes/details) and `location`, its time (below), optional `recurrence` and `reminders`, a `status`, and a `source`. Calendar Events are not Tasks.

**Time.** A timed event (`allDay: false`) has `startAt`/`endAt`, RFC 3339 instants: any offset is accepted, and they are stored and returned in UTC (`2026-11-17T09:30:00+01:00` comes back as `2026-11-17T08:30:00Z`); `endAt` must be after `startAt`. It may carry `timeZone`, the IANA zone it was scheduled in (for example an ICS `TZID`), as a display hint. An all-day event (`allDay: true`) has `startDate`/`endDate`, local dates with `endDate` inclusive (a one-day event has both equal; ICS and FullCalendar use an exclusive end, so adapters subtract or add a day). All-day dates mean those days in the owner's timezone and take no `timeZone`.

**Owner's timezone.** Reads that compare all-day dates with instants (listing, search) take a `timeZone` parameter and default to `LIFE_OS_TIMEZONE` (set for the Calendar container in `compose.yaml`; unset or unknown means UTC, logged at startup). An all-day event spans midnight of its first day to midnight after its last day in that zone, so daylight-saving changes are respected. The Dashboard passes the timezone it shows the owner.

**Status.** `active` or `cancelled`. Cancelling (`PATCH {"status":"cancelled"}`) keeps the event as history: it stays readable by id but leaves listings and search unless `includeCancelled=true`; `cancelledAt` records when. `PATCH {"status":"active"}` restores it. `DELETE` removes an event permanently.

**Source.** `source.kind` is `manual` (created in Life OS), `ics` (an ICS/iCal feed), or `google` (Google Calendar). Imported events also carry `source.sourceId` (the Calendar source they came from) and `source.externalUid` (their identity there, such as the ICS `UID`); a source holds each `externalUid` once, so refreshes update in place. The API only creates `manual` events (a `source` in a request body is ignored) and updates never change the source: imported events are written by Calendar's own source adapters (P10-04). When a refresh no longer finds an imported event, the adapter cancels it and sets `removedFromSourceAt`; restoring it (by the source or the owner) clears both. The legacy `externalReference` (`provider`/`externalId`) is still accepted and returned but deprecated.

**Reminders** are stored intent only; Calendar delivers no notifications. Each reminder is either `{"kind":"relative","minutesBefore":30}` (before the start; an all-day event starts at owner-local midnight) or `{"kind":"localTime","daysBefore":1,"localTime":"19:00"}` (at 19:00 on the owner's wall clock, one calendar day before the event's start date, resolved in the owner's timezone when due, so a pickup on 17 November at 06:00 reminds on 16 November at 19:00, whatever daylight saving does). At most 10 per event. `reminders: null` (the default) means "follow the defaults" — for imported events the source's default, such as the ZAW feed's evening-before reminder — while `[]` means explicitly none.

**Recurrence** (`daily`/`weekly`/`monthly`/`yearly`, `interval`, optional `until`) is stored with the event. Listing returns a recurring event once, as stored; occurrences are not expanded yet.

## API

| Request | Behavior |
| --- | --- |
| `POST /v1/events` | Creates an active manual event (201). Requires `title` and `allDay` plus that kind's start and end. `Idempotency-Key`: an identical repeat returns the original event; reuse with a different body is 409. |
| `GET /v1/events/{id}` | The event, whatever its status. |
| `PATCH /v1/events/{id}` | Changes only the named fields; `null` clears a nullable field (`description`, `location`, `timeZone`, `recurrence`, `reminders`, and the start/end fields). Changing `allDay` switches kind: the other kind's fields are cleared and the new kind's start and end must be given. The merged event is validated as a whole; a concurrent change makes the update re-read (409 if it keeps changing). |
| `DELETE /v1/events/{id}` | Deletes permanently (204). |
| `GET /v1/events?from&to` | Events overlapping `[from, to)` (`to` must be after `from`), soonest start first. Optional `timeZone`, `includeCancelled`, `sourceKind`, `sourceId`, `limit` (1–100, default 50), and `cursor`. |
| `GET /v1/events/search?q` | Events whose title, description, or location contain every word of `q` (case-insensitive substring, up to 10 words), across all dates. Optional `from`/`to` (either may be open), `timeZone`, `includeCancelled`, `sourceKind`, `sourceId`, `limit` (1–100, default 20), and `cursor`. |

Search ranks a word found in the title 3, in the location 2, in the description 1, most relevant first; among equals, events not over yet come first (soonest first), then past ones (most recent first). Listing and search page with an opaque `nextCursor`: pass it back as `cursor` with the same parameters; no `nextCursor` means the last page. A search cursor also keeps the moment that split upcoming from past, so paging stays consistent while time passes.

Every write needs `X-Life-OS-Actor` (see [API overview](../api/overview.md)). Invalid requests return 400 with an error message saying what to fix; unknown ids return 404.

## Tests

`internal/api` and `internal/repository` hold unit tests and integration tests. The integration tests run against the Postgres named by `CALENDAR_TEST_DATABASE_URL` (a throwaway database: each test creates and drops its own schema and applies every migration) and skip when it is unset, for example:

```sh
docker run -d --rm --name calendar-test-pg -e POSTGRES_PASSWORD=test -p 127.0.0.1:55441:5432 --tmpfs /var/lib/postgresql postgres:18.6-alpine
cd services/calendar
CALENDAR_TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:55441/postgres?sslmode=disable' GOWORK=off go test -count=1 ./...
docker rm -f calendar-test-pg
```
