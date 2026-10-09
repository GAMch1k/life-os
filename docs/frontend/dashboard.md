# Dashboard

The Dashboard is a Next.js App Router/TypeScript application using Tailwind and shadcn/ui. It provides the overview and routed Finance, Tasks, Inbox, and Calendar pages. It consumes generated TypeScript OpenAPI types under `src/lib/api/generated`, not hand-written transport copies.

Home (`/`) is attention-first: it answers “what needs my attention now?” and links into the dedicated workspaces for everything else. It is a read-only projection over the owning services' list APIs (`src/lib/home-attention.ts`) and never mutates data or adds business logic. It shows four compact cards:

- **Human Review** — how many captures wait in `human_review`, with up to two previews, linking to `/inbox`.
- **Urgent tasks** — open Tasks that are overdue or due today (UTC day boundaries, via the Tasks `dueBefore` filter with `includeUndated=false`), overdue first, previewing five, linking to `/tasks`.
- **Next on your calendar** — the next Calendar event in the coming 30 days, linking to `/calendar`.
- **Unscheduled tasks** — up to three open Tasks without a date, linking to `/tasks`.

Counts carry a `+` when the service reported more rows than Home fetched. Each card distinguishes an empty source (“Nothing waiting for review.”) from an unreachable one (“Inbox is unavailable right now.”); a stopped service never fails the whole page. Below the cards Home keeps the capture controls (text capture into Inbox and Voice recording). Finance, full Task lists, and the Calendar agenda live only in their workspaces. System/dependency problems and delayed retry work are not shown yet because no service exposes them; they belong to the runtime status and retry tickets.

`/tasks` is the Tasks control workspace. Its List view groups open work into Overdue, Today, Upcoming, and Unscheduled / No date; an undated Task remains visible rather than being assigned a made-up deadline. The compact date navigator controls its date focus, while Schedule is a day/week projection of dated Tasks only. This projection does not turn Tasks into Calendar Events; unscheduled work remains in List. A Task can be edited in place, rescheduled, returned to Unscheduled, completed, and reopened using the Tasks API; all mutations survive reload.

Browser requests use `/api/finance`, `/api/tasks`, `/api/inbox`, `/api/calendar`, and `/api/voice` relative to the current origin. Server-side rendering uses Docker service DNS internally. Loading/error handling is page-scoped so an unavailable service does not require exposing direct service ports.

The Home Voice capture control uses the browser `MediaRecorder` API. Stopping a recording uploads it to the asynchronous Voice job API and keeps the returned job ID in component state; it never waits for Whisper. “Accepted” means the audio has been durably queued, not that it was transcribed. The generated Voice client polls the persisted job and shows queued, processing, automatic-retry, succeeded, failed, or expired status; polling stops on terminal states and component cleanup clears the timer. Failed/expired uploads offer a re-record or re-send flow. A successful job means transcription completed and has been handed to Inbox; the Inbox widget remains the place to see later AI processing. Run `make dashboard-check` for format/lint/test/type/build and `make life-smoke` for browser-facing behavior.
