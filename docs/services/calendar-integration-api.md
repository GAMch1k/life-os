# Calendar integration API

Calendar owns events. **Integrations** are separate services that connect a provider to Calendar: `google-calendar` (P11-07) for Google Calendar and `calendar-feeds` (P11-08) for ICS feeds, with ZAW as a preset. An integration gives Calendar the events of its sources, and picks up the events changed in Life OS, only through Calendar's public API. Calendar never names a provider, so a provider can be replaced (Outlook instead of Google, for example) without changing Calendar.

This page explains that API for developers who build an integration. The contract is in `services/calendar/api/openapi.yaml` (paths under `/v1/integrations`). It was added in P11-06.

## Why, and where we are

Until P11-06, Google Calendar (`internal/google`) and ICS feeds (`internal/sources`, `internal/ics`) ran inside the Calendar service. The owner's rule is that integrations are replaceable services that talk to a domain service only through its API. The migration happens in steps:

| Ticket | What happens |
| --- | --- |
| P11-06 | Adds this API and migration 000014. Calendar's in-process Google and ICS adapters keep working as before. |
| P11-07 | `google-calendar` takes over OAuth, inbound sync, and writing Life OS events and edits to Google. Calendar's in-process Google code goes. |
| P11-08 | `calendar-feeds` takes over ICS feeds and the ZAW preset ([calendar-feeds](calendar-feeds.md)). The in-process ICS refresher goes; migration 000015 (`calendar_feeds_handover`) hands the feeds over, and `/v1/sources` no longer adds or refreshes feeds. |
| P11-09 | Drops the legacy columns and tables (`google_*`, the feed columns of `calendar_sources`, the `ics`/`google` source kinds). |

## Concepts

**Integration.** Each integration has a name made of lower-case letters, digits, and dashes, such as `google-calendar`. Its actor is `system:<name>`, sent in `X-Life-OS-Actor` like every Life OS write.

**Source.** A source is one calendar of an integration: a Google calendar, or one feed. It is identified by `(integration, externalId)`, where `externalId` is the integration's own id for it, such as a Google calendar id. Calendar also gives it a UUID (`id`), and its events carry that as `source.sourceId`. A source also has:

- `provider`: a display label such as `Google`, `ZAW`, or `ICS`.
- `readOnly`: whether the source accepts changes to its events.
- `enabled`: the owner's switch. A disabled source refuses syncs and keeps its events.
- `inProcess`: true while Calendar's own adapter still syncs the source. See [Handover](#handover-from-the-in-process-adapters).

The owner also sets the source's name, default reminders, and default priority (see [Registering a source](#registering-a-source)).

**Events.** The events of an integration's source have `source.kind` `external`. In the events API their `source` object also shows `integration`, `provider`, `name`, and `readOnly`, so the Dashboard can label them generically as "<provider> · <name>". `GET /v1/events?sourceKind=external` lists them, and `sourceKind=manual` lists the events created in Life OS.

What the owner may change on an external event:

