# Phase 7 runtime verification

The Phase 7 checks deliberately separate deterministic unit/integration coverage from user-triggered runtime acceptance. Fake planner/tool tests do not prove the Codex path.

- `make browser-e2e` runs Playwright against `lifeos-verify-*`: a browser submits the Dashboard Inbox capture form, the persisted item is automatically processed by the authenticated local Codex CLI and MCP, and the test asserts its processed audit state and persisted Task. It requires the local authenticated Codex session configured for the isolated processor.
- `make processor-smoke` is the direct Dashboard-style capture runtime smoke.
- `make action-idempotency-smoke` exercises owner commit/replay boundaries.
- `make voice-lease-recovery-smoke` uploads through the gateway, kills a claimed Voice worker, and proves automatic lease reclaim plus exactly-one Inbox capture.

Browser CSV and browser Voice status flows remain explicit P7-09 acceptance work. All disposable tests use `tools/verify-compose` and never normal persistent resources.
