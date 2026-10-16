# Phase 17 verification

Phase 17 shows how the shared server is doing. The read-only [host-health service](../services/host-health.md) samples memory, load, disks, uptime and every container on the host once a minute (P17-01), and how much of the server Life OS itself uses (P17-03). It reports findings against thresholds. The Dashboard turns them into the Server check on Status, and the [monitor](../services/monitor.md) sends a notification when the check changes state.

This sign-off on 16 Oct 2026 (P17-02) uses read-only evidence:

- **Live data:** host-health's facts as the Status page gets them, compared with `free`, `df`, `uptime` and `docker ps` taken at the same moment.
- **History:** the Notifications history, the Event Log, and `docker inspect` and `docker logs`.
- **Tests:** the existing tests and their last recorded runs.

No test was run for this report: the server is small and shared, and another job was using the verify stack.

host-health has no gateway route. Its facts were read through the Dashboard's `GET /api/status`, which returns the Status page's checks as JSON. The Server check there is host-health's latest sample, at most a minute old; the newest point of its memory chart gives the sample's time.

## Commands

These are the existing tests. None was re-run for this report.

| Command | What it proves |
| --- | --- |
| `cd services/host-health && GOWORK=off go test ./...` | **Low memory** (`TestEvaluateFindsLowMemory`): two low samples are not enough, three below 15% are a warning, below 7% is a problem at once, and above 15% is nothing. **Restart loops** (`restart_test.go`): compose down and up, a force-recreate, and a user's stop, start and restart are no loop; a restart-policy loop exiting 1 three times in 15 minutes is one, an OOM kill counts, and an ignored project (the test stack) never is. Also covered: disks, load, unhealthy containers, the API with low memory, the gateway refusing everything but its three GETs, and Life OS's use from cgroups. |
| `npm --prefix apps/dashboard run test` (in `tools/check`) | `src/lib/runtime-status-server.test.ts`: "low memory names the biggest users and says what to do", "disks, load, and containers each say what is wrong and what to do", and "several findings: the first (the worst) leads". It also covers the facts, the charts, and a stopped host-health as one alert. |
| `cd services/monitor && GOWORK=off go test ./...` | A warning is notified as medium and a problem as high, each once, after two confirmations; a recovery is low. A restart neither repeats nor loses an episode. |
| `make browser-e2e` → `e2e/runtime-status.spec.ts` › Server on Status | "the Server card shows the made-up server, with a day of memory and Life OS's use". |
| `make runtime-status-smoke` → `e2e/runtime-status.spec.ts` › Runtime status under forced problems | "low memory on the server is a problem naming the biggest users" (the verify stack's host-health restarted with the `low-memory` fixture), and "a stopped host-health is one alert, under Services". |

Last recorded runs:

- **P17-01 (11 Oct):** `tools/check` passed. The browser suite first failed: `/status` answered 500, fixed in `9cc6deb`. The rerun was 101 passed / 0 failed.
- **BUG-35 (11 Oct):** browser suite 108 / 0, and "all smokes" passed.
- **P12-10 (12 Oct, after P17-03):** browser suite 117 / 0.

## Live facts against the host

The sample was taken at 20:29:15.9 CEST (18:29:15.9 UTC). The host commands ran 1.1 s later, at 20:29:17.1 CEST (18:29:17.1 UTC), right after a verify-stack run had stopped.

| Field | host-health (Status › Server) | Host | Match |
| --- | --- | --- | --- |
| Memory total | 7.7 GiB (8,277,594,112 bytes, the chart's scale) | `free -b`: 8,277,594,112 bytes; `free -h`: 7.7Gi | yes |
| Memory available | 4.2 GiB (4,508,991,488 bytes), 55% | `free -b`: 4,521,443,328 bytes; `free -h`: 4.2Gi | yes (12 MB apart, 1 s later) |
| Load (1, 5, 15 min) | 1.47, 3.40, 2.75 | `uptime`: 1.35, 3.34, 2.73 | yes (load falling, 1 s later) |
| CPUs | 6 | `nproc`: 6 | yes |
| Disk `/` free | 98.6 GiB of 231 GiB | `df -B1 /`: 105,907,789,824 of 248,505,155,584 bytes (98.6 of 231.4 GiB); `df -h /`: 99G of 232G | yes (`df -h` rounds up) |
| Containers running | 32 | `docker ps -a`: 32 running | yes |
| Containers in all | 45 | 45 | yes |
| Of the test stack | 1 | 1 `lifeos-verify` container | yes |
| Unhealthy | 0 | 0 `(unhealthy)` | yes |
| Restarting | 0 | 0 in state `restarting` | yes |
| Uptime | 7 days | `uptime`: up 7 days, 28 min | yes |
| Life OS use, most memory | Life OS: CPU 0.07 cores (1%) · RAM 574 MiB (7%); postgres 214 MiB, dashboard 110 MiB, … | not compared: it needs `docker stats`, which is outside the read-only commands of this report | — |

An earlier pair, during a verify-stack run, shows that the card is a sample and not a live reading. The sample was from 20:23:15.7 CEST (18:23:15.7 UTC); the host commands ran 34 s later, while the load was climbing.

| Field | host-health | Host | Match |
| --- | --- | --- | --- |
| Memory available | 2.1 GiB (27%) | 1.6Gi | no: 34 s later, memory dropping |
| Load | 9.73, 5.25, 2.58 | 11.50, 6.17, 2.98 | no: 34 s later, load climbing |
| Containers | 54 running, 78 in all, 34 of the test stack | 54 running, 78 in all, 34 `lifeos-verify` | yes |

P17-01's own live check on 11 Oct matched the same way: 3.9 GiB available on both, and 122 GiB free on both.

## Warnings

### Thresholds

These are host-health's defaults (`LIFE_OS_HOST_HEALTH_*`). The card's warning line at 1,241,639,117 bytes, 15% of 7.7 GiB, confirms the memory threshold live. The others are not exposed through the gateway, and `.env` was not read.

| Finding | Warning | Problem | Seen live |
| --- | --- | --- | --- |
| `memory_low` | available below 15% (1.2 GiB here) for 3 samples in a row | below 7% (about 550 MiB) | never |
| `disk_low` | less than 10 GiB or 10% free | less than 3 GiB | never (98.6 GiB free) |
| `load_high` | 15-minute load above 6 (the CPUs) | | never |
| `container_restarting` | restarted by Docker 3 times within 15 min | | **yes**: the forgejo runner, 11–12 Oct |
| `container_unhealthy` | unhealthy for 5 min | | never notified |
| `containers_unavailable` | the docker gateway cannot be read | | never notified |

"Never" means that no `status.server` notification for it exists since host-health went live (11 Oct, about 19:20 CEST). The monitor notifies a check's state, not each finding, so a second warning that came while the runner's warning held (11 Oct 19:21 to 12 Oct 15:31 CEST) would only have shown on the card. The card's 24-hour memory chart (15 Oct 20:27 to 16 Oct 20:23 CEST) has its lowest point at 2.1 GiB (27%), during a verify-stack run on 16 Oct at 20:23 CEST. `free -h` read 1.6 GiB (21%) 34 s later. Both are above the warning line. The 1-minute load reached 11.5 during that run, but the finding uses the 15-minute load, which was 2.98.

### The restart-loop warning, live

The owner's forgejo `runner` container was restart-looping ("fail to invoke Declare: 405 Method Not Allowed", BUG-35). That container no longer exists on the host. All four notifications came from `system:monitor`, were delivered by ntfy at the first attempt, and each has its `notification.created` and `notification.delivered` in the Event Log.

| When | Notification | Importance | Body |
| --- | --- | --- | --- |
| 11 Oct 19:22:49 CEST (17:22:49 UTC) | `2c0564b0-f203-47d5-9072-71f7442fee43` "Server needs attention" | medium | "runner (forgejo) keeps restarting: 3 times in 15 min." |
| 11 Oct 20:40:49 CEST (18:40:49 UTC) | `af00c3b1-e04a-491c-b138-ae02bb52ea52` "Server recovered" | low | "Running normally: 4.5 GiB of memory available …" (false, see below) |
| 11 Oct 20:44:49 CEST (18:44:49 UTC) | `2bb23b34-b57d-4969-8b41-097cc7348dcd` "Server needs attention" | medium | "runner (forgejo) keeps restarting: 4 times in 15 min, last exit code 1." |
| 12 Oct 15:32:49 CEST (13:32:49 UTC) | `f25f0a90-2d02-4bfd-80ff-bab1e90c2872` "Server recovered" | low | "Back to normal after about 18 h 48 min." |

So a restart-loop warning has reached the phone for real, through the same path every Status check uses. That was before the switch to app push on 12 Oct; since then no `status.*` notification has been sent.

## The host-health restart flap

**What the container shows.** host-health has not crashed or been restarted by Docker:

- `lifeos-host-health-1` was created at the P17-03 deploy on 12 Oct 16:51:22 CEST (14:51:22 UTC), has restart count 0, and has never exited since.
- Its log has 18,173 lines, all `http request` with status 200: 6,059 `/v1/status`, 6,057 `/v1/history`, 6,057 `/ready`.
- `lifeos-host-health-docker-1`, created 11 Oct 20:39:31 CEST, has restart count 0 and logged only its start.
- Services has never reported host-health as not ready to the monitor.

**What flapped.** The Server check flapped, around host-health's own restarts. At the BUG-35 redeploy (host-health and its gateway started at 20:39:31 CEST), the runner was still looping. The Server check went to ok within a minute, and the monitor sent the false "Server recovered". Four minutes later the loop was back ("4 times in 15 min"). P17-01's first start shows the same pattern: the first warning, within minutes of host-health going live (about 19:20 CEST), counted 3.

**Likely cause** (not verified):

1. The runner died about once a minute (4 deaths within 5 minutes of the redeploy), so a full 15-minute window would hold about 15. Both counts instead match the minutes since host-health started.
2. host-health keeps its restart counts in memory. On its first sample it asks the docker gateway for the last 15 minutes of events (`since = now − window` in `internal/health/sampler.go`).
3. The Docker Engine answers such a `since` query only from a small in-memory buffer of its most recent events: 256 in the moby source (`daemon/events`), counting every event type.
4. On this host the health checks alone produce exec events at about 2 per second: Postgres every 2 s, the processor and Whisper every 10 s, cloudflared every 30 s. The buffer therefore reaches back about two minutes, and less while the verify stack runs its own 2-second Postgres check.
5. So a freshly started host-health sees only the last minute or two, and a running loop reads as cleared until it dies three more times.

**A second risk from the same buffer** (not observed). Each sample asks for the events since the previous one. When more than 256 events happen in a minute, the oldest are dropped silently; the verify stack starting or stopping can do that. A dropped die/start pair undercounts a loop. A dropped `kill` or `stop` before a `docker restart` would count it as a crash.

**Not verified.** `docker events` is outside the read-only commands of this report, and the runner is gone, so the loop cannot be observed again. No code was changed.

## Required acceptance

| # | Required | Evidence |
| --- | --- | --- |
| 1 | Live facts match the host (`free -h`, `df -h`, `docker ps`) | **Yes.** In the table above, every field matches, with the sample 1.1 s older than the host commands. Containers matched exactly even mid-run. |
| 2 | A simulated memory-pressure or restart-loop warning reaches the phone on the verify stack | **Not run this time**, because of the resource limits (another job held the verify stack). The test that covers it is `make runtime-status-smoke` → `runtime-status.spec.ts` › "low memory on the server is a problem naming the biggest users". It stops at Status and Home: the verify stack's monitor has no Notifications URL and no delivery channel, and there is no restart-loop fixture. The phone leg is proven by the live restart-loop warning on 11 Oct (above) and the monitor's tests. |
| 3 | Docs | [Host health](../services/host-health.md) (sources, the gateway, thresholds, the restart rule, Life OS's use), [Monitor](../services/monitor.md), and this report. |

**Verdict: accepted with follow-ups.**

## Follow-ups

- **The restart window after a host-health start, and the event buffer** (above). This should get a bug ticket. A fix could take either of two routes:
  - Keep the gateway's event stream open instead of replaying `since` every minute.
  - Report restart counts as incomplete for the first window after host-health starts, rather than as zero, so that the Server check does not report a recovery it cannot know.

  Either way, [Host health → Limits](../services/host-health.md#limits) should mention the limit.
- **Run the simulated warning** with `make runtime-status-smoke` when the server is free, one job at a time.
- **A memory warning to the phone through app push** has not happened live. Neither has any `status.server` since the switch on 12 Oct.
- **A second Server warning while one holds** is not notified again: the monitor's unit is a check, as documented in [Monitor → Limits](../services/monitor.md#limits). Only a change to problem (high) would notify.
- **Life OS's own use** (P17-03) was not compared with `docker stats`.
