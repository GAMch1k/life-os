# Activity service

Activity answers “what happened, who did it, and what led to it?” across Life OS. It stores durable, structured, user-facing events in its own `activity_db`. A spoken request can then be followed from the Voice upload, through transcription, Inbox processing, the agent's MCP action, and the Task it created, to the Task's later completion.

It is product/audit data, not a log sink. Services keep their technical `slog` logs. Activity holds only a small set of meaningful operation outcomes, recorded by the service that owns each operation.

## Why a dedicated service

The interesting workflows cross services: Voice → Inbox → processor → MCP → Tasks. Each service owns its database, so no one service can read the others' history. A single append-only store lets the Dashboard query one API for a whole trace. Services send it events over the private network using a small infrastructure client (`pkg/activity`). There is no message bus: each event is one HTTP call to one consumer, and nothing else subscribes.

```mermaid
flowchart LR
  Voice -- record --> Activity[(Activity / activity_db)]
  Inbox -- record --> Activity
  MCP -- record --> Activity
  Tasks -- record --> Activity
  Dashboard -- "GET /api/activity" --> Gateway --> Activity
```

## Event model

| Field | Meaning |
| --- | --- |
| `id` | Event UUID. Chosen by the emitter; often deterministic (see below). |
| `type` | Dotted name, e.g. `task.completed`. |
| `source` | Emitting service: `voice`, `inbox`, `mcp`, `tasks`. |
| `subject` | The entity the event is about: `{type, id}`, e.g. `{task, <uuid>}`. |
| `correlationId` | The workflow this event belongs to. |
| `causationId` | The event that directly caused this one (optional). |
| `actor` | `user`, `agent` (e.g. `codex-inbox-processor`, `mcp`), `system` (e.g. `voice-worker`), or `dev` (e.g. `claude-code`). See [Actors](#actors). |
| `outcome` | `succeeded`, `pending` (accepted/scheduled), or `failed`. |
| `summary` | One short human-readable sentence. |
| `data` | Small structured details (titles, tools, errors; ≤16 KB). |
| `occurredAt` / `recordedAt` | When it happened / when Activity stored it. |

Events are append-only. There is no update or delete API. The history starts when the Activity service was deployed; operations before that were not backfilled.

### Actors

Every write names its actor in `X-Life-OS-Actor`; nothing is attributed to the owner by default (see [The actor gate](#the-actor-gate)).

- `user`: the owner, for example a Dashboard edit. The Dashboard sends `X-Life-OS-Actor: user` on every service call. The Event Log shows it as "You".
- `agent:codex-inbox-processor`: the automatic Inbox processor. Every call it makes carries this actor: processing attempts, Human Review requests, MCP changes, runtime records, and the crash-recovery reconciliation that schedules a `recovered` retry. Older `recovered` retry events, recorded before reconciliation carried this actor, show `agent:mcp`.
- `agent:mcp`: an MCP caller that sent no actor header (or claimed to be `user`), for example an external MCP client. MCP treats every caller as an agent.
- `system:voice-worker`: Voice transcription.
- `dev:<name>`: a development tool or agent working on Life OS, never the owner or the Life OS AI. Claude Code and its subagents use `dev:claude-code`, Playwright specs `dev:e2e`, and `tools/` scripts `dev:<script>`. The Event Log shows "Developer tool (<name>)" with an outline badge. Events recorded before this kind existed (for example the smoke-test cleanup of 2026-10-09) keep their original `user` actor; events are never rewritten.

Names are 1–64 letters, digits, `.`, `_`, or `-`. An event emitted without any actor is attributed to the emitting service (`system:<source>`), never to the owner.

### The actor gate

Finance, Tasks, Inbox, Calendar, Voice, and Notifications reject every write (`POST`, `PUT`, `PATCH`, `DELETE`) without a valid `X-Life-OS-Actor` with `400` and error code `actor_required` (`pkg/activity.RequireActor`). Reads, `/health`, and `/ready` do not need it. Internal callers always send one: the processor and MCP as agents, the Voice worker as `system:voice-worker`, and traces forward `user` explicitly.

Two endpoints are deliberately not gated:

- MCP (`/api/mcp`) is itself the agent. It records an unnamed caller as `agent:mcp` and sends that actor on every downstream call, so the owning services' gate always passes.
- Activity's `POST /v1/activity-events` is internal (the gateway denies it) and carries the actor as a required, validated field of the event body. The emitting service is the messenger, not the actor, so a header would add nothing.

The public gateway passes only `user` and `dev:<name>`: a claim to be the Life OS AI (`agent:`) or a component (`system:`) is dropped, so such a write fails the gate. See [Networking](../architecture/networking.md).

### Recorded events

| Type | Source | Subject | Notes |
| --- | --- | --- | --- |
| `voice.job.accepted` | voice | `voice_job` | Upload accepted; the job id becomes the workflow correlation. |
| `voice.job.transcribed` | voice | `voice_job` | Includes a transcript preview and the Inbox item id. |
| `voice.job.retry_scheduled` / `voice.job.failed` | voice | `voice_job` | Transcription or Inbox hand-off failed. |
| `inbox.item.captured` | inbox | `inbox_item` | Any capture: Dashboard text, Voice, etc. |
| `inbox.processing.started` | inbox | `processing_attempt` | The processor started an attempt. |
| `inbox.processing.completed` / `.retry_scheduled` / `.failed` | inbox | `processing_attempt` | Attempt outcome recorded by the processor. |
| `inbox.human_review.requested` / `.resolved` | inbox | `inbox_item` | Ambiguous work waiting for, then given, a user decision. |
| `inbox.item.resolved` / `.discarded` | inbox | `inbox_item` | Final Inbox state. |
| `inbox.notification.failed` | inbox | `inbox_item` | The owner could not be notified about a Human Review or a failed capture (Notifications unreachable or refusing), by `system:inbox`, caused by the Inbox event it was about; `data` holds `category`, `dedupeKey`, and `error`. See [Inbox](inbox.md#notifications). |
| `mcp.action.requested` / `.completed` / `.failed` | mcp | `mcp_action` | Every mutation tool call (Tasks, Calendar, Finance). Read and search tools are not recorded. The Dashboard shows this domain as "Agent changes". |
| `task.created` / `.updated` / `.completed` / `.reopened` | tasks | `task` | Every Task mutation, from the Dashboard or an agent. |
| `calendar.event.created` / `.updated` / `.rescheduled` / `.cancelled` / `.restored` / `.deleted` | calendar | `calendar_event` | One per event change through the Calendar API, from the Dashboard (`user`), an agent, or a tool, with the request's correlation and causation; an update that changes nothing records nothing. `data` holds the title, times, status, source, and for updates the changed fields. See [Calendar](calendar.md#activity). |
| `calendar.source.refreshed` / `.refresh_failed` | calendar | `calendar_source` | One per refresh of a subscribed feed (scheduled or "Refresh now"), also when nothing changed, by the source as `system:ics-<name>` (for example `system:ics-zaw`); `data` holds `sourceName`, `outcome`, the counts (inserted, updated, removed, restored) or the error, and `requestedBy` for a manual refresh. Never the feed URL, and no events per imported event. |
| `notification.created` | notifications | `notification` | A notification was sent through the one path, by its producer (for example `system:calendar`); `data` holds `importance`, `category`, `source`, `link`, and the channels; the title is in the summary, never the body. A repeat with a known dedupe key records nothing. See [Notifications](notifications.md#activity). |
| `notification.delivered` / `.delivery_failed` | notifications | `notification` | One per channel, by `system:notifications`, in the producer's workflow (caused by `notification.created`); `data` holds `channel`, `attempts`, and for a failure the `error`. Retries record nothing. |

Finance changes are not recorded by Finance yet; its agent-driven changes still appear as `mcp.action.*` events.

## Correlation, causation, and subject

Three links reconstruct a workflow:

- **Correlation** groups one workflow. A Voice job's id is the correlation for everything it leads to. A Dashboard text capture starts its own workflow, so the Inbox item id is the correlation. A direct Dashboard Task edit uses the request's correlation id.
- **Causation** names the direct cause. Upload → transcription → capture → processing started → MCP request → `task.created`. Processing started → processing completed → item resolved.
- **Subject** links the same entity across workflows. A Task created by the agent and later completed in the Dashboard has two events with subject `task/<id>` but different correlations.

These travel as HTTP headers between services:

| Header | Carries |
| --- | --- |
| `X-Correlation-ID` | Workflow id (already propagated by the shared HTTP runtime). |
| `X-Causation-ID` | The id of the event that caused the request. |
| `X-Life-OS-Actor` | `user`, `agent:<name>`, `system:<name>`, or `dev:<name>`. Required on writes. |

The gateway clears `X-Correlation-ID` and `X-Causation-ID` on public traffic, so only services link events to workflows.

Inbox stores an explicit upstream correlation id on the item (`inbox_items.correlation_id`, NULL for self-rooted items). It exposes the effective value as `InboxItem.correlationId`, so the processor can continue the workflow for every MCP call it makes.

### Deterministic event ids

Where an occurrence happens once, its id is a UUIDv5 of its type and identifiers, for example `EventID("task.created", taskId)` or `EventID("inbox.processing.started", attemptId)`. This gives two properties:

- **Idempotent emission.** Recording the same id again returns the stored event (HTTP 200) instead of adding a duplicate, so an idempotent Task-create replay records nothing new.
- **Causation without lookups.** A service can name an upstream event it did not record. For example, the processor names `inbox.processing.started` as the cause of its MCP calls.

Repeatable occurrences (Task updates, MCP calls, review resolutions) get random ids.

## Delivery guarantees

Recording is best effort and never fails the domain operation. The emitter retries briefly (three tries within about half a second), then logs a warning. If Activity is down for longer, those events are missing from the trace; the domain data itself is unaffected. A service with an empty `ACTIVITY_SERVICE_URL` records nothing. Activity events are therefore evidence of what happened, but they are not the system of record for domain state.

## API

The gateway serves `/api/activity/` read-only (non-GET requests return 403). Services record events on the private network at `http://activity:8080`.

- `GET /v1/activity-events`: newest first. Filters: `correlationId`, `subjectType` + `subjectId`, `type`, `source`, `outcome`, `from` (inclusive) / `to` (exclusive) on `occurredAt`, plus `cursor` and `limit` (1–100, default 50). The opaque `nextCursor` is a keyset position (`occurredAt`, `id`).
- `GET /v1/activity-events/{eventId}`
- `POST /v1/activity-events`: internal. `201` when recorded, `200` with the stored event when the id already exists.

Reconstructing the example trace:

```sh
# Everything that happened because of one Voice recording, oldest first
curl "$GATEWAY/api/activity/v1/activity-events?correlationId=$JOB_ID&limit=100" | jq '.items | reverse'
# Everything that ever happened to the resulting Task
curl "$GATEWAY/api/activity/v1/activity-events?subjectType=task&subjectId=$TASK_ID"
```

The contract is `services/activity/api/openapi.yaml`. The Dashboard Event Log (`/activity`, see [Dashboard](../frontend/dashboard.md)) is built only on this API: it lists and filters events, then rebuilds a selected entry's trace from its correlation, causation, and subject.

## Operations

- Database: `activity_db` / `activity_user`, password `LIFE_OS_ACTIVITY_DB_PASSWORD` in `.env`. Fresh volumes are provisioned automatically. For an existing deployment, add the password and run `make db-provision` once before starting the new `activity` and `activity-migrate` services.
- Emitting services (`tasks`, `inbox`, `voice`, `voice-worker`, `mcp`) read `ACTIVITY_SERVICE_URL`.
- Retention: events are kept indefinitely for now; they are small and single-user.
- Verification: `make activity-trace-smoke` speaks a Task request (espeak-ng in a throwaway container). It sends the request through Voice, whisper.cpp, Inbox, the automatic processor with the authenticated Codex CLI, MCP, and Tasks on the disposable verification stack, then completes the Task itself (recorded as `dev:smoke-activity-trace`). It reconstructs and checks the whole chain from the Activity API alone.
