# Phase 8 verification

Phase 8 turned the stable runtime into a usable control surface: Tasks that never disappear, an attention-first Home, Human Review, an Event Log, a Status page, durable retries, and a hardened Codex runtime. As in [Phase 7](phase-7.md), unit and integration tests with fake planners stay useful, but they are not proof of the real workflow. Each ticket's acceptance used the normal user triggers on the disposable `lifeos-verify-*` stack, with real whisper.cpp and the authenticated Codex CLI (`gpt-5.6-luna`).

## Commands

| Command | What it proves |
| --- | --- |
| `make phase8-acceptance` | The ten Phase 8 sign-off chains below, in one serial browser run. |
| `make browser-e2e` | The per-feature browser specs: Home, Tasks workspace, Human Review, Event Log, text and voice capture. |
| `make activity-trace-smoke` | A spoken request reconstructed end to end from the Activity API alone. |
| `make runtime-status-smoke` | Status and the Home alert explain forced problems: Whisper, processor, MCP, Codex sign-in, Tasks, and the Voice worker. |
| `make retry-schedule-smoke` | Ambiguity goes to Human Review; a usage limit waits for the reset across restarts; retries back off 30 s, then 60 s. |
| `make codex-runtime-smoke` | The Codex runtime lifecycle: environment, cancellation, timeout, and SIGKILL crashes before and after the session id. |

Two situations cannot be produced on demand with the real provider: a usage limit, and a Codex run that has started but has not yet reported its session. For those, `tools/fixtures/codex-limit-simulator` stands in for the CLI at its command-line boundary, and only while a smoke asks it to. Every other call goes to the real Codex CLI.

## Sign-off chains

`make phase8-acceptance` (`apps/dashboard/e2e/phase8-acceptance.spec.ts`), passed on 2026-10-09. All ten chains passed in 5.6 minutes:

1. **Undated Task stays visible.** A Home capture "Create a task titled …" creates a Task with no due date, and `/tasks` lists it under Unscheduled / No date.
2. **Browser edit.** The Task is renamed in the Tasks workspace, and the change persists across a reload.
3. **Inbox reschedule without a duplicate.** "Reschedule my existing task "…" to 2026-10-20." updates the same Task's due date. There is still exactly one such Task.
4. **Voice completes the matching Task.** A recorded "I already bought the chicken." goes through the Voice UI and whisper.cpp to an Inbox item. The processor and Codex resolve it to `task_complete` with `chicken`, and the Task "Buy chicken for dinner" is completed. No other Task is created.
5. **Ambiguous mutation waits.** "I already called Anna, mark that task as done." with two Anna Tasks goes to Human Review with both candidates. Neither Task changes until the user chooses one in `/inbox`; only the chosen one is completed.
6. **Home is attention-first.** While the review waits, Home's Human Review card shows "1 capture waiting for your decision" next to the other attention cards.
7. **Event Log trace.** The chain-1 capture opens in `/activity` and leads to processing, `task_create`, Task created, and Inbox item resolved. The Task's history shows the user's edit and the agent's reschedule in other workflows.
8. **Durable retry on a provider failure.** With a usage limit reported (simulated), the capture is scheduled just after the reported reset, with reason `usage_limited`. Status shows "Codex has reached its usage limit" and the next retry, and Home shows an alert. The capture is processed after the reset into exactly one Task.
9. **No duplicate after a crash.** The processor is SIGKILLed after Codex reported its session. The next processor reconciles the abandoned attempt (`recovered`), and the capture ends with exactly one Task.
10. **UI/agent parity.** The user's edit and the agent's reschedule of the same Task are both the Tasks service's own `task.updated`, with actor user vs `agent:codex-inbox-processor`. Completing a Task in the Tasks workspace and completing one from a voice note are both its `task.completed`.

## Defects found during sign-off

- **Done statements were never acted on.** "I already bought the chicken" went to Human Review. The planner prompt asked for a "precise" Task identity, so Codex (`gpt-5.6-luna` and `gpt-5.6-terra`) answered "ambiguous" with no action. Identity is the processor's job: `task_search`, exactly one match, otherwise Human Review. The prompt now says so, and says a statement that something was done is `task_complete`.
- **Paraphrased search terms found nothing.** Codex sometimes proposed a paraphrase such as "called Anna" as `taskQuery`. The Tasks search is a substring match, so that found nothing, and Human Review opened with no candidates. The prompt now explains the match and asks for the shortest distinctive word from the title.

Both fixes were checked against real Codex. A quotation, a bare noun phrase, a vague request, and a destructive instruction still produce no action.

## Known limitations accepted for Phase 8

- A Human Review item without candidates (Codex found nothing to do) cannot yet be dismissed from the queue. That is ticket P8-15.
- Codex sessions are not resumed after a crash. Planning is a single read-only turn and is re-run; domain actions are idempotent. See [MCP and AI](../services/mcp.md#codex-runtime).
- The Docker gateway resolves upstream addresses at startup, so it is recreated after services are recreated on deploy.
