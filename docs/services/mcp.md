# MCP and AI

The Go MCP service is available behind `/api/mcp/mcp` using streamable HTTP, with `/api/mcp/health` and `/api/mcp/ready` probes. Its typed read tools call Finance (monthly summary, accounts, categories, transaction search), Tasks list/search, Inbox list, and Calendar list/search/get through the services' HTTP APIs. It also provides narrowly scoped mutation tools used by Inbox processing: `task_create`, `task_update`, `task_complete`, `task_reopen`, `calendar_create`, `calendar_update`, `calendar_cancel`, `calendar_delete`, `finance_create_manual_transaction`, `inbox_start_processing_attempt`, `inbox_record_processing_attempt`, and `inbox_resolve`. It never owns or queries Finance, Tasks, Inbox, or Calendar tables directly; upstream failures become understandable MCP tool errors.

Codex is told that a `taskQuery` is matched as a case-insensitive substring of Task titles and notes, so it proposes the shortest distinctive word from the title ("chicken", "Anna"). A statement that something was done, such as "I already bought the chicken", is a `task_complete` for the Task it refers to. Codex does not need to know the existing Tasks, because identity is resolved here.

The owner writes Tasks in English, Russian, and German, so a Task's title may be in another language or word form than the capture: "I already bought the chicken" has to find "Купить курицу на ужин". Codex therefore adds `taskQueryAlternatives`, up to six more terms for the same thing: translations into the other two languages and distinctive word stems that also match inflected forms (`"chicken"` plus `["куриц", "Hähnchen", "Huhn"]`). It omits them when the capture quotes the exact title.

For an existing Task mutation, the processor sends each term to `task_search` and unions the matching Tasks by id. It searches open Tasks for `task_complete` and `task_update`, and completed Tasks for `task_reopen`, so a finished Task with the same name never makes a capture ambiguous. It proceeds only when the union is exactly one Task:
- no Task: Human Review with no candidates;
- several Tasks, for example "Buy chicken wings" and "Купить курицу для супа": Human Review listing them (at most ten), and nothing changes;
- a plan with only a `taskQuery` is searched with that term alone, as before.

There is no fuzzy scoring: a Task is a candidate only if one of the terms literally occurs in its title or notes. Alternatives shorter than three characters are ignored, and duplicates (case-insensitive) are searched once. The resolved UUID is passed only to the owning Tasks API operation; the search terms never reach it. That includes a Human Review approval, which still carries the alternatives.

`make cross-language-smoke` proves this with the real Codex CLI and whisper.cpp on the verification stack:
- EN "I already bought the chicken." completes "Купить курицу на ужин";
- DE "Ich habe die Milch schon gekauft." completes "Buy milk";
- RU "Я уже вынес мусор." completes "Take out the trash", while unrelated Tasks in each language stay open;
- the spoken English sentence completes "Hähnchen fürs Abendessen kaufen" through Voice, although the completed Russian chicken Task still matches the words;
- with "Купить курицу для супа" and "Buy chicken wings" both open, the same sentence goes to Human Review with exactly those two candidates, and neither changes;
- no Task is created.

The non-interactive processor uses the existing local Codex CLI/ChatGPT login, not an API key. Compose starts it as the `processor` service during normal Life OS startup; it polls the raw Inbox at `LIFE_OS_PROCESSOR_POLL_MILLIS` (default one second), starts once immediately after boot, and exposes private `/health` and `/ready` on port 8081. `SIGTERM` stops polling and shuts that server down gracefully. The one-shot command remains a diagnostic only.

The deployed worker image contains `codex` but contains no authentication. Set `LIFE_OS_CODEX_HOME_HOST_PATH` to the host's existing authenticated Codex directory (normally `~/.codex`) and `LIFE_OS_CODEX_RUNTIME_UID` to its owner (`id -u`) in the ignored `.env`. At start, the service runs as that unprivileged UID, bind-mounts the directory read-only, and copies only `auth.json` plus optional `config.toml` into its own `/tmp` tmpfs. The source session, image, Git repository, and named Docker volumes never receive a copied credential. The worker runs with a read-only root filesystem, no Linux capabilities, and `no-new-privileges`. Check the same runtime context with `docker compose exec processor codex login status`; it must report the existing ChatGPT login before the worker can become operational.

`make processor-smoke` is the acceptance smoke for the actual deployed chain: gateway/Dashboard-style capture → persisted Inbox item → automatic processor polling → authenticated local Codex CLI → MCP `task_create` → Tasks persistence → processed audit record → Inbox resolution. It uses only the isolated `lifeos-verify-*` resources and never manually invokes the processor.