- **Read-only source:** only its reminders and priority. Any other change, and any delete, returns 409 `managed_by_source`.
- **Source that accepts changes:** the owner can change the event in Life OS like a Life OS event: All events, This event, This and following, cancel, and delete. These events show `editable: true` and are returned by `editable=true`, which the AI uses to find what it may change. Each change reaches the integration through the [change feed](#change-feed), and the integration writes it to the provider. Such an edit therefore reaches the provider **asynchronously**. The integration resolves conflicts with the provider's own versions, for example with Google's etags.
- **`readOnlyReason`:** an event can be read-only on its own even in a source that accepts changes, such as one occurrence of a Google rule Calendar cannot hold. The integration sets `readOnlyReason` on that item, and the owner sees the reason.

## Authorization

| Endpoint | Allowed actors |
| --- | --- |
| `/v1/integrations/{integration}/sources...` | `system:<integration>` and `dev:<name>` |
| `GET /v1/integrations/changes` | any `system:<name>` and `dev:<name>` |

Any other actor gets 403 `forbidden`, and so does a request with no actor. This includes reads, which elsewhere need no actor. Writes without a valid actor are still refused first, with 400 `actor_required`. `dev:<name>` is for tests and tools.

## Registering a source

`PUT /v1/integrations/{integration}/sources/{externalId}` registers a source. URL-encode `externalId`: Google calendar ids contain `@` and `#`. The call is idempotent: the first call creates the source (201, enabled), and later calls update it (200).

```http
PUT /v1/integrations/google-calendar/sources/owner%40example.com
X-Life-OS-Actor: system:google-calendar

{"name": "Owner", "provider": "Google", "readOnly": false,
 "defaultReminders": [{"kind": "relative", "minutesBefore": 10}]}
```

`provider` and `readOnly` always take the values you send.

`name`, `defaultReminders`, and `defaultPriority` are also the owner's settings, so they follow the rule Calendar's presets use. Each one changes only when the integration sends a different value from the one it sent last time (Calendar keeps that in `integration_config`). Examples:

- **Renamed at the provider:** a Google calendar renamed in Google. The integration sends the new name, and the source is renamed.
- **Renamed by the owner:** the owner renames the source in Life OS through `PATCH /v1/sources/{id}`. The integration keeps sending the old name, so the owner's name stays until the source is renamed at the provider.
- **Field left out:** leaving out `defaultReminders` or `defaultPriority` says nothing about it, so the current value stays.

`enabled` belongs to the owner alone. The owner changes these settings through `/v1/sources/{id}`, which also serves external sources. Changing their `url` or `filter` there, or refreshing them, returns 409 `managed_by_integration`.

Other source endpoints:

- `GET /v1/integrations/{integration}/sources` lists the integration's sources. `GET .../sources/{externalId}` reads one.
- `DELETE .../sources/{externalId}` deletes the source and all its events. Deleting a source writes nothing to the change feed, so a deletion in Life OS never deletes events at the provider.
- Feed URLs are not part of this API. A feed URL is the integration's own configuration, and the ZAW URL names a street, so it stays in `calendar-feeds`.

### Handover from the in-process adapters

Migration 000014 gave every existing source its identity:

- **Google sources:** `google-calendar` / the Google calendar id, provider `Google`, read-only unless the account may edit the calendar.
- **ICS feeds:** `calendar-feeds` / the source's own UUID, provider `ZAW` for the preset and `ICS` otherwise.

These sources keep `kind` `ics` or `google` and have `inProcess: true`. Registering, syncing, or deleting them through this API returns 409 `source_in_process`, so two writers never sync the same source.

The release that switches an in-process adapter off hands its sources over in one migration:

```sql
SET CONSTRAINTS events_record_change, event_exceptions_record_change IMMEDIATE;
UPDATE calendar_sources SET in_process = false, kind = 'external' WHERE integration = 'google-calendar';
UPDATE events SET source_kind = 'external'
WHERE source_id IN (SELECT id FROM calendar_sources WHERE integration = 'google-calendar');
```

The first line is needed only if the same migration also alters `events` or `event_exceptions` (see [Notes for migrations](#notes-for-migrations)). After the handover, the integration registers the sources again with the same `externalId` and continues from there. calendar-feeds' handover is migration 000015, `calendar_feeds_handover`, followed once by `calendar-feeds handoff`, which copies each feed's URL, filter, preset, and refresh state from the legacy columns (not from `integration_config`, which holds only the owner's settings the integration last sent); see [Calendar feeds handoff](../operations/calendar-feeds-handoff.md).

## Syncing events

`POST /v1/integrations/{integration}/sources/{externalId}/sync` writes one batch of a source's events in one transaction and returns what changed. Events are keyed by `externalUid`, and sending the same batch again changes nothing.

| Mode | What the batch contains | How the rest is handled |
| --- | --- | --- |
| `snapshot` | every event of the source | A stored event the batch leaves out is cancelled and marked `removedFromSourceAt`. It is kept as history and restored if it comes back. This is today's ICS and Google reconcile. |
| `delta` | new and changed events in `items`, and the uids of events the source no longer has in `deleted` | A deleted uid is handled like an event a snapshot leaves out. Unknown uids are ignored. Events the batch does not name stay as they are. |

The response looks like this:

```json
{"mode": "snapshot", "received": 5, "inserted": 1, "updated": 1, "removed": 1, "restored": 0, "unchanged": 2}
```

### An item

```json
{
  "externalUid": "standup",
  "title": "Standup", "description": "Daily sync", "location": null,
  "allDay": false,
  "startAt": "2026-10-12T09:00:00+02:00", "endAt": "2026-10-12T09:15:00+02:00",
  "timeZone": "Europe/Berlin",
  "status": "active",
  "recurrence": {"frequency": "weekly", "interval": 1, "byWeekday": ["MO", "WE"], "until": "2026-12-31T22:59:59Z"},
  "exceptions": [
    {"recurrenceId": "2026-10-14T07:00:00Z", "cancelled": true},
    {"recurrenceId": "2026-10-19T07:00:00Z",
     "changes": {"title": "Standup (planning)", "startAt": "2026-10-19T08:00:00Z", "endAt": "2026-10-19T09:00:00Z"}},
    {"recurrenceId": "2026-10-21T07:00:00Z", "changes": {"description": null, "location": "Room 2", "priority": "high"}}
  ],
  "reminders": {"useDefault": true, "reminders": [{"kind": "relative", "minutesBefore": 30}]},
  "priority": "high"
}
```

Items are validated like events created in Life OS. The item covers everything today's Google derivation and ICS import write:

- **Time:** a timed event has `startAt` and `endAt`, and optionally `timeZone`. An all-day event has `startDate` and `endDate` (inclusive) and no time zone.
- **Status:** `cancelled` keeps the event as cancelled history, as a feed's `STATUS:CANCELLED` does.
- **Series:** `recurrence` is Calendar's model. A rule outside the model is sent as one event per occurrence, which is what Google's expansion does today. `exceptions` holds the series' changed and skipped occurrences:
  - `recurrenceId` is the occurrence's original start: a UTC instant for a timed series, a date for an all-day one. Other offsets are accepted and converted.
  - `changes` uses the stored form of `event_exceptions`. A `null` description or location clears it for that occurrence.
  - An exception must skip the occurrence or change something.
- **`reminders`** are what the source gives the event: Google's notifications. Leave them out when the source has none, as ICS feeds do. The owner's reminders then stay as they are.
  - With `reminders`, the event follows the source only while the owner has not changed its reminders. Calendar records what the source last sent (`source_reminders`), so an owner's own reminders survive every sync.
- **`priority`:**
  - `low`, `medium`, or `high` set the event's own priority, as Google's `lifeosPriority` property does.
  - `default` removes it, so the event follows the source's default priority.
  - Leaving it out keeps the event's priority as it is. That is how an owner's priority survives syncs from a source that carries none.
- **`readOnlyReason`** makes one event read-only although its source accepts changes (see [Concepts](#concepts)).
- **`previousExternalUid`** re-keys an event (see [Two-way calendar](#two-way-calendar-google-calendar)).

### Options

- **`window`** says which stored events the batch speaks for:
  - `{"start": "<instant>"}`: the source lists its events from that instant on, like Google's time window. A stored event that ended before `start` is history the source no longer lists, and it stays as it is. Today's Google sync uses this rule.
  - `{"upcomingOnly": true}`: the source lists only events still to come, as many ICS feeds do. A stored event that already started is neither removed nor rewritten, unless the batch moves it into the future. Today's ICS refresh uses this rule.
  - Without `window`, the batch speaks for every event of the source.
  - The rules apply to both modes. In a delta, they apply to `deleted` and to rewrites.
- **`emptyGuard`** (snapshot only): a snapshot with no items is refused with 409 `empty_snapshot` while the source still has upcoming active events. Nothing changes. Today's ICS refresh treats an empty feed this way. Set the flag when the provider returned nothing at all.
- **`feedCursor`**: the change-feed cursor the integration has handled. The sync is refused with 409 `unhandled_changes` while the feed still has changes to this source's events after that cursor that someone else made, so a sync never overwrites an edit made in Life OS that has not reached the provider yet. See [Two-way calendar](#two-way-calendar-google-calendar).

### Errors

| Status | Code | Meaning |
| --- | --- | --- |
| 400 | `invalid_sync_request` | Malformed batch: no or unknown `mode`, no `items`, `deleted` in a snapshot, both windows, a bad `feedCursor`, too many items |
| 400 | `invalid_item` | An item fails validation; the message names `items[i]`, its uid, and what to fix |
| 400 | `invalid_exception` | An exception fails validation (named the same way) |
| 400 | `duplicate_uid` | A uid twice in the batch, or both in `items` and `deleted` |
| 404 | `not_found` | The integration has no such source |
| 409 | `source_disabled` | The owner disabled the source |
| 409 | `source_in_process` | Calendar's own adapter still syncs it |
| 409 | `empty_snapshot` | See `emptyGuard` |
| 409 | `unhandled_changes` | See `feedCursor` |

A batch holds at most 10,000 items, and a request body at most 16 MiB. A large delta can be sent in several batches. A snapshot must fit in one request.

Calendar records nothing in Activity for a sync. The integration records its own outcome, as the in-process adapters do today: `calendar.source.refreshed`, `calendar.source.refresh_failed`, and `calendar.source.removed`, with subject `calendar_source` and the source's Calendar id.

## Change feed

`GET /v1/integrations/changes?cursor=&limit=&scope=` lists the changes of the events an integration may have to write elsewhere:

- **Life OS events** (`source.kind` `manual`), for example for the "Life OS" calendar in Google.
- **The events of sources that accept changes** (`readOnly: false`), so that edits made in Life OS reach the provider.

The events of read-only sources are not in the feed.

### Reading it

1. **Start without a cursor.** The feed begins with a snapshot (`phase: "snapshot"`): every followed event as it is now, ordered by id, page by page.
2. **Finish the snapshot.** The last snapshot page has `hasMore: false`. Its `nextCursor` continues with the changes made since the snapshot started, so nothing is missed. Every event the integration knows that the snapshot did not list no longer exists.
3. **Follow the changes.** Pass `nextCursor` back as `cursor`. It is always set. Store it after you have handled the page. `hasMore: true` means more entries are waiting now.

```json
{
  "phase": "changes", "hasMore": false, "nextCursor": "ZmVlZHw0Mg",
  "items": [{
    "eventId": "8e0c…", "deleted": false,
    "event": {"id": "8e0c…", "title": "Dentist (moved)", "...": "as GET /v1/events/{id} returns it"},
    "exceptions": [],
    "source": {"sourceId": "51d2…", "integration": "google-calendar", "externalId": "owner@example.com", "externalUid": "dentist"},
    "actor": "user", "actors": ["user"],
    "changedAt": "2026-10-10T10:00:01Z", "sequence": 42
  }]
}
```

### Entries

- **`event`** is the event's current state: the full event as `GET /v1/events/{id}` returns it, with `effectiveReminders` and the rest.
- **`exceptions`** holds a series' occurrence changes and skips, in stored form.
- **Deleted events** come as a tombstone: `deleted: true` and `event: null`.
- **`source`** tells an integration where its event belongs, including for a tombstone. It is null for a Life OS event.
- **`actors`** lists everyone who made the changes the entry stands for, and `actor` is the latest of them. Values are as in `X-Life-OS-Actor`.
  - Skip an entry as your own write only when `actors` is just `["system:<you>"]`.
  - The integration's own syncs of writable sources produce such entries, and Calendar's in-process Google reverse sync records `system:google-calendar`.
  - Snapshot entries have no actor.
- **Collapsing:** entries come in commit order, and several changes of one event within a page become one entry. With `limit=1`, every change is its own entry.
- **`scope`:**
  - `own` is the default for `system:<name>`: Life OS events plus the reading integration's own sources.
  - `all` adds other integrations' events. It is the default for `dev:<name>`.

### Retention

Entries are kept for `CALENDAR_CHANGE_RETENTION_DAYS` (default 30), and a small background loop prunes older ones every hour. A cursor older than that gets 410 `cursor_expired`. Start again without a cursor: the snapshot makes the integration whole again. `GET /v1/events?sourceKind=manual` also lists Life OS events.

### How the journal works

The feed reads the `calendar_event_changes` journal. Constraint triggers on `events` and `event_exceptions` write it in the same transaction as each change, so no code path can change a followed event without an entry: not the API, not the in-process Google reverse sync, not an integration's sync.

- **Actor:** the writing transaction names the actor with `SELECT set_config('calendar.actor', $1, true)` (the `SetChangeActor` query). API transactions use the request's actor and the in-process Google sync uses `system:google-calendar`. A write that names none is recorded as `system:calendar`.
- **Commit order:** the triggers are deferred to commit time and append while holding one advisory lock until the transaction ends. Entries are therefore numbered in commit order, and a reader that moves past a number never skips an entry still to come.
- **No-op updates:** an update that changes nothing but `updated_at` writes no entry.
- **Deleted sources:** deleting a source deletes its events without entries.

## Flows

### Read-only feed (calendar-feeds)

1. Register each feed:

   ```json
   {"name": "ZAW", "provider": "ZAW", "readOnly": true, "defaultReminders": [{"kind": "localTime", "daysBefore": 1, "localTime": "19:00"}]}
   ```

2. On every refresh: download the feed, parse it, apply the type filter (ZAW: the four bin types), and send a snapshot with `window: {"upcomingOnly": true}`. Add `emptyGuard: true` when the feed had no `VEVENT` at all.
3. Record the outcome (counts, failures, backoff) in Activity and in the integration's own state, as the refresher does today.

### Two-way calendar (google-calendar)

1. Register each Google calendar the owner syncs. Set `readOnly` to `true` unless the account's access role is owner or writer.
2. **Inbound:** derive items from Google's events as `internal/google/mapping.go` does today. Send a snapshot with `window: {"start": <the full listing's time window>}`, or deltas for incremental syncs.
3. **Outbound:** follow the change feed.
   - **Life OS events** (`source: null`): write them to the "Life OS" calendar. This replaces `google_exports`.
   - **Events of your writable calendars:** write the owner's edits to Google with the stored etag. Skip entries whose `actors` is only you.
4. **Recommended loop:** drain the feed and push what it holds, then sync with `feedCursor` set to the cursor you have handled. A 409 `unhandled_changes` means an edit arrived in between: drain again, then retry.
5. **Splits:** a series split in Life OS (This and following) creates its new series in the same source, with `externalUid` `lifeos:<uuid>`. Create it in Google. Then sync the item with Google's id as `externalUid` and the placeholder as `previousExternalUid`. Calendar re-keys the event in place and keeps its id.
6. **Conflicts:** if an edit conflicts with a newer version in Google, keep Google's version and sync it. It overwrites the Life OS edit, which is today's `google_conflict` behaviour.

### Recovering

- **410 `cursor_expired`:** read the feed without a cursor and reconcile against the snapshot.
- **409 `source_in_process`:** wait for the handover release.

## Notes for P11-07 and P11-08

These behaviours of today's in-process code are what the integrations reproduce:

- **Google source names and reminders:** a Google source's name follows the calendar's summary at every sync, and its default reminders follow the calendar's defaults whenever a listing carries them. The registration rule above gives the same result.
- **Google's own writes:** Google echoes Calendar's own writes back with their etag, and the sync ignores them (`withoutOwnWrites`). An integration does the same with its own store.
- **ICS feeds:** expanded occurrences are keyed `<UID>#<RECURRENCE-ID>`. The ZAW preset strips the location suffix from titles before filtering, and its default reminder is 19:00 the day before.
- **Editability:** for parity with `Target` in `internal/google/twoway.go`, set `readOnlyReason` on the occurrences of rules Calendar cannot hold (`<id>_<date>`) and on lone instances.

## Notes for migrations

The journal triggers are deferred. A migration that changes rows of `events` or `event_exceptions` and then alters either table in the same transaction must first run:

```sql
SET CONSTRAINTS events_record_change, event_exceptions_record_change IMMEDIATE;
```

Otherwise PostgreSQL refuses the `ALTER TABLE` because trigger events are pending.
