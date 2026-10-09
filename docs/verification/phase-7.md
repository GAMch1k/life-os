# Phase 7 runtime verification

The Phase 7 checks deliberately separate deterministic unit/integration coverage from user-triggered runtime acceptance. Fake planner/tool tests do not prove the Codex path.

- `make browser-e2e` runs Playwright against `lifeos-verify-*`. It covers a browser Dashboard text capture through Inbox, automatic authenticated local Codex CLI processing, MCP and a persisted Task; a CSV file chooser upload with visible counts and a persisted transaction; and a mocked browser `MediaRecorder` hardware boundary that uses the production Voice UI upload and polling path through a real Whisper transcription and persisted Inbox handoff. It requires the local authenticated Codex session configured for the isolated processor. The temporary `jfk.wav` fixture is downloaded only while the test runs and is never committed.
- `make processor-smoke` is the direct Dashboard-style text capture runtime smoke.
- `make processor-codex-actions-smoke` is an opt-in authenticated Codex CLI smoke for explicit Calendar and Finance captures. It seeds only a disposable Finance account, then proves automatic processing records exactly one Calendar event and exactly one Finance transaction through MCP.
- `make action-idempotency-smoke` exercises owner commit/replay boundaries after the service-side mutation response is lost, and asserts the same persisted owner record is returned.
- `make voice-lease-recovery-smoke` uploads through the gateway, kills a claimed Voice worker, and proves automatic lease reclaim, real Whisper completion, and exactly-one Inbox capture.

`browser-e2e` and the Codex action smoke are runtime acceptance tests, not replacements for unit tests. All disposable tests use `tools/verify-compose` and never normal persistent resources.

## Deployed-runtime acceptance

The 2026-10-08 Phase 7 acceptance pass used the deployed WireGuard gateway at
`http://10.66.66.1:3001`, not the disposable Compose overlay. The processor
reported its existing ChatGPT-backed Codex session from inside its own container
with `docker compose exec processor codex login status`; all gateway readiness
endpoints returned 200. Playwright then proved these observed chains:

- Dashboard text capture → persisted Inbox → automatic processor claim → local
  `codex exec` → MCP `task_create` → persisted Task → processed audit → resolved
  Inbox.
- Dashboard Calendar capture → the same automatic processor path → exactly one
  persisted Calendar event; the test deleted that tagged event afterwards.
- Dashboard Finance capture, using the explicitly supplied dedicated P4 test
  account → MCP finance mutation → exactly one tagged persisted transaction.
- Browser MediaRecorder fixture → Voice upload/job polling → real Whisper
  transcript → exactly one Inbox item → automatic Codex/MCP task creation →
  persisted Task and resolved Inbox.
- Russian and German Dashboard text captures each reached a processed Inbox
  audit and persisted Task; the English path is covered by the text and Voice
  checks above.

The Calendar and Finance deployed checks are intentionally opt-in E2E specs:
`LIFE_OS_E2E_LIVE_CALENDAR=1` enables the former and
`LIFE_OS_E2E_LIVE_FINANCE_ACCOUNT_ID=<dedicated-test-account-id>` enables the
latter. They must never be aimed at an account containing real financial data.
The Finance check leaves a clearly tagged one-cent test transaction because the
current Finance API has no transaction-delete endpoint. Voice acceptance used a
temporary, locally generated WAV fixture and left only the resulting clearly
named task/audit records. This makes persistent test-data effects explicit
rather than hiding them behind a supposedly read-only smoke test.
