# Phase 7 runtime verification

The Phase 7 checks deliberately separate deterministic unit/integration coverage from user-triggered runtime acceptance. Fake planner/tool tests do not prove the Codex path.

- `make browser-e2e` runs Playwright against `lifeos-verify-*`. It covers a browser Dashboard text capture through Inbox, automatic authenticated local Codex CLI processing, MCP and a persisted Task; a CSV file chooser upload with visible counts and a persisted transaction; and a mocked browser `MediaRecorder` hardware boundary that uses the production Voice UI upload and polling path through a real Whisper transcription and persisted Inbox handoff. It requires the local authenticated Codex session configured for the isolated processor. The temporary `jfk.wav` fixture is downloaded only while the test runs and is never committed.
- `make processor-smoke` is the direct Dashboard-style text capture runtime smoke.
- `make processor-codex-actions-smoke` is an opt-in authenticated Codex CLI smoke for explicit Calendar and Finance captures. It seeds only a disposable Finance account, then proves automatic processing records exactly one Calendar event and exactly one Finance transaction through MCP.
- `make action-idempotency-smoke` exercises owner commit/replay boundaries after the service-side mutation response is lost, and asserts the same persisted owner record is returned.
- `make voice-lease-recovery-smoke` uploads through the gateway, kills a claimed Voice worker, and proves automatic lease reclaim, real Whisper completion, and exactly-one Inbox capture.

`browser-e2e` and the Codex action smoke are runtime acceptance tests, not replacements for unit tests. All disposable tests use `tools/verify-compose` and never normal persistent resources.
