# Google Calendar integration (`google-calendar`)

`google-calendar` connects the owner's one Google account and syncs the Google calendars they choose both ways with Calendar (P11-07; the behaviour came from Calendar's in-process sync of P11-01 to P11-03). It is an **integration**: a separate, replaceable service that reaches Calendar only through Calendar's public [integration API](calendar-integration-api.md) and events API, as `system:google-calendar`. Calendar names no provider, so another provider (Outlook, for example) can replace this service without changing Calendar. Owner setup is in [Google Calendar setup](../operations/google-calendar.md).

- Database: `google_calendar_db` (user `google_calendar_user`, password `LIFE_OS_GOOGLE_CALENDAR_DB_PASSWORD`), migrated by `google-calendar-migrate`.
- Contract: `services/google-calendar/api/openapi.yaml`. Gateway: `/api/integrations/google-calendar/` (`proxy_pass http://google-calendar:8080/`); the Dashboard also proxies it at `/api/integrations/google-calendar`. `/health` and `/ready` as usual.
- Every write needs `X-Life-OS-Actor` (400 `actor_required`). Tokens and the client secret never appear in responses, logs, errors, or Activity.

## What it does

| Part | How |
| --- | --- |
| Connection | OAuth with a single-use state and PKCE; the refresh token sealed with `LIFE_OS_GOOGLE_TOKEN_KEY` (AES-256-GCM, the same key and label Calendar used, so the handed-over token opens); access tokens in memory only. |
| Calendar list | The account's calendars with the owner's choice of those to sync (a new primary calendar starts on). |
| Inbound sync | Each chosen calendar is a Calendar source (`kind` `external`, integration `google-calendar`, `externalId` the Google calendar id, provider `Google`, read-only unless the account may edit it). Every `GOOGLE_CALENDAR_SYNC_SECONDS` (120) it lists the calendar with Google's sync token, stores the raw events, derives Calendar's events with the same mapping as before, and sends Calendar what changed. |
| Edits made in Life OS | The owner's edits of events of calendars the account may edit come through Calendar's change feed and are written to Google a few seconds later (`GOOGLE_CALENDAR_FEED_SECONDS`, 15): only what changed, with the stored etag. |
| Life OS events | Exported to a "Life OS" calendar this service creates in the owner's Google account; changes made to the copies in Google come back to the Life OS events through Calendar's events API. |

## Connection

**Flow.** The Dashboard's Google dialog calls `POST /v1/connection/authorize` and opens the returned consent URL. Google returns the owner to the Dashboard page `/calendar/google/callback` — the redirect URI registered at Google, unchanged since P11-01 (the gateway routes it to the Dashboard and keeps it out of its access log) — which posts `code` and `state` to `POST /v1/connection` as the owner. The service checks the state, exchanges the code (with its PKCE verifier), identifies the account from its calendar list (the primary calendar's id is the account's address), stores the sealed refresh token and the list, and the Dashboard returns to `/calendar?google=connected`. Connecting another account replaces the connection: its calendars' sources leave Calendar. Reconnecting the same account keeps the calendar choices and makes every synced calendar, waiting export, and waiting edit due at once.

**Scopes** (unchanged): `calendar.calendarlist.readonly`, `calendar.events`, and `calendar.app.created`, with `access_type=offline` and `prompt=consent`. Without `calendarlist.readonly` the connection is refused (403 `google_scope_missing`) and the grant revoked; other missing scopes are reported as `missingScopes`.

**Security.** A fresh 256-bit state and an S256 PKCE verifier per authorization; only the state's SHA-256 is stored (`google_oauth_states`), valid for 10 minutes and used once (400 `invalid_state`). The refresh token is encrypted at rest with a random nonce and the additional-data label `life-os/calendar/google-refresh-token/v1`; a changed key fails to decrypt (status `error`: reconnect).

**Token lifecycle.** An access token is refreshed when it has less than 5 minutes left; a rotated refresh token is sealed and stored; a 401 refreshes once and retries. `invalid_grant` makes the connection `expired` (Activity `calendar.google.expired`, once) and stops every call to Google until the owner reconnects; another OAuth refusal sets `error`; an outage changes nothing.

**Not configured.** Without `LIFE_OS_GOOGLE_CLIENT_ID`, `LIFE_OS_GOOGLE_CLIENT_SECRET`, and a valid `LIFE_OS_GOOGLE_TOKEN_KEY`, or once they are set without a valid `LIFE_OS_GOOGLE_REDIRECT_URI`, the service starts and logs why; `GET /v1/connection` says `configured: false` and authorize, connect, and refresh answer 503 `google_not_configured`.

