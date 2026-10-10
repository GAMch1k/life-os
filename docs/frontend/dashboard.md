# Dashboard

The Dashboard is a Next.js App Router/TypeScript application using Tailwind and shadcn/ui. It provides the overview and routed Finance, Tasks, Inbox, Calendar, Activity, and Status pages. It consumes generated TypeScript OpenAPI types under `src/lib/api/generated`, not hand-written transport copies.

Home (`/`) is capture-first: opening it lets the owner type or dictate immediately, without scrolling, on desktop and phone. The capture card comes first (`src/components/capture-card.tsx`): a chat-like composer where Enter captures and Shift+Enter adds a line (one Enter is exactly one capture; a second submit while one is in flight and Enter during IME composition are ignored), the Voice **Record** control beside the Capture button, and the captures of the current visit listed under it with their live Inbox state (Received, Processing, Retry at a time, Needs review, Failed, Processed), polled until they settle. The composer clears and keeps focus after each capture, and is focused on load on desktop (not on touch devices). A line under it counts automatic work still in Inbox, from Inbox `GET /v1/backlog`: pending captures (`raw` or `processing`, with retries and the next scheduled retry) and failed ones, each linking to its `/inbox` section. Human Review is counted once, in its own card. When something needs the user's attention (see `/status` below), one alert linking to `/status` follows the capture card; when everything works, it shows nothing. The rest of Home is a read-only projection over the owning services' list APIs (`src/lib/home-attention.ts`) in four compact cards:

