# Inbox service

Inbox is the capture queue in `inbox_db`. It stores user content, source (`manual`, `web`, `voice`, `watch`, or `email`), metadata/media references, and a lifecycle: `raw` → `processing` → `resolved` or `discarded`, with `human_review` for decisions the user must make and `failed` when automatic processing gave up. It intentionally does not decide whether a capture becomes a task, event, or transaction. Processing attempts are kept separately with a per-item idempotency key, agent/model identity, attempted actions, timestamps, and retryable/failed/processed outcome; raw content is never consumed or overwritten.

An ambiguous or low-confidence Task mutation becomes `human_review`, rather than an automatic retry. The Inbox item retains the original capture, proposed action, reason, and candidate Tasks in `processingMetadata`; it is excluded from the processor's raw-item poll. The Dashboard Human Review queue calls `POST /v1/inbox-items/{itemId}/human-review/resolve` with one retained candidate ID. Inbox verifies that selection, stores an approved action, returns the item to `raw`, and the processor resumes it through MCP and the owning Tasks API. The item resolves only after that mutation succeeds. `retryable` remains reserved for temporary technical failures; `failed` means recovery is exhausted or operator intervention is needed. `make human-review-smoke` proves the automatic ambiguous-capture → Human Review → selected Task → persisted completion → resolved Inbox chain in disposable Compose resources.

`GET /v1/backlog` summarizes unfinished work for status surfaces, from durable Inbox state only:
- pending captures (`raw` or `processing`);
- captures that already failed at least once and are being retried, with the latest failure text and time;
- the Human Review count;
- the creation time of the oldest pending capture.

The Dashboard Status page uses it to show retries and prolonged backlogs.

Inbox records `inbox.*` activity events for capture, processing attempts (started, completed, retry scheduled, failed), Human Review (requested, resolved), and resolution or discard. They are recorded under the item's workflow correlation. A caller such as the Voice worker may supply `X-Correlation-ID`, and Inbox stores it in `inbox_items.correlation_id` (migration `000006`). Otherwise the item id is the correlation. Either way it is returned as `InboxItem.correlationId`. See [Activity](activity.md).

The current dashboard captures and resolves/discards items. The automatic MCP/Codex processor reads raw items only through MCP, preserves an audit trail, handles ambiguity safely, and calls core APIs through MCP tools. Its processing lifecycle is `raw → processing → resolved`. The outcome of an attempt decides what happens next:

- **`retryable`** (a temporary technical failure) returns the item to `raw` with a durable retry schedule. `inbox_items.next_retry_at` says when it may be retried (immediately when absent), and `retry_reason` says why, for example `usage_limited` or `backoff`. The attempt keeps its own `next_retry_at`.
- **`failed`** (automatic recovery is unlikely) moves the item to `failed`. It is no longer picked up automatically but stays visible as unresolved.
- **`human_review`** (a decision for the user) never enters technical retry.

`GET /v1/inbox-items?state=raw&due=true` lists only items with no schedule or whose time has come. It is the processor's only work source, so a schedule survives restarts of both the processor and Inbox. Any other state change clears the schedule. The backlog reports the next scheduled retry with its reason, and the number of failed items. Each retry creates a distinct audit attempt key (`<operation>.retry.<n>`), preserving the original attempt and avoiding the former uniqueness collision; a processed attempt for the same deterministic operation prevents later duplicates. Invalid requests return 400 and missing items 404; readiness is at `/ready`. Test with `make db-test SERVICE=inbox` and `make e2e-smoke`.
