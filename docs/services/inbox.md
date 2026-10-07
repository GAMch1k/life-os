# Inbox service

Inbox is the capture queue in `inbox_db`. It stores user content, source (`manual`, `web`, `voice`, `watch`, or `email`), metadata/media references, and a lifecycle: `raw` → `processing` → `resolved` or `discarded`. It intentionally does not decide whether a capture becomes a task, event, or transaction. Processing attempts are kept separately with a per-item idempotency key, agent/model identity, attempted actions, timestamps, and retryable/failed/processed outcome; raw content is never consumed or overwritten.

The current dashboard captures and resolves/discards items. Phase 5 MCP/Codex processing will claim raw items, preserve an audit trail, handle ambiguity safely, and call core APIs. Invalid requests return 400 and missing items 404; readiness is at `/ready`. Test with `make db-test SERVICE=inbox` and `make e2e-smoke`.
