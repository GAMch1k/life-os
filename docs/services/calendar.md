# Calendar service

Calendar owns events and calendar sources in `calendar_db` and is their only source of truth: clients (the Dashboard, MCP, the Inbox processor) use its API, never its database. Gateway base: `/api/calendar`. The contract is `services/calendar/api/openapi.yaml`; `/health` and `/ready` are available.

## Events

An event has a `title`, optional `description` (notes/details) and `location`, its time (below), optional `recurrence` and `reminders`, a `status`, and a `source`. Calendar Events are not Tasks.

**Time.** A timed event (`allDay: false`) has `startAt`/`endAt`, RFC 3339 instants: any offset is accepted, and they are stored and returned in UTC (`2026-11-17T09:30:00+01:00` comes back as `2026-11-17T08:30:00Z`); `endAt` must be after `startAt`. It may carry `timeZone`, the IANA zone it was scheduled in (for example an ICS `TZID`), as a display hint. An all-day event (`allDay: true`) has `startDate`/`endDate`, local dates with `endDate` inclusive (a one-day event has both equal; ICS and FullCalendar use an exclusive end, so adapters subtract or add a day). All-day dates mean those days in the owner's timezone and take no `timeZone`.

**Owner's timezone.** Reads that compare all-day dates with instants (listing, search) take a `timeZone` parameter and default to `LIFE_OS_TIMEZONE` (set for the Calendar container in `compose.yaml`; unset or unknown means UTC, logged at startup). An all-day event spans midnight of its first day to midnight after its last day in that zone, so daylight-saving changes are respected. The Dashboard passes the timezone it shows the owner.

**Status.** `active` or `cancelled`. Cancelling (`PATCH {"status":"cancelled"}`) keeps the event as history: it stays readable by id but leaves listings and search unless `includeCancelled=true`; `cancelledAt` records when. `PATCH {"status":"active"}` restores it. `DELETE` removes an event permanently.

