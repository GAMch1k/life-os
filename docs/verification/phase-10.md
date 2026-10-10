# Phase 10 verification

Phase 10 turned Calendar from a skeletal service into a usable personal calendar: a complete event lifecycle with search, a FullCalendar workspace, AI and MCP parity for creating, finding, rescheduling and cancelling events, generic ICS feeds with ZAW waste collection as the first real source, and Calendar in the Event Log, on Home and on Status. See [Calendar](../services/calendar.md), [MCP and AI](../services/mcp.md) and the [Dashboard](../frontend/dashboard.md).

As in earlier phases, unit and integration tests prove the pieces. The sign-off on 2026-10-10 used the normal user triggers on the disposable `lifeos-verify` stack: browser actions in Chromium, captures through the Inbox API the Dashboard uses, the real automatic processor, and the authenticated Codex CLI (`gpt-5.6-luna`, medium). The ZAW import was also checked on the live stack.

## Commands

| Command | What it proves |
| --- | --- |
| `make browser-e2e` | Every browser spec, now started with the verify-only `tools/fixtures/calendar-ics.compose.yaml` overlay, so the ICS import tests run against fixture feeds instead of skipping. |
| `make calendar-ai-smoke` | Codex creates, reschedules and cancels Calendar events through MCP and the Calendar API, across languages, without guessing ambiguous targets. |
| `make relative-dates-smoke` | Relative dates in captures resolve in the owner's timezone (Europe/Berlin). |
| `make phase8-acceptance`, `make runtime-status-smoke` | The Phase 8 chains and the Status scenarios still hold, including the new Calendar feed health. |
| `LIFE_OS_BENCHMARK_DIR=… ./tools/benchmark-ai-runtime gpt-5.6-luna/medium` | The routine AI profile on 20 scenarios, including Calendar reschedule, ambiguity and cancel. |

## Required acceptance

| # | Required | Evidence |
| --- | --- | --- |
| 1 | The browser creates an event | `calendar-workspace.spec.ts`: a timed and an all-day event are created in the dialog and persisted. |
| 2 | The browser edits and reschedules it | The same spec edits the event, changes its date through the form and by dragging, and checks every view after a reload. |
| 3 | The browser deletes it | The same spec deletes it after confirmation, and it is gone after a reload. |
| 4 | Inbox, Codex and MCP create or update an event through the Calendar API | `smoke-calendar-ai`: "Add dentist at Dr. Weber, Friday 15:00 for an hour" creates one event 15:00–16:00 Berlin with its location. "Move the dentist appointment to next Monday at 9" moves the same event and keeps its hour. An English capture moves "Zahnarzt" and keeps its 90 minutes. |
| 5 | An ambiguous existing-event mutation does not guess | `smoke-calendar-ai`: "Move my meeting" with two meetings waits in Human Review with both as candidates, and neither changes. "Cancel the dentist appointment" waits in Human Review, as every AI cancellation does; approving it cancels exactly that event. Benchmark `cal_ambiguous` and `cal_cancel` pass. |
| 6 | An ICS feed imports and refreshes without duplicates | Calendar integration tests (sanitized ZAW-style fixture): first import, identical refresh with no duplicates, changed event updated in place, removed future event cancelled, returning UID restored, failure keeps the last good data. Browser: "each feed refresh is one entry that says what it changed" ("— 2 added", "— 1 added, 1 cancelled"). |
| 7 | The ZAW feed behaves as a normal ICS source | Live, 2026-10-10: Calendar ensured the `zaw` preset at startup and imported 21 of the feed's 29 entries, only Bioabfall, Gelber Sack, Papier and Restmüll 14-täglich, at 06:00 local, with reminders the day before at 19:00. A restart logged `action=unchanged` and kept the same 21 events. |
| 8 | Calendar changes appear in Activity | Browser: "an event you edit in the Calendar shows as yours, apart from the tool that created it" and "a failed feed refresh shows as the feed's own entry". Live: `calendar.source.refreshed` by `system:ics-zaw`, "Refreshed ZAW: 21 new, 0 changed, 0 removed, 0 restored". |
| 9 | Home shows an upcoming event appropriately | Browser: "the next event is an active one that has not started, opening the Calendar at its day" and "an imported next event names the feed it comes from". |
| 10 | Restart and reload preserve the Calendar state | Every workspace test reloads after changing an event. The live Calendar restart kept the ZAW source and its events unchanged. |

## Results

Final run on `master` (after P10-06), one job at a time:

- `tools/check`: passed.
- Browser suite: 54 passed, 22 skipped (gated suites and deployed-only specs, as before), 0 failed.
- Phase 8 acceptance: 9/9 chains. Runtime status: 9 passed.
- Smokes: relative dates, cross-language Task matching, Codex actions, activity trace, Human Review: all passed.
- The calendar AI smoke and the benchmark first failed only on their exact event counts, because the verification stack had imported the owner's real ZAW feed from `.env` (every AI scenario itself passed). `tools/verify-compose` now blanks `LIFE_OS_ZAW_ICS_URL`, and both passed on the re-run: the calendar AI smoke, and the benchmark 20/20.

## Defects found during Phase 10

- **The Tasks list API never paged** (found in the polish pass) and the Calendar list ignored `cursor`; both page now.
- **Reschedules planned for the wrong domain.** With calendar changes allowed, Codex sometimes planned "Move the electrician call…" as a Calendar update (2 of 3 runs). The processor now looks in the other domain when the planned one finds nothing, with the same exactly-one rule.
- **The verification stack imported the owner's feed** (above).
- **The Codex actions smoke was stale** since Phase 8 (Human Review) and BUG-12 (owner-timezone dates); fixed.

## Follow-up: P10-08 reminder defaults

The owner asked what "Default reminders" meant (BUG-28: the details showed that label instead of the reminders that apply) and asked for Google Calendar-style reminders: several at once, defaults and an event's own together. Since P10-08 every event follows its calendar's default reminders and adds its own on top; Life OS events have owner defaults for timed and all-day events (`/v1/settings`), imported events their source's, and the Dashboard's Reminder defaults dialog edits both. Migration 000007 keeps every existing event's reminders.

- Calendar unit and integration tests, including the migration up and down: passed.
- `tools/check`: passed. Browser suite: 56 passed, 22 skipped, 0 failed, with two new specs: several own reminders on top of a default, and Reminder defaults for the owner and a source.
- `make calendar-ai-smoke` has a sixth step: with a 30-minute default, "remind me a day before and 3 hours before" gives the new event both reminders of its own plus the default. Its first run found BUG-31: Codex planned the haircut without an end, Calendar refused it, and the capture retried. A new event without an end now lasts one hour (all-day: one day), and the smoke passes all six steps. Benchmark after the prompt changes: 20/20.

## Known limitations accepted for Phase 10

- Phase 10 stores reminder intent only; nothing sends notifications yet. A notifier will trigger every effective reminder.
- Manual recurring events have no per-occurrence exceptions. ICS `RANGE=THISANDFUTURE` is treated as a single occurrence, and VALARMs in feeds are not imported (source defaults apply).
- A manual "Refresh now" is attributed to the feed (`system:ics-<slug>`); who asked is in `data.requestedBy`.
- Human Review stores the chosen Calendar event in Inbox's `taskId` field; a neutral `candidateId` would be a later cleanup.