## Inbound sync

The loop runs every `GOOGLE_CALENDAR_FEED_SECONDS` (and when woken): it removes the sources of calendars that stopped syncing, reads Calendar's change feed, writes the waiting edits and exports to Google, and then syncs the calendars that are due (every `GOOGLE_CALENDAR_SYNC_SECONDS`, later after failures). One sync of a calendar:

1. Takes the calendar's lease (one sync per calendar across processes).
2. Lists it with `events.list` (`singleEvents=false`, `showDeleted=true`): in full from a year back the first time, after a 410, or with `?full=true`; otherwise only what changed since its sync token.
3. Registers its source with Calendar (`PUT /v1/integrations/google-calendar/sources/{googleCalendarId}`) when it is new or its name, access, or default reminders changed. `readOnly` follows the account's access role every time.
4. In one transaction holding the calendar row: stores the raw events (`google_events`; its own writes, recognized by their etag, are skipped), derives the calendar's events from all of them (`internal/google/mapping.go`: single events, series with their exceptions when the rule fits Calendar's model, else one event per occurrence for 400 days, the mapping table below), and sends Calendar only what differs from what it was last told (`google_sent_events`) as `delta` batches of at most 500 (`POST .../sync` with `window.start` the full listing's window start, so history before it stays, and the deleted uids). Events are derived again when the listing changed something, after the service's own write, after a full listing, or every 12 hours.
5. Records the sync token and the outcome.

Rules: occurrences of a rule Calendar cannot hold (`<id>_<original start>`) and lone changed instances carry `readOnlyReason` ("This is one occurrence of a repeating event in “Owner” whose rule Life OS can't edit, …"); every event of a calendar the account can only read carries the reader reason. An item Calendar refuses (400 `invalid_item`) is left out and logged, so one event never stops a calendar. The owner's own reminders and priority of an imported event survive every sync (Calendar's rule; an item carries Google's reminders and its `lifeosPriority` property only).

**Writable calendars and the feed cursor.** A sync of a calendar the account may edit waits while an edit of its events is still to be written to Google, and sends `feedCursor` (the change feed position the service has handled), so Calendar refuses it (409 `unhandled_changes`) if an edit arrived in between; the service then reads the feed, writes the edit, and syncs again. A sync never overwrites an edit made in Life OS that has not reached Google.

**Failures.** A failed listing or a refused or unreachable Calendar changes no event: the calendar records `lastError` and one more failure and is retried after the interval, doubling up to an hour. Activity records `calendar.source.refresh_failed` when a streak starts or the error changes, and `calendar.source.refreshed` (counts) on the first sync, on a sync that changed something, and on the first good one after failures.

**Mapping** (unchanged from Calendar's):

| Google | Calendar |
| --- | --- |
| `id` | `externalUid` |
| `summary`, `description`, `location` | `title` ("Untitled event" when empty), notes (HTML from the web converted to text), place, clipped to 500/10,000/500 characters |
| timed `start`/`end` with `timeZone` | `startAt`/`endAt` and `timeZone` (an end not after the start gets one minute) |
| all-day `start.date`/`end.date` (end exclusive) | `startDate`/`endDate` (end inclusive) |
| `reminders` (`useDefault`, overrides) | the source event's reminders; the calendar's default notifications are the source's `defaultReminders` |
| `extendedProperties.private.lifeosPriority` | the event's own priority (`default` when empty) |
| `recurrence` + changed/cancelled instances | a series with exceptions when it fits Calendar's model, else one read-only event per occurrence |
| `eventType: workingLocation` | not imported |

**Switching off, unlisting, disconnecting.** The source of a calendar that stops syncing (switched off, no longer listed by Google, disconnected, or another account connected) is queued in `google_source_removals` and removed from Calendar with its events right away, or as soon as Calendar answers, so the owner's choice never waits for Calendar. Activity records `calendar.source.removed` per calendar (reason `sync_disabled`, `calendar_unlisted`, `disconnected`, `account_replaced`; `removedEvents` the events Calendar was told of). Turning a calendar on again before its removal ran removes the old source first.

## Two-way sync

| Event | Edited in Life OS | Reaches Google | Changes made in Google |
| --- | --- | --- | --- |
| Created in Life OS (`manual`) | yes | exported to the Life OS calendar from the change feed (retried while Google is unreachable) | applied to the Life OS event through Calendar's events API (Google wins a conflict) |
| In a calendar the account may edit: single events and series Calendar holds | yes (`editable`) | from the change feed a few seconds later, with the stored etag | synced |
| In a calendar the account can only read | reminders and priority, in Life OS only | never | synced |
| One occurrence of a rule Calendar cannot hold, or a lone instance | reminders and priority only (`readOnlyReason`) | never | synced |

**The change feed.** The service follows `GET /v1/integrations/changes?scope=own` (cursor on the connection, `feed_cursor`). A new connection, an expired cursor (410), and switching export on read it from a snapshot: upcoming Life OS events without an export are queued, those already exported are only remembered (nothing is written again), and an export whose event the snapshot did not list has its copy deleted. After the snapshot every change is handled in the transaction that stores the new cursor: a Life OS event's change queues its export (`google_exports`, with the event as the feed gave it), and an edit of a writable Google calendar's event queues an edit (`google_pushes`). Entries that are only `system:google-calendar` (its own syncs and the changes it applies) are skipped.

**Writing an edit.** The event's state in Calendar is compared with the item derived from Google's stored event, and Google gets a PATCH with only the fields that differ (title → `summary`, notes, place, start and end in the event's zone, the rule plus the EXDATEs Google already has, reminders as popup overrides, priority as `lifeosPriority`), with `If-Match` the stored etag; attendees, conference data, and colors stay as they are. Occurrence changes write the instances (a skip cancels the instance, an occurrence brought back is confirmed), cancelling deletes the event in Google (Life OS keeps it as cancelled history), deleting it in Life OS deletes it in Google, restoring a deleted one writes it back, and a series ended by "this and following" gets its UNTIL and loses its changed instances after it. The new series of a split (`lifeos:<id>` in Calendar) is created in Google with its skips and changed occurrences, and the next sync gives Calendar's event Google's id (`previousExternalUid`), keeping its Life OS id. Google's answer is stored and reaches Calendar with the calendar's next sync, right after. Reminders Google cannot hold (a time-of-day reminder on a timed event, more than five) stay Life OS's own.

**Conflicts and refusals.** When Google refuses an edit — it changed in Google meanwhile (412), it was deleted there, or the calendar became read-only (403) — Google's version is kept: it is read back, Calendar is told it again at the next sync (which replaces the edit in Life OS), and `calendar.google.conflict` is recorded. Google or Calendar unreachable, or an expired connection, keeps the edit waiting and retries it (30 s, doubling up to an hour); the calendar does not sync meanwhile, and the connection shows `changes.pendingEdits` and `failedEdits`.

**Life OS calendar and exports** (as before): the Life OS calendar is created (or an owned calendar of that name adopted) when Google is connected with export on, joins the calendar list with sync on, and is created again if the owner deletes it (`lifeOsCalendar.note`). Each export writes the event's whole copy under the fixed id `lifeos<event uuid hex>` (so a repeated export finds it), with the reminders that apply as popup overrides and the private properties `lifeosEventId` and `lifeosPriority`, then its changed occurrences as changed instances, with `If-Match`; a copy changed in Google meanwhile is not overwritten (Google's version is applied to the Life OS event, `calendar.google.conflict` with `data.kind` `export`). Failures retry with backoff (`calendar.google.export_failed` once per streak); an expired connection parks them. The copies are not imported as Google events; a copy changed in Google is applied to its Life OS event through `PATCH /v1/events/{id}` and its occurrence endpoints (Calendar records `calendar.event.updated`/`rescheduled` attributed to `system:google-calendar`), and a copy deleted in Google cancels its Life OS event.

## API

| Request | Behaviour |
| --- | --- |
| `GET /v1/connection` | `{configured, status, accountEmail, scopes, missingScopes, connectedAt, lastError, exportEnabled, lifeOsCalendar, export: {pending, failed, lastError, lastExportedAt}, changes: {lastReadAt, lastError, lastErrorAt, pendingEdits, failedEdits, lastEditError}}`. |
| `POST /v1/connection/authorize` | `{url}`: Google's consent page. 503 `google_not_configured`. |
| `POST /v1/connection` | `{code, state}` → the connection. 400 `invalid_state`/`google_code_rejected`, 403 `google_scope_missing`, 502 `google_rejected`/`google_unavailable`, 503. |
| `PATCH /v1/connection` | `{exportEnabled}`. 409 `google_not_connected`. |
| `DELETE /v1/connection` | Revokes the token at Google (best effort), deletes the connection, and removes the synced calendars' sources from Calendar (204, also without a connection). |
| `GET /v1/calendars` | The list with `sync` (`state` `off`/`pending`/`synced`/`failing`, `sourceId` — Calendar's source id —, last attempt and success, next sync, `lastError`, `consecutiveFailures`, `lastCounts`, `syncing`); the Life OS calendar has `lifeOs: true`. |
| `POST /v1/calendars/refresh` | Reads the list from Google again. |
| `PATCH /v1/calendars/{id}` | `{syncEnabled}`. |
| `POST /v1/calendars/{id}/sync?full=` | Syncs now; 409 `sync_in_progress` also while edits wait to reach Google. |
| `GET /v1/exports?eventId=…` | Where Life OS events are in Google (up to 200 ids): `{eventId, calendarId, calendarName, lifeOs, state, error}` for each exported one. The Dashboard's event details use it ("In Google: Life OS · Exported"). |

## Activity

Source `google-calendar`, the same event types as before: `calendar.google.connected`, `disconnected`, `expired` (subject `google_connection`), `calendar.google.calendar_created`, `export_failed`, `conflict`, and per Google calendar `calendar.source.refreshed`, `refresh_failed`, and `removed` (subject `calendar_source`, Calendar's source id; `data.sourceKind` `external`, `provider` `Google`). Calendar itself records the event changes the integration makes through its API.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `DATABASE_URL` | required | `google_calendar_db`. |
| `CALENDAR_SERVICE_URL` | empty | Calendar (compose: `http://calendar:8080`). Empty: nothing is synced. |
| `ACTIVITY_SERVICE_URL` | empty | Activity. |
| `LIFE_OS_TIMEZONE` | `UTC` | The owner's timezone (all-day series' ends, the Life OS calendar's zone). |
| `LIFE_OS_GOOGLE_CLIENT_ID`, `LIFE_OS_GOOGLE_CLIENT_SECRET`, `LIFE_OS_GOOGLE_TOKEN_KEY` | empty | The OAuth client and the sealing key (moved here from Calendar). Secrets. |
| `LIFE_OS_GOOGLE_REDIRECT_URI` | empty (compose: `LIFE_OS_PUBLIC_URL` + `/calendar/google/callback`, such as `https://lifeos.example.com/calendar/google/callback`) | The Dashboard callback registered on the OAuth client; https, http only for localhost. The service has no default: with the client set and this empty, Google stays not configured and the log names it. |
| `GOOGLE_CALENDAR_SYNC_SECONDS` | `120` | Sync interval, 30–86400 (compose passes `LIFE_OS_CALENDAR_GOOGLE_SYNC_SECONDS`, the name the owner's `.env` already has). |
| `GOOGLE_CALENDAR_FEED_SECONDS` | `15` | How often Calendar's change feed is read, 1–3600 (compose: `LIFE_OS_GOOGLE_CALENDAR_FEED_SECONDS`). |
| `GOOGLE_AUTH_URL`, `GOOGLE_TOKEN_URL`, `GOOGLE_REVOKE_URL`, `GOOGLE_CALENDAR_API_URL` | Google's | Tests and the verification stack point them elsewhere. |

## History: the handoff from Calendar

The P11-07 release moved what Calendar's in-process sync kept in `calendar_db` into `google_calendar_db` once, with a `google-calendar handoff` command: the connection with its sealed refresh token, the calendars with their sync tokens, windows, and Calendar source ids, the raw Google events, the export links with their etags, and the uids of the events Calendar held of each Google source. P11-09 removed the command (and `HANDOFF_CALENDAR_DATABASE_URL`) after Calendar's migration 000017 dropped the legacy `google_*` tables; the service never reads Calendar's database. See [Google Calendar setup: history](../operations/google-calendar.md#history-the-handoff-from-calendar-p11-07).

## Code and tests

`internal/google` holds the moved code (OAuth, sealing, Google's API client, mapping, reverse mapping) and the parts rewritten for the integration API: `sync.go` and `send.go` (inbound sync and deltas), `feed.go` (change feed), `push.go` (edits), `export.go` and `linked.go` (Life OS calendar), `sources.go` (registration and removals). `internal/calendar` is the Calendar client; `internal/ics`, `internal/recurrence`, and `internal/sources` are copies of Calendar's packages the mapping uses.

Unit tests cover the mapping, reverse mapping, client, sealing, and configuration. The integration tests (`GOOGLE_CALENDAR_TEST_DATABASE_URL`, skipped when unset) run against the fake Google (`internal/google/fakegoogle`) and **the real Calendar**: `internal/testsupport` builds `services/calendar`, migrates a private schema with Calendar's migrations, and runs it as a process, so every test goes through Calendar's actual integration API — inbound sync (full, incremental, changes, removal, restore, read-only calendars, expanded occurrences, switching off, disconnecting), edits written to Google (only what changed, `If-Match`, outage, conflict, cancel, restore, delete, series, This event, split and re-key, truncate), and exports and changes made to copies in Google.