- **Human Review** — how many captures wait in `human_review` (the backlog's true count), with up to two previews, linking to `/inbox`.
- **Urgent tasks** — open Tasks that are overdue or due today (UTC day boundaries, via the Tasks `dueBefore` filter with `includeUndated=false`), overdue first, previewing five, linking to `/tasks`.
- **Next on your calendar** — the next Calendar event in the coming 30 days, linking to `/calendar`.
- **Unscheduled tasks** — up to three open Tasks without a date, linking to `/tasks`.

Counts carry a `+` when the service reported more rows than Home fetched. Each card distinguishes an empty source (“Nothing waiting for review.”) from an unreachable one (“Inbox is unavailable right now.”); a stopped service never fails the whole page. Finance, full Task lists, and the Calendar agenda live only in their workspaces.

`/inbox` shows every unfinished capture exactly once, in the section for its state (`src/lib/inbox-summary.ts`). The header counts come from `GET /v1/backlog` and link to their sections; the same capture card sits below it.

- **Human Review** (`human_review`): each review offers its candidate Tasks and a Discard action. A capture with no candidates, where Codex found nothing to do, can only be discarded or captured again.
- **Pending** (`raw` and `processing`), newest first, each marked Received, Retrying, Retry at a time (the reason, such as the Codex usage limit, is in the tooltip and the section note), or Processing.
- **Failed**: automatic processing gave up. Shows the latest attempt error; the capture can be discarded.
- **Recently processed**: up to ten recently resolved captures, latest resolution first, each linking to its workflow in the Event Log (`/activity?correlationId=…`).

Sections list up to 50 rows and say “Showing 50 of N” when more exist; empty sections are hidden, and an unreadable one says so instead of looking empty. Times use the owner's timezone (`LIFE_OS_TIMEZONE`).

`/tasks` is the Tasks control workspace. Its List view groups open work into Overdue, Today, Upcoming, and Unscheduled / No date; an undated Task remains visible rather than being assigned a made-up deadline. The compact date navigator controls its date focus, while Schedule is a day/week projection of dated Tasks only. This projection does not turn Tasks into Calendar Events; unscheduled work remains in List. A Task can be edited in place, rescheduled, returned to Unscheduled, completed, and reopened using the Tasks API; all mutations survive reload.

`/activity` is the Event Log, a read-only view over the Activity API (`src/lib/activity-log.ts`; see [Activity](../services/activity.md)). It lists events newest first with human-readable labels, outcome, time (UTC), domain, and actor. Filters are plain URL parameters set from a form, so any filtered view can be bookmarked or shared: domain (`source`), entity (`subjectType`), event type, status (`outcome`), and a UTC date range (`fromDate`/`toDate`, both inclusive). "Older events" pages with the API's cursor. Opening an entry (`?event=<id>`) loads its whole workflow (same `correlationId`) and its subject's history, then shows:

- **What led here**: the causation chain back to the start of the workflow.
- **What it led to**: everything caused by it, directly or indirectly.
- **Same <entity> in other workflows**: for example, the user completing a Task that the agent created earlier.
- **Whole workflow**: the causation tree, with the selected entry highlighted.
- **Diagnostics**: a collapsed section with ids, correlation, causation, actor, timestamps, and the raw event data.

Every entry in these sections opens its own trace, so a chain such as Inbox capture → processing → MCP `task_create` → Task created → Task later completed can be followed click by click. A workflow and a subject history are each read as up to 500 events. If an event's cause was recorded in another workflow, the Event Log links to it instead of guessing. The Event Log never writes; like the gateway, its `/api/activity` proxy allows only GET/HEAD.

`/status` answers "is Life OS working, and if not, what do I do?" It is built only from supported runtime state (`src/lib/runtime-status.ts`), never from UI behaviour. Four checks are shown, each as OK, Needs attention, or Problem, with the runtime's own error text and a suggested action:

- **Automatic processing**: the processor's private `/status` endpoint (see [MCP and AI](../services/mcp.md)). It reports when the processor is not running, when it cannot read Inbox through MCP, when Codex is signed out (from `codex login status` or a 401), when Codex has hit its usage limit (Codex's own "try again at …" text is shown verbatim, plus the reset time Codex reports for the exhausted bucket), and when Codex runs are failing for any other reason. It also shows the Codex run in progress, counts of completed, failed, timed-out, cancelled, and crash-recovered runs, the Codex plan, and each usage bucket's used percentage and reset time, exactly as the Codex CLI reports them, or "unavailable".
- **Inbox backlog**: Inbox `GET /v1/backlog`. Shows captures that failed and are being retried, with the latest error and the next scheduled retry and its reason, for example "after the Codex usage limit resets". It reports captures that automatic processing gave up on (`failed`) as a problem, and flags a capture that has waited more than 15 minutes.
- **Voice transcription**: Voice `GET /v1/backlog`. Reports Whisper not ready, a stalled Voice worker, and recordings waiting for a retry, with the next scheduled retry time.
- **Services**: every owning service's `/ready`.

Each probe has a 2.5 s timeout. An unreachable source shows as a problem rather than failing the page. The Dashboard reaches the processor at `PROCESSOR_STATUS_URL` (`http://processor:8081` in Compose) on the private network; the processor is never exposed through the gateway. `make runtime-status-smoke` stops Whisper, the processor, MCP, Tasks, and the Voice worker, and signs Codex out inside the processor container, one at a time on the disposable verification stack. It then checks in the browser that Status and the Home alert explain each problem and that Status recovers.

Browser requests use `/api/finance`, `/api/tasks`, `/api/inbox`, `/api/calendar`, `/api/voice`, and `/api/activity` relative to the current origin. Server-side rendering uses Docker service DNS internally. Loading/error handling is page-scoped so an unavailable service does not require exposing direct service ports.

The Voice control in the capture card uses the browser `MediaRecorder` API. Stopping a recording uploads it to the asynchronous Voice job API and keeps the returned job ID in component state; it never waits for Whisper. “Accepted” means the audio has been durably queued, not that it was transcribed. The generated Voice client polls the persisted job and shows queued, processing, automatic-retry, succeeded, failed, or expired status; polling stops on terminal states and component cleanup clears the timer. Failed/expired uploads offer a re-record or re-send flow. A successful job means transcription completed and has been handed to Inbox; `/inbox` is the place to see later AI processing. Run `make dashboard-check` for format/lint/test/type/build and `make life-smoke` for browser-facing behavior.