The processor reads raw items through `inbox_list`, asks Codex for a constrained JSON plan, accepts only high-confidence Task and Calendar actions and strictly validated manual finance transactions, records each attempt through MCP, and resolves an item only after every action succeeds. A planned finance action still requires an explicit account ID, signed amount, currency, and date (the processor's own rule; the MCP tool itself defaults to the Cash account and today, see [Finance](#finance)). An existing event is changed only when exactly one matches, and an AI-proposed cancellation always waits for the owner's approval (see [Calendar events](#calendar-events)); the processor never deletes an event. Ambiguous, low-confidence, and nothing-to-do plans go to Human Review and are never retried automatically. Technical failures are retried on a durable schedule (see [Retry scheduling](#retry-scheduling)). A retryable operation receives a distinct persisted attempt key while retaining its deterministic base operation key, so prior attempt history remains visible and the Inbox uniqueness constraint cannot block a subsequent retry. A processed base operation is skipped. `go run ./cmd/processor --dry-run` returns proposed MCP actions without creating attempts, mutating core services, or resolving Inbox items. `go run ./cmd/processor --content '...'` is a separate non-mutating diagnostic that prints only Codex's proposed JSON plan.

Each MCP call from the processor carries the Inbox item's `X-Correlation-ID`, the processing attempt's `inbox.processing.started` event as `X-Causation-ID`, and `X-Life-OS-Actor: agent:codex-inbox-processor`. MCP forwards these headers to the owning service. Every mutation tool records `mcp.action.requested` and then `mcp.action.completed` or `mcp.action.failed`, and the owning service's change names the request event as its cause. A caller that sends no actor header (or claims to be `user`) is recorded as `agent:mcp`, and that actor goes with every downstream call, so the owning services' actor gate always passes; a development tool calling MCP with `dev:<name>` keeps its own actor. See [Activity](activity.md).

For every allowed mutation, the processor derives a stable per-action key from the Inbox operation, action index, tool, and canonical arguments. MCP forwards it to Tasks, Calendar, or Finance as `Idempotency-Key`. Those services persist the key and a payload hash with the created row: repeating the same key and payload returns the original row, while reusing a key with materially different content returns `409`. Manual callers may omit the header and retain the usual create behavior. Idempotency metadata is retained with the owned record for its lifetime; it is not in-memory state.

## Finance

MCP's Finance tools are Finance API operations, the same ones the Dashboard uses; MCP never reads Finance's database (see [Finance](finance.md)):

| Tool | Finance API |
| --- | --- |
| `finance_search_transactions` | `GET /v1/transactions` with typed filters: `q` (words in the counterparty, description, or notes), `accountId`, `categoryId` (an id or `uncategorized`), `from`/`to`, `direction` (`in`/`out`), `amountMin`/`amountMax` (absolute minor units), `status`, `source`, `limit` (default 20), and `cursor`. Ids, dates, and enum values are checked before Finance is called |
| `finance_find_transactions` | `GET /v1/transactions` with the caller's raw URL query (kept for older callers) |
| `finance_list_accounts`, `finance_list_categories` | `GET /v1/accounts` (balances, the built-in Cash account first), `GET /v1/categories` |
| `finance_month_summary` | `GET /v1/summaries/monthly` (`month=YYYY-MM`, optional `currency`, `accountId`) |
| `finance_create_manual_transaction` | `POST /v1/transactions`, with the action's `Idempotency-Key` |

`finance_create_manual_transaction` needs only `amount` (`{"amount": <signed minor units>, "currency": "EUR"}`; a decimal amount is refused): without `accountId` the transaction goes to the built-in **Cash** account, so "spent 12 euros at the bakery" is `{"amount": {"amount": -1200, "currency": "EUR"}, "counterparty": "Bäckerei"}`; without `occurredOn` it is dated today in the owner's timezone; without `categoryId` the category rules choose. It also takes `counterparty`, `description`, and `note`. Its field names stay those the processor plans with: MCP sends `occurredOn` as Finance's `bookingDate` and `note` as `notes`. `accountId` and `categoryId` must be UUIDs (an account name is refused with a hint to leave it out for Cash) and `occurredOn` a date, checked before Finance is called. It records `mcp.action.requested`/`completed`/`failed` like every mutation, and Finance records the transaction itself (`finance.transaction.created`, caused by the request event).

## Calendar events

MCP's Calendar tools are Calendar API operations, the same ones the Dashboard uses; MCP never reads Calendar's database:

| Tool | Calendar API |
| --- | --- |
| `calendar_list`, `calendar_search` | `GET /v1/events`, `GET /v1/events/search` (the caller's URL query, such as `q=Zahnarzt&from=…&timeZone=Europe/Berlin`) |
| `calendar_get` | `GET /v1/events/{id}` |
| `calendar_create` | `POST /v1/events` with `title`, `description`, `location`, `allDay`, `startAt`/`endAt` or `startDate`/`endDate`, `timeZone`, `reminders` (the event's own, added to the owner's default reminders), `useDefaultReminders` (left out: true; `false` with no `reminders`: none at all), `priority` (`low`, `medium`, or `high`; left out: medium), and the action's `Idempotency-Key` |
| `calendar_update` | `PATCH /v1/events/{id}` with only the given fields (`reminders` replaces the event's own, `useDefaultReminders` turns its defaults on or off, `priority` sets how important it is); `clear` names fields to remove (`description`, `location`, `timeZone`, `reminders`, which returns the event to its defaults alone), sent as `null`. A field both set and cleared, an unknown `clear` entry, or nothing to change is rejected before Calendar is called |
| `calendar_cancel` | `PATCH /v1/events/{id}` `{"status":"cancelled"}`: kept as history, restorable. With `recurrenceId` (an occurrence's `occurrence.recurrenceId`: an RFC 3339 instant or a date, else rejected before Calendar is called) it skips only that occurrence of a recurring event: `PATCH /v1/events/{id}/occurrences/{recurrenceId}` `{"status":"cancelled"}` |
| `calendar_delete` | `DELETE /v1/events/{id}`, permanent; for an MCP client acting on the owner's explicit request. The processor never uses it |

`calendar_create` and `calendar_update` take no `recurrence` (the MCP server rejects the field): recurring events are created and changed only in the Calendar (the Dashboard). `eventId` must be a UUID. Every mutation carries the caller's `X-Life-OS-Actor` (or `agent:mcp`) and records `mcp.action.requested`/`completed`/`failed`.

**Planning.** Codex may propose `calendar_create`, `calendar_update`, and `calendar_cancel`. Tasks are to-dos (call, buy, renew, pay, …) with an optional due date; Calendar events are appointments and meetings with a time and maybe a place. "Move the electrician call to Friday" is a Task change. A place goes in `location`, not the title ("Add dentist at Dr. Weber, Friday 15:00 for an hour" is title "Dentist", location "Dr. Weber"). Moving or changing an event is `calendar_update` with an `eventQuery`, `eventQueryAlternatives`, and only the changed fields; cancelling, deleting, or removing one is `calendar_cancel` with only the search terms. A request to be reminded of, or to add a reminder to, an event that already exists is `calendar_update` with an `eventQuery` and `reminders`, also when the item restates the event. The search terms follow the Task rules above: the shortest distinctive word, plus up to six translations and stems in the owner's other languages (`"dentist"` plus `["Zahnarzt", "стоматолог", "зубн"]`). To reschedule, Codex gives the new start, and the end only when the capture states one or a duration. Relative dates follow the owner's timezone as described in [Dates and the owner's timezone](#dates-and-the-owners-timezone). A planned action can never name an event id: `eventId` or `taskId` in a planned Calendar change makes the plan invalid.

**New events.** A planned `calendar_create` without an end gets the default length, as in Google Calendar: a timed event lasts one hour and an all-day event is its one day ("Add a haircut on Sunday at 11:00" names no duration). Reminders the item asks for are the event's own and add to the owner's default reminders.

**Default reminders stay on.** AI reminders only ever add to the owner's default reminders; turning an event's defaults off or on is the owner's, in the Calendar (the Dashboard). The plan prompt does not offer `useDefaultReminders`, and the processor drops it from every planned or approved `calendar_create` and `calendar_update` before validating, so a plan that sends it does not fail: the field is left out of the MCP call and the attempt's action records `"droppedFields":["useDefaultReminders"]`. (Codex once planned `useDefaultReminders: false` unasked for "Add a haircut … remind me a day before and 3 hours before", which would have dropped the owner's 30-minute default.) An update that only turned the defaults off or on goes to Human Review with no candidates and "Default reminders are turned off or on in the Calendar.". The MCP tools keep the parameter for other clients.

**Restated events (duplicate guard).** A capture sometimes restates an event that already exists to add to it, and Codex still plans a `calendar_create`: "Сегодня в 8 вечера начало свидания с Ирой. Ну, пускай будет 3 часа." created "Свидание с Ирой" 20:00–23:00, and four minutes later "Сегодня в восемь вечера свидания с Ирой. Напомни мне за два часа до этого…" was planned as the same event 20:00–21:00 with a 120-minute reminder (BUG-33). So before every planned `calendar_create` the processor calls `calendar_search` once for the planned title on the planned start's local day (`from`/`to` that day in the capture's timezone, the same `timeZone`, `limit=10`, and the same filter as every event lookup, `changeableEvents`: today `sourceKind=manual`). Calendar matches words with their diacritics, so each word is searched by its longest part without any ("Zahnärztin" as "rztin"; at most ten words, 200 characters). A found event is a **duplicate** when it is of the same kind and starts when the plan does (a timed event within five minutes, an all-day event on the same date) and its title equals the planned one apart from case, diacritics, punctuation, and spacing ("свидание с Ирой!" is "Свидание с Ирой"; "Свидание с Ирой в кино" is not). Then:
- no duplicate: the event is created as before;
- exactly one: the create becomes a `calendar_update` of that event carrying only what the capture adds: `reminders` as the event's own plus the new ones, each once (as for any AI update, below); `location` and `description` only when the event has none (its own are never overwritten); `priority` when it differs; and a new end only when the plan states one. An end exactly the default length after the planned start (an hour, or the one day of an all-day event) is what Codex fills in when the capture names no duration, so it never cuts a longer event short ("…for two hours" after a one-hour event does set 21:00). The event keeps its title and start. The processing attempt records `{"tool":"calendar_update","arguments":{"eventId":…,"reminders":[…]},"convertedFrom":"calendar_create","plannedArguments":{…}}`. When the event already has everything (the same capture twice), nothing is called and the attempt records `{"tool":"none","convertedFrom":"calendar_create","duplicateOf":…,"reason":"The event already exists and the capture adds nothing to it"}`; the item resolves. A recurring occurrence as the one duplicate goes to Human Review with "Recurring events are changed in the Calendar.";
- several: Human Review with those events as candidates, the planned `calendar_create` as the proposed action, and "This looks like an event that already exists: choose it to update it". Approving one runs the create as the update of that event (the processor reads it with `calendar_get`; nothing is searched or planned again); discarding the item creates nothing.

Events the filter leaves out (feed events, and Google events, which are managed by their source) are never duplicates, so a capture of the same title and time still creates a Life OS event. A failed duplicate search is retried like any failed search, and nothing is created meanwhile.

**Priority.** `calendar_create` and `calendar_update` may carry `priority` (`low`, `medium`, or `high`; the processor refuses any other value as an invalid plan). The plan prompt asks for it only when the item says how important the event is: `high` for important or urgent ("important", "urgent", "wichtig", "важно"), `low` for not important or low priority; otherwise it is left out, so a new event is medium and an update keeps the event's priority. "Das Visum-Gespräch ist wichtig" is a `calendar_update` of the one matching event with only `priority: "high"`. The event's priority becomes the importance of its reminder notifications.

**Resolution.** For each term the processor calls `calendar_search` with `from` set to the start of the capture's day in its timezone (the capture's own, else `LIFE_OS_TIMEZONE`), the same `timeZone`, `editable=true`, and `limit=10`, and unions the events by id. Calendar leaves cancelled events out, so only active events the AI may change from that day on are candidates: Life OS events and, since two-way Google sync (P11-03), Google events of calendars the owner may edit, which Calendar changes in Google first (a single one is changed like a Life OS event; a recurring one only in the Calendar, see below; cancelling a Google occurrence skips it through Google's instance). Feed (ICS) events, Google calendars the owner can only read, and occurrences of Google rules Calendar cannot hold are managed by their source and are never candidates; last year's appointment of the same name does not make a capture ambiguous. A recurring event is found as one occurrence (the search item carries `occurrence`), its next one not over yet, so there is at most one candidate per series. Then:
- exactly one event and `calendar_update`: the processor calls `calendar_update` with that `eventId` and the changes. When the plan gives only a new start, it keeps the event's length (a timed event's duration, an all-day event's number of days); making an event all-day with only a start date makes it that one day. Planned `reminders` **add** to the event's own ("remind me 2 hours before" keeps the day-before reminder it has): the processor sends the event's own reminders (from the search result, or for an approved update from `calendar_get`) followed by the new ones it lacks, each once, since the MCP tool replaces the list. Only a plan that also names `reminders` in `clear` replaces them (the item asks to remove or replace its reminders, such as "only remind me 2 hours before"; the plan prompt says so in one clause); the processor then sends just the new ones and drops `reminders` from `clear`. A plan whose reminders the event already has sends only its other changes, or when there are none calls nothing and records `{"tool":"none","plannedTool":"calendar_update","eventId":…,"reason":"The event already has these reminders, so nothing changed"}`, and the item resolves;
- several events: Human Review listing them (at most ten), and nothing changes;
- no event: Human Review with no candidates; nothing is created as a fallback;
- `calendar_cancel`: **always** Human Review, even with exactly one match, which is then the only candidate. Nothing is cancelled until the owner approves.

**Recurring events.** The owner decided that recurring events are made and changed only in the Calendar, and that the AI may only skip one occurrence when asked ("Cancel gym today" cancels just today's gym). So:
- a planned `calendar_create` or `calendar_update` that carries a `recurrence` goes to Human Review ("Recurring events are created in the Calendar." or "Recurring events are changed in the Calendar.") and changes nothing; the plan prompt asks Codex to answer a request for a new repeating event with no action and that reason;
- a `calendar_update` whose one match is a recurring event goes to Human Review with no candidates and "Recurring events are changed in the Calendar.", and changes nothing (also when the plan was uncertain); an approved update of an event that turns out to recur (the processor reads it with `calendar_get`) is refused the same way;
- `calendar_cancel` may carry `onDate`, the ISO date of the day the item names ("today" is the capture's local date). Then the processor first searches each term with `from`/`to` set to that local day and takes only recurring occurrences from it, so a recurring match stands for its occurrence on that day; a series without an occurrence that day stands for its next occurrence (the usual search), and so does every recurring match without `onDate`. Single events are found as before. The candidate's title names the occurrence's date (`Gym · Sat 10 Oct 2026 07:00–08:00`) and it carries `recurrenceId`. Because Inbox keeps only the chosen candidate's id (the series id), the proposed action stores `occurrences`, a map from series id to recurrence id (a map the plan brought is replaced). On approval the processor passes the chosen series' recurrence id to `calendar_cancel` as `recurrenceId`, so only that occurrence is skipped, never the series. An approved cancellation of a recurring event without a recorded occurrence (for example from a review made before this) is refused with "Recurring events are changed in the Calendar.".

**Reschedules across domains.** Codex sometimes plans a reschedule for the wrong domain, such as `calendar_update` for "Move the electrician call to 2026-10-21" while "Call the electrician" is a Task. When a `calendar_update` that sets a new start finds no event, the processor searches open Tasks with the same terms; exactly one match runs the equivalent `task_update` (`dueAt` is `startAt` as given, or for a `startDate` the owner-local midnight of that date, like a date-only Task; title and description/notes carry over, event-only fields are dropped). Symmetrically, a `task_update` that sets `dueAt` and finds no open Task searches manual events, and exactly one match runs the equivalent `calendar_update` (an all-day event gets the date; a `dueAt` at the owner-local midnight is a date-only due date, so a timed event moves to that date at its original local time of day, also across a daylight-saving change; the event keeps its length). A planned or approved `calendar_update` that gives only a `startDate` for a timed event is completed the same way. Several matches in the other domain go to Human Review with those candidates and the equivalent action, so approving one runs it; nothing in either domain goes to Human Review with the plan as given ("No open Task or upcoming Calendar event matches…"). The planned domain is always searched first and wins when it matches. Cancellations, completions, reopenings, and changes without a new time never cross domains.

A low-confidence or ambiguous plan with one Task or Calendar change also goes to Human Review with the matching Tasks or events (after the same fallback). If Calendar still refuses a change with `409` `managed_by_source` or `google_read_only` (for example an approved action on a feed event, or on a Google calendar that became read-only), the item goes to Human Review with no candidates and "This event is managed by its source and can't be changed by capture", never to a technical retry. Google being unreachable or the connection expired (`google_unavailable`, `google_expired`, `google_not_connected`) is a dependency failure, retried later like an unavailable service; `google_conflict` (the event changed in Google meanwhile) is retried like any refused action, and the next attempt finds Google's version. A failed search is retried like any failed action; a partial result is never acted on. Updates and cancellations carry no idempotency key: a retried `PATCH` with the same fields leaves the event the same. As for Tasks, when one action of a plan goes to Human Review, the actions before it have already run and the ones after it do not.

**Human Review.** It uses the existing Inbox mechanism unchanged: the proposed action (with its search terms and changes) and the candidates are stored on the item. A Calendar candidate carries the event's `id`, `kind: "calendar_event"`, the event's own title as `eventTitle`, its times, location, and status, and a `title` the Dashboard shows, saying when (in the owner's timezone) and where: `Dentist · Fri 16 Oct 2026 15:00–16:00 · Dr. Weber`. The `errorSummary` says what waits, for example "Cancelling a Calendar event needs your approval: choose the event to cancel it" or "Several Calendar events match: choose the one to change". Choosing a candidate (`POST /v1/inbox-items/{id}/human-review/resolve`) queues the item again with the approved action, in which Inbox records the chosen candidate as `taskId`, its one selection field. The processor then runs that action on that event without planning or searching again: it takes the id as `eventId`, drops the search terms, and for an update that gives only a new start reads the event (`calendar_get`) to keep its length.

`make calendar-ai-smoke` proves this with the real Codex CLI on the verification stack, with `LIFE_OS_TIMEZONE=Europe/Berlin`:
- "Add dentist at Dr. Weber, Friday 15:00 for an hour" creates one event, 15:00–16:00 Berlin on the next Friday (Monday when run on a Friday), with location Dr. Weber;
- "Move the dentist appointment to next Monday at 9" updates that event (by id) to 09:00 Berlin on the coming Monday (or the one after), still one hour, same title and location, and creates nothing;
- "Cancel the dentist appointment" goes to Human Review with that event as the only candidate, and it stays active; approving it through the Inbox API cancels exactly that event, which stays readable with `cancelledAt`;
- with a 90-minute event titled "Zahnarzt", the English "Move my dentist appointment to the day after tomorrow at 11:30" moves it to 11:30–13:00 Berlin two days after capture;
- with "Team meeting" and "Meeting with Anna", "Move my meeting to Thursday at 14:00" goes to Human Review with both as candidates, and neither changes;
- with the owner's 30-minute default, "Add a haircut … remind me a day before and 3 hours before" gives the new event those two reminders of its own on top of the default;
- with a weekly "Gym" series that has an occurrence today, "Cancel gym today" goes to Human Review with exactly today's occurrence (its `recurrenceId` recorded in the proposed action); approving it cancels only that occurrence, while next week's and the series stay active;
- "Tomorrow at 19:00 dinner with Anna for two hours." and then "Tomorrow at 19:00 dinner with Anna, remind me two hours before." leave exactly one dinner with Anna, 19:00–21:00 Berlin, with a 120-minute reminder of its own: the second capture is resolved by one `calendar_update` of the first event;
- no other event is created.

Writes in the smoke are sent as `X-Life-OS-Actor: dev:smoke-calendar-ai`. The Calendar image must include the P10-01 search API.

## Processor status

The processor's private health port (`8081`, not exposed through the gateway) serves `/health`, `/ready`, and `/status`. Like `/health` and `/ready`, `/status` is an operational endpoint outside the OpenAPI contracts. It holds what the running process has observed since it started, in memory, and resets on restart:

```json
{
  "model": "gpt-5.6-luna",
  "timeZone": "Europe/Berlin",
  "startedAt": "…",
  "poll": {"lastAt": "…", "lastSucceededAt": "…", "lastError": "…"},
  "codex": {
    "state": "ready | usage_limited | unauthenticated | failing | unknown",
    "authenticated": true,
    "authCheckedAt": "…",
    "lastSucceededAt": "…",
    "lastFailedAt": "…",
    "lastError": "ERROR: You've hit your usage limit… try again at 6:22 PM.",
    "consecutiveFailures": 3
  }
}
```

- `authenticated` comes from `codex login status`, checked at startup and every five minutes. It reads local credentials only and uses no model quota.
- `state` is derived as follows:
  - `unauthenticated` when the login check fails;
  - otherwise, after a failed planning run, the failure is classified: `usage_limited` (usage or rate limit), `unauthenticated` (401 or not logged in), or `failing` (anything else);
  - otherwise `ready` after a successful run, or `unknown` when Codex has not run yet.
- `timeZone` is the processor's configured timezone (`LIFE_OS_TIMEZONE`), which captures without their own browser timezone are planned in (see [Dates and the owner's timezone](#dates-and-the-owners-timezone)).
- The Dashboard Status page (`/status`) renders this, together with the Inbox and Voice backlogs.

### Codex usage and reset information

**Data source.** The processor starts `codex app-server` (stdio JSON-RPC) every five minutes and calls two methods of the Codex CLI's own app-server protocol: `account/read` and `account/rateLimits/read`. Their schema is published by `codex app-server generate-json-schema`, and they are the same data the Codex apps use to show usage. The calls use no model quota. The result appears under `usage.current` on `/status`, and `processor --usage` prints it for diagnostics.

**Reported fields:**
- auth mode, for example `chatgpt` for a subscription sign-in;
- plan, for example `plus`;
- one entry per metered bucket. For example, `codex` with a 5-hour and a weekly window, and a separate `gpt-reserve` bucket. Each window has a used percentage, a window length, and a reset time;
- whether the backend reports a bucket's limit as reached;
- how many one-off "full reset" credits the account has.

The account email is never stored.

**Reliability and limits:**
- The percentages and reset times are the backend's own numbers, shown unchanged; Life OS never estimates them.
- Codex marks the app-server protocol as experimental, so a future CLI version may change it. Today's response shape is pinned in `usage_test.go`, using a real Codex CLI 0.160.0 response.
- The usage is a snapshot that can be up to five minutes old (`usage.current.checkedAt`).
- The response does not say which bucket a given model draws from. For example, the `gpt-reserve` bucket lists `gpt-5.6-luna` as its normal model, while `gpt-5.6-luna` may also count against `codex`. The Dashboard therefore lists every bucket rather than guessing which one applies.
- Reset credits are only counted. Life OS never spends them.

**Fallback.** If a read fails (an older CLI, an unsupported account, or an app-server error), `usage.error` is set and the last good snapshot, if any, is kept with its timestamp. With no snapshot, the Dashboard shows "Codex usage: unavailable" and still relies on the classified `usage_limited` state from failed runs, together with Codex's own "try again at …" text.

The Status page lists these values under Automatic processing. When Codex is usage-limited and a bucket is reported as reached or at 100%, the suggested action names that bucket's reset time.

A failed Codex run keeps only the CLI's own `ERROR:` lines, without the transport retry lines (`Reconnecting… n/5`), in its error and in the Inbox attempt's `errorSummary`. The full output echoes the banner and the prompt, including the user's capture. That buried the actual reason, such as a usage limit, behind text that was then truncated.

If the MCP service restarts, it forgets every session. On the resulting `404`, the processor starts a new MCP session and retries the rejected call once, which the Streamable HTTP transport requires. The rejected request was never processed. Previously the processor kept its stale session and failed every poll until it was restarted itself.

## Dates and the owner's timezone

Each capture is planned in the owner's timezone at the moment of capture: the capturing browser's timezone when the capture names one, else `LIFE_OS_TIMEZONE`, else UTC. The Dashboard's text and voice captures send the browser's IANA zone as `sourceMetadata.timeZone` (the Voice worker carries the upload's `sourceMetadata` over to the Inbox item). The processor uses it when `time.LoadLocation` accepts it (and it is not `Local`); an unknown or missing zone falls back to the configured one, so captures from the Watch, scripts, or MCP clients without metadata keep working as before.

`LIFE_OS_TIMEZONE` (an IANA name such as `Europe/Berlin`; default `UTC`) is the configured fallback. Compose passes it to the processor and also sets the processor's `TZ` to it. The processor validates it with `time.LoadLocation` at startup; an unknown name is logged and falls back to UTC (as in the Dashboard) rather than stopping capture processing. The processor binary embeds `time/tzdata` and the image installs `tzdata`, so any zone resolves both for the processor and for the Codex CLI.

Every planning prompt states the capture's zone and the local date, time, and weekday, lists the coming seven days with their weekdays and UTC offsets (which covers a daylight-saving change), and gives a worked example ("tomorrow at 10:00 is 2026-10-11T10:00:00+02:00"). Codex is told to:
- resolve "today", "tomorrow", "tonight", "this/next week", and bare clock times in the owner's timezone;
- read a weekday ("on Friday") as its next occurrence that is not already past;
- write `dueAt`, `startAt`, and `endAt` as RFC3339 with the owner's UTC offset on that date, and `startDate`, `endDate`, and `occurredOn` as the owner's local date.

Relative dates mean what they meant when the owner captured the item, so the reference is the Inbox item's `createdAt`, not the moment Codex runs. A capture taken at 23:59 and planned just after midnight, or retried hours later after a usage limit, keeps its "tomorrow". When planning is more than ten minutes late, the prompt also states the current time. Every Codex run gets `TZ` set to the zone it uses (a planning run the capture's zone; `login status` and usage reads the configured one), so Codex's own environment context agrees with the prompt. The owning services store instants, so an offset time and its UTC equivalent are the same value. `--content` diagnostics have no capture time and use the current time.

`make relative-dates-smoke` proves this with the real Codex CLI on the verification stack, with `LIFE_OS_TIMEZONE=Europe/Berlin`:
- the processor reports the zone on `/status`, and its container resolves it (tzdata);
- "Remind me to call mom tomorrow at 10." creates a Task due at 10:00 Berlin time on the day after capture;
- "dentist on Friday at 15:00 for one hour" creates a 15:00–16:00 Berlin event on the next Friday (Monday when run on a Friday, which would be ambiguous);
- "Anna's birthday the day after tomorrow" creates an all-day event on the Berlin date two days after capture.

Expectations are computed with `TZ=Europe/Berlin date` from each item's own `createdAt`.

## Retry scheduling

The processor polls only due work (`inbox_list` with `state=raw&due=true`). It classifies each failure and asks Inbox to persist when the item may be retried (`nextRetryAt`, `retryReason`):

| Failure | Example | Next attempt |
| --- | --- | --- |
| Codex usage limit | `ERROR: You've hit your usage limit…` | At the reset time the Codex CLI reports for the exhausted bucket (`account/rateLimits/read`, read fresh and cached for a minute), plus up to a minute of jitter. Reason `usage_limited`. Without a reported reset, it backs off. |
| Other Codex failure | sign-in, transport, CLI error | Backoff. |
| Dependency unavailable | MCP or an owning service unreachable | Backoff. |
| Unusable model output | invalid JSON, a disallowed tool, an action the owning service rejects | Backoff. After 5 attempts the item becomes `failed` (operator attention). |
| Ambiguous, low confidence, or nothing to do | a quotation, an unclear request | Human Review. Never a technical retry. |

Backoff is 30 s, doubling per earlier attempt of the item, capped at one hour, with ±20% jitter. Reason `backoff`. A retry adds an audit attempt; it never repeats a processed operation. Retry-After headers and explicit recovery intervals are not exposed by the current dependencies, so the only explicit recovery time used today is the Codex usage reset. A nothing-to-do capture appears in the Human Review queue with Codex's reason and no candidates; the user can discard it there, or capture it again with more detail.

`make retry-schedule-smoke` proves the following on the verification stack, with real Inbox, MCP, processor, Codex, and Tasks:
- ambiguity goes to Human Review;
- no retry happens before a reported reset, even across a processor and Inbox restart;
- the work completes after the reset;
- backoff runs 30 s, then 60 s.

A usage limit cannot be triggered on demand, so `tools/fixtures/codex-limit-simulator` simulates one at the Codex CLI boundary for that smoke only.

## Codex runtime

The routine planning profile is `gpt-5.6-luna` with reasoning effort `medium` (`LIFE_OS_CODEX_MODEL`, `LIFE_OS_CODEX_REASONING_EFFORT`), chosen by the [AI runtime benchmark](../verification/ai-runtime-benchmark.md). The effort is passed to Codex as `-c model_reasoning_effort=…`; empty keeps the Codex configuration's default. Attempts record the profile as `model/effort`, and `/status` reports the effort and the token usage Codex reports per run.

Codex is the processor's agent runtime. It runs as a managed subprocess of the machine's existing ChatGPT sign-in, with no API key (`internal/processor/codex.go`). Every planning run gets:

- **Binary.** `LIFE_OS_CODEX_COMMAND` is resolved once at startup. A missing CLI stops the processor with exit code 78.
- **Working directory.** The explicit `LIFE_OS_CODEX_WORKDIR`, in a read-only sandbox (`-s read-only`).
- **Environment.** Only `PATH`, `HOME`, `USER`, `CODEX_HOME`, locale, `TZ` (the capture's timezone when it names one, else the configured one), `TMPDIR`, and CA settings. API keys, service URLs, and other secrets are never inherited.
- **MCP servers.** None: `-c mcp_servers={}` ignores servers in the host Codex config.
- **Process group.** Each run has its own process group, so nothing it starts can outlive it.
- **Deadline.** `LIFE_OS_CODEX_TIMEOUT_SECONDS` (default 300).
- **Cancellation.** Graceful: `SIGTERM` to the group, then `SIGKILL` after 5 s. The whole group is killed after every run.
- **Concurrency.** At most `LIFE_OS_CODEX_MAX_CONCURRENCY` runs at once (default 1).
- **Structured output.** `--json` events give the Codex session id (`thread.started`) and the failure reason (`turn.failed`). `-o` captures the final plan.

### Lifecycle

The Inbox processing attempt is the durable job record. It exists in Inbox before Codex is spawned, and moves through these runtime states:

`spawning` → `running` (with the Codex session id once reported) → `completed` / `failed` / `timed_out` / `cancelled`.

The processor heartbeats every 30 s until the attempt's outcome is stored. Outcomes are always stored, even during shutdown:

- **Normal stop (`SIGTERM`, for example `docker compose stop` or a redeploy).** The run is cancelled and the capture is handed back as retryable, with reason `cancelled`, due now. The processor has a 30 s stop grace period.
- **Timeout.** The run is stopped and the capture backs off, like any other Codex failure.
- **Crash (`SIGKILL`, OOM, host reboot).** The attempt keeps its last runtime state and stops heartbeating. Every 30 s, and at startup, the processor reconciles attempts that another instance owned and whose heartbeat is older than `LIFE_OS_PROCESSOR_STALE_AFTER_SECONDS` (default 120). They become retryable, with reason `recovered`, due now. This also recovers captures orphaned before runtime tracking existed.

**Resume.** `codex exec resume` requires a persisted session. The processor's Codex home is a tmpfs, runs are `--ephemeral`, and a crash loses the session anyway. Planning is a single read-only turn, so the safe fallback is to plan again. The recorded session id stays on the attempt for audit. Re-planning cannot duplicate a domain change: Codex never executes actions, and the processor sends every Task, Calendar, or Finance mutation with a deterministic idempotency key derived from the operation. Each instance gets a unique id (`runtimeOwner`), so reconciliation never takes over a run the current processor still owns.

`/status` reports the runtime under `runtime`:
- the instance, binary, timeout, and concurrency;
- the run in progress, with its state, session, and start time;
- counts of completed, failed, timed-out, cancelled, and recovered runs since start;
- the last reconciliation error.

`make codex-runtime-smoke` proves the lifecycle with the real Codex CLI on the verification stack:
- normal execution;
- the sanitized environment of a live run, read from `/proc`;
- cancellation when the processor stops mid-run;
- a 2 s deadline, leaving no Codex process behind;
- `SIGKILL` crashes, both before and after Codex reports its session, reconciled by the next processor.

Each scenario ends with exactly one Task. The real CLI reports its session within a fraction of a second, so the spawned-but-no-session window is held open by the smoke-only wrapper `tools/fixtures/codex-limit-simulator` (mode `slow-start`). It waits 45 s, then runs the real CLI.

