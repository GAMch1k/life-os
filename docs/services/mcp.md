# MCP and AI

The Go MCP service is available behind `/api/mcp/mcp` using streamable HTTP, with `/api/mcp/health` and `/api/mcp/ready` probes. Its typed read tools call Finance monthly summary/transactions, Tasks list/search, Inbox list, and Calendar list through the services' HTTP APIs. It also provides narrowly scoped mutation tools used by Inbox processing: `task_create`, `task_update`, `task_complete`, `task_reopen`, `calendar_create`, `inbox_start_processing_attempt`, `inbox_record_processing_attempt`, and `inbox_resolve`. It never owns or queries Finance, Tasks, Inbox, or Calendar tables directly; upstream failures become understandable MCP tool errors.

Codex is told that a `taskQuery` is matched as a case-insensitive substring of Task titles and notes, so it proposes the shortest distinctive word from the title ("chicken", "Anna"). A statement that something was done, such as "I already bought the chicken", is a `task_complete` for the Task it refers to. Codex does not need to know the existing Tasks, because identity is resolved here. For an existing Task mutation, the processor sends the model's identifying `taskQuery` to `task_search` and proceeds only when the Tasks API returns exactly one result. Zero or multiple results leave the Inbox attempt recoverable without creating or mutating a Task. The resolved UUID is passed only to the owning Tasks API operation.

The non-interactive processor uses the existing local Codex CLI/ChatGPT login, not an API key. Compose starts it as the `processor` service during normal Life OS startup; it polls the raw Inbox at `LIFE_OS_PROCESSOR_POLL_MILLIS` (default one second), starts once immediately after boot, and exposes private `/health` and `/ready` on port 8081. `SIGTERM` stops polling and shuts that server down gracefully. The one-shot command remains a diagnostic only.

The deployed worker image contains `codex` but contains no authentication. Set `LIFE_OS_CODEX_HOME_HOST_PATH` to the host's existing authenticated Codex directory (normally `~/.codex`) and `LIFE_OS_CODEX_RUNTIME_UID` to its owner (`id -u`) in the ignored `.env`. At start, the service runs as that unprivileged UID, bind-mounts the directory read-only, and copies only `auth.json` plus optional `config.toml` into its own `/tmp` tmpfs. The source session, image, Git repository, and named Docker volumes never receive a copied credential. The worker runs with a read-only root filesystem, no Linux capabilities, and `no-new-privileges`. Check the same runtime context with `docker compose exec processor codex login status`; it must report the existing ChatGPT login before the worker can become operational.

`make processor-smoke` is the acceptance smoke for the actual deployed chain: gateway/Dashboard-style capture → persisted Inbox item → automatic processor polling → authenticated local Codex CLI → MCP `task_create` → Tasks persistence → processed audit record → Inbox resolution. It uses only the isolated `lifeos-verify-*` resources and never manually invokes the processor.

The processor reads raw items through `inbox_list`, asks Codex for a constrained JSON plan, accepts only high-confidence task/event creation actions and strictly validated manual finance transactions, records each attempt through MCP, and resolves an item only after every action succeeds. A finance action requires an explicit account ID, signed amount, currency, and date; calendar updates/deletions are never allowed. Ambiguous, low-confidence, and nothing-to-do plans go to Human Review and are never retried automatically. Technical failures are retried on a durable schedule (see [Retry scheduling](#retry-scheduling)). A retryable operation receives a distinct persisted attempt key while retaining its deterministic base operation key, so prior attempt history remains visible and the Inbox uniqueness constraint cannot block a subsequent retry. A processed base operation is skipped. `go run ./cmd/processor --dry-run` returns proposed MCP actions without creating attempts, mutating core services, or resolving Inbox items. `go run ./cmd/processor --content '...'` is a separate non-mutating diagnostic that prints only Codex's proposed JSON plan.

Each MCP call from the processor carries the Inbox item's `X-Correlation-ID`, the processing attempt's `inbox.processing.started` event as `X-Causation-ID`, and `X-Life-OS-Actor: agent:codex-inbox-processor`. MCP forwards these headers to the owning service. Every mutation tool records `mcp.action.requested` and then `mcp.action.completed` or `mcp.action.failed`, and the owning service's change names the request event as its cause. A caller that sends no actor header is recorded as `agent:mcp`. See [Activity](activity.md).

For every allowed mutation, the processor derives a stable per-action key from the Inbox operation, action index, tool, and canonical arguments. MCP forwards it to Tasks, Calendar, or Finance as `Idempotency-Key`. Those services persist the key and a payload hash with the created row: repeating the same key and payload returns the original row, while reusing a key with materially different content returns `409`. Manual callers may omit the header and retain the usual create behavior. Idempotency metadata is retained with the owned record for its lifetime; it is not in-memory state.

## Processor status

The processor's private health port (`8081`, not exposed through the gateway) serves `/health`, `/ready`, and `/status`. Like `/health` and `/ready`, `/status` is an operational endpoint outside the OpenAPI contracts. It holds what the running process has observed since it started, in memory, and resets on restart:

```json
{
  "model": "gpt-5.6-luna",
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

## Retry scheduling

The processor polls only due work (`inbox_list` with `state=raw&due=true`). It classifies each failure and asks Inbox to persist when the item may be retried (`nextRetryAt`, `retryReason`):

| Failure | Example | Next attempt |
| --- | --- | --- |
| Codex usage limit | `ERROR: You've hit your usage limit…` | At the reset time the Codex CLI reports for the exhausted bucket (`account/rateLimits/read`, read fresh and cached for a minute), plus up to a minute of jitter. Reason `usage_limited`. Without a reported reset, it backs off. |
| Other Codex failure | sign-in, transport, CLI error | Backoff. |
| Dependency unavailable | MCP or an owning service unreachable | Backoff. |
| Unusable model output | invalid JSON, a disallowed tool, an action the owning service rejects | Backoff. After 5 attempts the item becomes `failed` (operator attention). |
| Ambiguous, low confidence, or nothing to do | a quotation, an unclear request | Human Review. Never a technical retry. |

Backoff is 30 s, doubling per earlier attempt of the item, capped at one hour, with ±20% jitter. Reason `backoff`. A retry adds an audit attempt; it never repeats a processed operation. Retry-After headers and explicit recovery intervals are not exposed by the current dependencies, so the only explicit recovery time used today is the Codex usage reset. A nothing-to-do capture appears in the Human Review queue with Codex's reason and no candidates. Letting the user dismiss such an item from that queue is a separate follow-up; until then it stays there without being retried.

`make retry-schedule-smoke` proves the following on the verification stack, with real Inbox, MCP, processor, Codex, and Tasks:
- ambiguity goes to Human Review;
- no retry happens before a reported reset, even across a processor and Inbox restart;
- the work completes after the reset;
- backoff runs 30 s, then 60 s.

A usage limit cannot be triggered on demand, so `tools/fixtures/codex-limit-simulator` simulates one at the Codex CLI boundary for that smoke only.

## Codex runtime

Codex is the processor's agent runtime. It runs as a managed subprocess of the machine's existing ChatGPT sign-in, with no API key (`internal/processor/codex.go`). Every planning run gets:

- **Binary.** `LIFE_OS_CODEX_COMMAND` is resolved once at startup. A missing CLI stops the processor with exit code 78.
- **Working directory.** The explicit `LIFE_OS_CODEX_WORKDIR`, in a read-only sandbox (`-s read-only`).
- **Environment.** Only `PATH`, `HOME`, `USER`, `CODEX_HOME`, locale, `TZ`, `TMPDIR`, and CA settings. API keys, service URLs, and other secrets are never inherited.
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