**Source.** `source.kind` is `manual` (created in Life OS), `ics` (an ICS/iCal feed), or `google` (Google Calendar). Imported events also carry `source.sourceId` (the Calendar source they came from, see [Sources](#sources-ics-feeds)), `source.name` (that source's current name, such as "ZAW", which the Dashboard shows as the event's origin), and `source.externalUid` (their identity there: the ICS `UID`, or `<UID>#<RECURRENCE-ID>` for one occurrence of a recurring ICS event); a source holds each `externalUid` once, so refreshes update in place. The API only creates `manual` events (a `source` in a request body is ignored) and updates never change the source. **Imported events are read-only:** a `PATCH` that names anything but `reminders`, and any `DELETE`, return 409 `managed_by_source`; only their source changes, cancels (`removedFromSourceAt`), and restores them. The legacy `externalReference` (`provider`/`externalId`) is still accepted and returned but deprecated.

**Reminders** are stored intent only; Calendar delivers no notifications. Each reminder is either `{"kind":"relative","minutesBefore":30}` (before the start; an all-day event starts at owner-local midnight) or `{"kind":"localTime","daysBefore":1,"localTime":"19:00"}` (at 19:00 on the owner's wall clock, one calendar day before the event's start date, resolved in the owner's timezone when due, so a pickup on 17 November at 06:00 reminds on 16 November at 19:00, whatever daylight saving does). They work like Google Calendar's notifications: an event follows its calendar's default reminders while `useDefaultReminders` is true (the default) and adds any number of its own (`reminders`, at most 10) on top, so "1 day before, 3 hours before, 30 minutes before" can all apply at once. The defaults are an imported event's source's `defaultReminders` and, for an event created in Life OS, the owner's defaults from [settings](#settings), one list for timed and one for all-day events. Every returned event has `reminders` (its own), `useDefaultReminders`, `defaultReminders` (the defaults for this event, whether it uses them or not), and `effectiveReminders`: the defaults when used, then its own, each reminder once. A notifier will trigger every effective reminder.

### Settings

`GET /v1/settings` returns the owner's default reminders for the events they create, `defaultReminders.timed` and `defaultReminders.allDay` (at most 10 each, none until first set), and `updatedAt` (null until the first change). `PATCH /v1/settings` replaces the lists it names, for example `{"defaultReminders":{"timed":[{"kind":"relative","minutesBefore":30}]}}`; every Life OS event that uses its defaults follows. The settings live in `calendar_settings`, a single row. Changing them, like changing a source's configuration, is not recorded in Activity.

**Recurrence** (`daily`/`weekly`/`monthly`/`yearly`, `interval`, optional `until`) is stored once with the event and follows iCalendar (RFC 5545) rules: a timed event keeps its wall-clock time in its `timeZone` (else the owner's) across daylight-saving changes, a monthly event on the 31st skips shorter months, and `until` is the latest start of an occurrence. Listing and search **expand** recurring events (below); reading, updating, or deleting by id works on the stored recurring event, so a change applies to every occurrence.

## Listing, search, and occurrences

`GET /v1/events?from&to` returns the events overlapping `[from, to)`, soonest start first. A recurring event appears once per occurrence that overlaps the range, in its place in the order: the item is the recurring event (same `id`, title, recurrence, and so on) with that occurrence's `startAt`/`endAt` (or `startDate`/`endDate`) and `occurrence: {seriesId, recurrenceId}`, where `recurrenceId` is the occurrence's start (a UTC instant, or a date for all-day events) and is unique within the series. Clients that key items should use `id` plus `occurrence.recurrenceId`. Non-recurring events are returned as stored, without `occurrence`. Paging (`limit`, `nextCursor`) runs over occurrences and events alike, so Home, MCP, and the Calendar UI see the same items.

`GET /v1/events/search?q` matches every word of `q` in the title, location, or description. A matching recurring event is returned as occurrences too: every occurrence in the range when both `from` and `to` are given, otherwise one item, its next occurrence that is not over yet (or its last one, if it has ended).

Recurring ICS events are not stored as rules: each occurrence is imported as its own event (see below), so they list like any other event.

## Sources (ICS feeds)

A **source** is an external feed whose events Calendar imports and keeps refreshed. Sources belong to the Calendar domain (registry plus adapter inside this service; Google Calendar will follow the same pattern). Today the only kind is `ics`.

A source has a `name` (the label shown with its events, such as "ZAW"), the feed `url` (https; `webcal://` is accepted and means https), `enabled`, a type `filter`, optional `defaultReminders`, `preset` (read-only: set for a source Calendar configures itself, see [ZAW waste collection](#zaw-waste-collection)), and its read-only `refresh` state: `nextRefreshAt`, `lastAttemptAt`, `lastSuccessAt`, `lastOutcome` (`imported`, `not_modified`, `failed`), `lastError`, `consecutiveFailures`, `lastCounts` (feed events, imported, inserted, updated, removed, restored, unchanged, skipped), the `etag`/`lastModified` validators, and `refreshing`. Everything is stored in `calendar_sources`, so the schedule and state survive restarts.

**Filter.** A term matches an event when it equals one of its `CATEGORIES` or occurs in its `SUMMARY` (case-insensitive). With `filter.include` terms only matching events are imported; then events matching `filter.exclude` are dropped; empty lists import everything. For example a waste feed whose summaries name the waste type can keep `["Bioabfall", "Gelber Sack", "Papier"]`.

**Refresh.** Calendar refreshes every enabled source daily at a quiet hour in the owner's timezone (`CALENDAR_SOURCE_REFRESH_AT`, default 04:30), right away when a source is added, re-enabled, or gets a new URL or filter, and on demand with `POST /v1/sources/{id}/refresh` (the Dashboard's "Refresh now", which returns the source with the outcome). The scheduler inside the Calendar service checks for due sources every minute and refreshes one at a time. One refresh per source runs at a time: concurrent requests share it, and a lease in the database (`refreshing_until`) keeps other processes out until it is released or expires, so a crash mid-refresh cannot block a source. When the server sent an `ETag` or `Last-Modified`, the next refresh is conditional and a `304 Not Modified` changes nothing (`lastOutcome: not_modified`).

**Reconciliation** (one transaction per refresh, idempotent):

- an event whose UID is new is inserted (active, or cancelled if the feed says `STATUS:CANCELLED`);
- an event whose feed entry changed is updated in place (same id; the owner's reminders stay);
- a **future** event that is no longer in the feed (or that the filter now excludes) is cancelled and marked `removedFromSourceAt`: it leaves active views but stays as history — never deleted, never silently kept active;
- a removed event whose UID comes back is restored (`active`, `removedFromSourceAt` and `cancelledAt` cleared);
- past events (started before the refresh) are left as they are, so feeds that list only upcoming dates do not cancel history.

**Failures** keep the last good data: an unreachable feed, an HTTP error, a response that is not a complete iCalendar document (an HTML error page, a cut-off download), or an empty calendar where there were upcoming events records `lastOutcome: failed` and `lastError`, and the source is retried after 15 minutes, doubling up to 6 hours but never later than the next daily slot. The next good refresh resets `consecutiveFailures`.

**Activity.** Each refresh records `calendar.source.refreshed` or `calendar.source.refresh_failed` in Activity (when `ACTIVITY_SERVICE_URL` is set), attributed to the source as `system:ics-<name>` (for example `system:ics-zaw`), with the counts or the error. The feed URL, which can carry personal data such as a street, is never put in Activity, logs, or `lastError`. It is one summary event per refresh, also when nothing changed (the feed answered 304 or its events were all unchanged), so the Event Log shows that the daily refresh ran; the events a refresh imports, changes, cancels, or restores get no events of their own (see [Activity](#activity)). The Dashboard labels it with what changed ("Source refreshed: ZAW — 3 added, 1 cancelled") and shows each source's refresh health on Status.

**ICS normalization.** Parsing uses [golang-ical](https://github.com/arran4/golang-ical) for the document structure and [rrule-go](https://github.com/teambition/rrule-go) for recurrence rules:

- `DTSTART;VALUE=DATE` events are all-day; the exclusive `DTEND` becomes an inclusive `endDate` (`DURATION` and a missing end are handled).
- `TZID` times are resolved as an IANA zone, through the `VTIMEZONE`'s `X-LIC-LOCATION`, as a Windows zone name, from an IANA name at the end of a longer identifier (`/mozilla.org/…/Europe/Berlin`), or from the `VTIMEZONE` rules themselves; the IANA name becomes the event's `timeZone`. UTC times are taken as is; floating times are in the calendar's `X-WR-TIMEZONE`, else the owner's timezone.
- A timed event without an end gets a nominal one-minute duration; `DURATION` days are nominal (same wall-clock time).
- A recurring event (`RRULE`, `RDATE`, `EXDATE`) is imported as one event per occurrence from a year back to a year ahead (at most 1,000 per event; the window moves with each refresh), keyed `<UID>#<RECURRENCE-ID>` with the occurrence's original start (`20261017T040000Z`, or `20261017` for all-day). An override (`RECURRENCE-ID`) replaces its occurrence; `STATUS:CANCELLED` cancels it.
- Unreadable events (no start, a repeated UID) are skipped and counted; an event without a `UID` is keyed by its start and summary. A feed may hold at most 10,000 events.

**Safe fetching.** Feeds are downloaded only over https and only from public addresses: after DNS resolution, every connection to a loopback, private (including the Docker networks), link-local (including cloud metadata at 169.254.169.254), unique-local, or other special-purpose address is refused, and every redirect (at most 5) is checked again. Downloads are capped (`CALENDAR_ICS_MAX_BYTES`, default 5 MiB, also after decompression) and time-limited (`CALENDAR_ICS_TIMEOUT_SECONDS`, default 30), and never go through an environment proxy. `CALENDAR_ICS_ALLOW_PRIVATE_URLS=true` lifts the address and https rules for local development and verification against a local feed only.

**Deleting** a source deletes the events imported from it; set `enabled: false` instead to stop refreshing and keep them.

## ZAW waste collection

ZAW's Abfallkalender is served by MyMüll, which exports a personal iCal feed per street (`https://service.mymuell.de/api.php?m=zaw&r=ical&city_id=<city>&area_id=<area>&reminder=0&time=&city_name=<town>&street_name=<street>`): one timed event per pickup at 06:00 Europe/Berlin, the waste type only in `SUMMARY`, followed by the address (`Bioabfall - <town>, <street>`), which is also the `LOCATION`. The server sends no `ETag` or `Last-Modified` (`cache-control: no-store`), so every daily refresh is a full download, reconciled like any feed. No ZAW-specific scraping or storage is involved: ZAW is an ordinary ICS source plus a preset.

**Preset.** When `CALENDAR_ZAW_ICS_URL` is set (compose passes `LIFE_OS_ZAW_ICS_URL` from `.env`; the URL names a street, so it lives only there and is never logged), Calendar ensures at every start, before its scheduler runs, that exactly one source has `preset: "zaw"`:

- if there is none, a source added through the API with the same URL is taken over; otherwise a new one is added, enabled and due right away, so the first import happens within moments of the start rather than at the next 04:30;
- the preset sets `name` "ZAW", the `url`, `filter.include` `["Bioabfall", "Gelber Sack", "Papier", "Restmüll Tonnen und Container 14-täglich"]` (the feed's names for the bins; the weekly "Restmüll Container wöchentlich" for large containers and the "Schadstoffmobil" are left out), and `defaultReminders` `[{"kind":"localTime","daysBefore":1,"localTime":"19:00"}]` (the evening before each pickup, owner-local);
- the configuration last applied is stored with the source (`preset_config`). A later start changes only the settings whose configured value changed since (a new URL in `.env`, or a release that changes the types or reminders); a new URL or filter makes the source due right away. Anything else edited through the API (`PATCH /v1/sources/{id}`: name, filter, reminders, `enabled`) stays, and `enabled` is never changed by the preset;
- concurrent starts are serialized (an advisory lock, and a unique index on `preset`), so restarts never add a second source.

Unset, Calendar leaves an existing ZAW source as it is (and logs that); to remove it, unset the variable, restart, then disable or delete it. Deleting it while the variable is set brings it back at the next start.

**Filtering in Life OS.** The types are chosen by Life OS's own filter on the summary rather than by MyMüll's `trash=ZAW_BIO,ZAW_GELB,ZAW_PAP,ZAW_REST_2W` URL parameter, so the full feed is downloaded (`lastCounts.feedEvents` counts every pickup) and the selection can be changed on the source without touching the URL. Adding `trash=` to the URL also works, but then a type left out there cannot be brought back by the filter.

**Titles.** For this source only, the trailing ` - <town>, <street>` is removed from each title when it repeats the event's location, which keeps it (`Bioabfall`, `Gelber Sack`, `Papier Tonnen und Container`, `Restmüll Tonnen und Container 14-täglich`). This happens before filtering, so a street whose name contains a waste type cannot bring back an excluded type.

**Removed pickups** (a date ZAW moves or drops) are cancelled by the source and kept as history, like any feed's. The events are read-only in the Calendar UI, labelled "ZAW"; only their reminders can be changed (add your own, or turn the ZAW default off for one pickup).

## Google Calendar connection

Calendar connects to the owner's one Google account (P11-01). This release builds the connection, the token lifecycle, and the choice of calendars to sync; importing events (P11-02, polling with sync tokens) and writing Life OS events to a dedicated "Life OS" Google calendar (P11-03) come later, so nothing syncs yet. Owner setup (Google Cloud project, consent screen, OAuth client, `.env`) is in [Google Calendar setup](../operations/google-calendar.md).

**Flow.** The Dashboard's Calendar page asks Calendar to start an authorization (`POST /v1/google/connection/authorize`) and opens the returned consent URL. Google returns the owner to the Dashboard route `/calendar/google/callback` (the OAuth redirect URI), which posts the `code` and `state` to Calendar (`POST /v1/google/connection`) through the Dashboard's `/api/calendar` proxy as `user`. Calendar checks the state, exchanges the code at Google's token endpoint, identifies the account from its calendar list (the primary calendar's id is the account's address, so no profile scope is needed), stores the encrypted refresh token and the calendar list, and the Dashboard returns to `/calendar?google=connected`. Connecting another account replaces the connection and its calendar choices; reconnecting the same account keeps them.

**Scopes.** `calendar.calendarlist.readonly` (which calendars exist), `calendar.events` (read and write events), and `calendar.app.created` (the calendars Life OS creates), with `access_type=offline` and `prompt=consent` so Google issues a refresh token every time. Google lets the owner untick permissions: without `calendarlist.readonly` the connection is refused (403 `google_scope_missing`) and the grant revoked; other missing scopes are stored and reported as `missingScopes`.

**Security.**

- *State and PKCE.* Each authorization has a fresh 256-bit `state` and an S256 PKCE verifier. Calendar stores only the state's SHA-256 with the verifier (`google_oauth_states`), valid for 10 minutes and deleted when used, so a state works once; expired ones are swept at the next authorization. A forged, replayed, or late state is refused (400 `invalid_state`) before any call to Google. The code is useless without the client secret and the verifier, both of which only Calendar holds.
- *Encryption at rest.* The refresh token is encrypted with AES-256-GCM under `LIFE_OS_GOOGLE_TOKEN_KEY` (32 bytes, base64), with a random nonce per encryption stored beside it and a fixed additional-data label; a changed key or an altered ciphertext fails to decrypt (status `error`: reconnect). Access tokens are kept in memory only (their expiry is stored for reference); after a restart the first use refreshes one.
- *No secrets out.* Tokens and the client secret never appear in responses, logs, errors, or Activity; errors keep Google's error code and description only. The callback URL's one-time code is dropped from the browser's address bar at once, the page sends no referrer, and the gateway does not log the callback request.

**Token lifecycle.** An access token is refreshed on demand when it has less than 5 minutes left (`RefreshMargin`), one refresh at a time; a rotated refresh token is re-encrypted and stored. A calendar-list call answered 401 refreshes once and retries. When Google refuses the refresh token (`invalid_grant`: it expired, or the owner removed Life OS in their Google Account) the connection becomes `expired` with a `lastError` ending "reconnect Google in Calendar", records `calendar.google.expired` once, and stops asking Google until the owner reconnects. Another refusal (such as `invalid_client` after a changed client secret) sets `error`; an outage (unreachable, 5xx) changes nothing and answers 502 `google_unavailable`. A later successful refresh returns the connection to `connected`. Nothing checks the connection in the background yet (P11-02's polling will), so an expiry shows once something uses the token, such as Refresh list.

**Calendars.** The list is stored as Google last listed it (hidden and deleted entries left out), primary first, then by name. A newly listed primary calendar starts with `syncEnabled` true, every other calendar false; a refresh keeps the owner's choices, updates names, colors, and access roles, and drops calendars Google no longer lists.

**Not configured.** Without all of `LIFE_OS_GOOGLE_CLIENT_ID`, `LIFE_OS_GOOGLE_CLIENT_SECRET`, and a valid `LIFE_OS_GOOGLE_TOKEN_KEY`, Calendar starts normally and logs why; `GET /v1/google/connection` says `configured: false`, and authorize, connect, and refresh answer 503 `google_not_configured`. A connection stored earlier can still be disconnected (it is then only forgotten: without the key the token cannot be decrypted to revoke it).

| Request | Behavior |
| --- | --- |
| `GET /v1/google/connection` | `{configured, status: not_connected\|connected\|expired\|error, accountEmail, scopes, missingScopes, connectedAt, lastError}`. |
| `POST /v1/google/connection/authorize` | `{url}`: Google's consent page with a new single-use state and PKCE challenge (a reconnect adds `login_hint`). 503 when not configured. |
| `POST /v1/google/connection` | `{code, state}` → the connection (200). 400 `invalid_state` or `google_code_rejected`, 403 `google_scope_missing`, 502 `google_rejected`/`google_unavailable`, 503 `google_not_configured`. |
| `DELETE /v1/google/connection` | Revokes the refresh token at Google (best effort; a failure is noted in the Activity summary) and deletes the connection with its calendars (204, also without a connection). |
| `GET /v1/google/calendars` | `{items}`: the stored list with `id` (Calendar's), `googleCalendarId`, `summary`, `color`, `accessRole`, `primary`, `syncEnabled`. Empty without a connection. |
| `POST /v1/google/calendars/refresh` | Reads the list from Google again and returns it. 409 `google_not_connected` or `google_expired`, 502, 503. |
| `PATCH /v1/google/calendars/{id}` | `{syncEnabled}` → the calendar; 404 for an unknown id. |

Activity records `calendar.google.connected` (summary "Google Calendar connected: owner@example.com (3 calendars)", attributed to the request's actor), `calendar.google.disconnected` (with `revokedAtGoogle`), and `calendar.google.expired` (attributed to `system:calendar`, outcome failed), all with subject `google_connection`; their `data` holds the account address and, for a connection, the granted and missing scopes.

The integration tests (`internal/api/google_integration_test.go`) run the whole flow against an in-process fake Google (`internal/google/fakegoogle`), never the real one: state reuse and expiry, PKCE, refresh margin, restarts, `invalid_grant`, rotation, a changed key, and checks that no response, Activity event, or stored column holds a token or the secret.

## Activity

When `ACTIVITY_SERVICE_URL` is set, every event change made through the API records exactly one Activity event once it is stored, with subject `calendar_event` and the event's id. It is attributed to the request's `X-Life-OS-Actor` (`user` for the owner in the Dashboard, `agent:<name>` for the AI through MCP, `dev:<name>` for development tools) and carries the request's `X-Correlation-ID` and `X-Causation-ID`, so a change the agent makes appears in its capture's workflow in the Event Log, after the MCP action that caused it.

| Type | Recorded for |
| --- | --- |
| `calendar.event.created` | `POST /v1/events`. Its id is derived from the event's, so an idempotent replay is recorded once. |
| `calendar.event.rescheduled` | A `PATCH` that changes the start, the end, or all-day (with whatever else it changes, unless it also changes the status). |
| `calendar.event.cancelled` / `calendar.event.restored` | A `PATCH` that changes `status`. |
| `calendar.event.updated` | Any other `PATCH` that changes something: title, notes, location, time zone, repetition, or reminders (also an imported event's reminders). |
| `calendar.event.deleted` | `DELETE /v1/events/{id}`. |

A `PATCH` that changes nothing, and any refused request (invalid, 404, 409 `managed_by_source`), records nothing. `data` holds the `title`, the times (`startAt`/`endAt` or `startDate`/`endDate`), `allDay`, `timeZone`, `status`, `sourceKind` and `sourceId`, and `recurrence`; an update adds `changes` (the changed fields) and, when renamed, `previousTitle`; a reschedule adds `previous` (the old times). Notes, location, and reminders are not copied into Activity. The summary is a sentence with times in the owner's timezone, such as "Event “Dentist” moved to Thu 19 Nov 2026, 09:00 CET".

Source refreshes record one summary event per refresh (see [Sources](#sources-ics-feeds)), not one per imported event: a first import brings dozens of events, and the refresh's counts say what changed.

## API

| Request | Behavior |
| --- | --- |
| `POST /v1/events` | Creates an active manual event (201). Requires `title` and `allDay` plus that kind's start and end. `Idempotency-Key`: an identical repeat returns the original event; reuse with a different body is 409. |
| `GET /v1/events/{id}` | The event, whatever its status. |
| `PATCH /v1/events/{id}` | Changes only the named fields; `null` clears a nullable field (`description`, `location`, `timeZone`, `recurrence`, `reminders`, and the start/end fields). Changing `allDay` switches kind: the other kind's fields are cleared and the new kind's start and end must be given. The merged event is validated as a whole; a concurrent change makes the update re-read (409 if it keeps changing). `reminders: null` returns the event to its defaults alone (no own reminders, `useDefaultReminders` true unless the body sets it). Imported events: only `reminders` and `useDefaultReminders` (else 409 `managed_by_source`). |
| `DELETE /v1/events/{id}` | Deletes a manual event permanently (204); imported events return 409 `managed_by_source`. |
| `GET /v1/events?from&to` | Events and occurrences overlapping `[from, to)` (`to` must be after `from`), soonest start first. Optional `timeZone`, `includeCancelled`, `sourceKind`, `sourceId`, `limit` (1–100, default 50), and `cursor`. |
| `GET /v1/events/search?q` | Events whose title, description, or location contain every word of `q` (case-insensitive substring, up to 10 words), across all dates. Optional `from`/`to` (either may be open), `timeZone`, `includeCancelled`, `sourceKind`, `sourceId`, `limit` (1–100, default 20), and `cursor`. |
| `GET /v1/sources` | Every source with its refresh state. |
| `POST /v1/sources` | Adds a source (201): `name`, `url`, optional `enabled` (default true), `filter`, `defaultReminders`. Its first import is due right away. |
| `GET /v1/sources/{id}` | The source. |
| `PATCH /v1/sources/{id}` | Changes the named fields (`defaultReminders: null` removes them). A new `url` or `filter` drops the validators and schedules a full refresh now. |
| `DELETE /v1/sources/{id}` | Deletes the source and its events (204). |
| `GET /v1/settings`, `PATCH /v1/settings` | The owner's default reminders for timed and all-day Life OS events (see [Settings](#settings)). |
| `POST /v1/sources/{id}/refresh` | Refreshes now and returns the source (200, whatever the outcome); 409 `source_disabled`, `refresh_in_progress`, or `conflict` (the source changed meanwhile). |

Search ranks a word found in the title 3, in the location 2, in the description 1, most relevant first; among equals, events not over yet come first (soonest first), then past ones (most recent first). Listing and search page with an opaque `nextCursor`: pass it back as `cursor` with the same parameters; no `nextCursor` means the last page. A search cursor also keeps the moment that split upcoming from past, so paging stays consistent while time passes.

Every write needs `X-Life-OS-Actor` (see [API overview](../api/overview.md)). Invalid requests return 400 with an error message saying what to fix; unknown ids return 404.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `LIFE_OS_TIMEZONE` | `UTC` | The owner's timezone (all-day matching, recurrence wall clock, refresh schedule, floating ICS times). |
| `CALENDAR_SOURCE_REFRESH_AT` | `04:30` | Daily source refresh time, HH:MM owner-local (compose passes `LIFE_OS_CALENDAR_SOURCE_REFRESH_AT`). |
| `ACTIVITY_SERVICE_URL` | empty | Activity service for event changes and refresh outcomes; empty records nothing. |
| `CALENDAR_ICS_MAX_BYTES` | `5242880` | Largest feed download. |
| `CALENDAR_ICS_TIMEOUT_SECONDS` | `30` | Time limit of one download. |
| `CALENDAR_ICS_ALLOW_PRIVATE_URLS` | `false` | Development only: allow http and private/loopback feed addresses. |
| `CALENDAR_ZAW_ICS_URL` | empty | The owner's MyMüll iCal feed for the ZAW source (compose passes `LIFE_OS_ZAW_ICS_URL`); empty configures none. See [ZAW waste collection](#zaw-waste-collection). |
| `LIFE_OS_GOOGLE_CLIENT_ID`, `LIFE_OS_GOOGLE_CLIENT_SECRET` | empty | The owner's Google OAuth Web client. Secret: never logged or returned. See [Google Calendar connection](#google-calendar-connection). |
| `LIFE_OS_GOOGLE_TOKEN_KEY` | empty | 32-byte key, base64 (`openssl rand -base64 32`), that encrypts the stored refresh token. A new key means reconnecting. |
| `LIFE_OS_GOOGLE_REDIRECT_URI` | `https://lifeos.gamch1k.com/calendar/google/callback` | The redirect URI registered on the OAuth client: the Dashboard's callback route (https; http only for localhost). |

## Tests

`internal/api`, `internal/repository`, `internal/ics`, `internal/recurrence`, and `internal/sources` hold unit tests (parser edge cases on a sanitized waste-collection feed in `internal/ics/testdata`, time zones, the address guard, filters, the ZAW title clean-up and type selection, reconciliation, schedule and backoff, recurrence across daylight saving) and integration tests (among them the ZAW preset: repeated starts keep one source, configuration changes, the four types, reminders, removal). The integration tests run against the Postgres named by `CALENDAR_TEST_DATABASE_URL` (a throwaway database: each test creates and drops its own schema and applies every migration) and skip when it is unset; the source tests serve the feed from an in-process HTTP server. For example:

```sh
docker run -d --rm --name calendar-test-pg -e POSTGRES_PASSWORD=test -p 127.0.0.1:55441:5432 --tmpfs /var/lib/postgresql postgres:18.6-alpine
cd services/calendar
CALENDAR_TEST_DATABASE_URL='postgres://postgres:test@127.0.0.1:55441/postgres?sslmode=disable' GOWORK=off go test -count=1 ./...
docker rm -f calendar-test-pg
```
