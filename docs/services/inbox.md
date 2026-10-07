# Inbox service

Inbox is the capture queue in `inbox_db`. It stores user content, source (`manual`, `web`, `voice`, `watch`, or `email`), metadata/media references, and a lifecycle: `raw` → `processing` → `resolved` or `discarded`. It intentionally does not decide whether a capture becomes a task, event, or transaction. Processing attempts are kept separately with a per-item idempotency key, agent/model identity, attempted actions, timestamps, and retryable/failed/processed outcome; raw content is never consumed or overwritten.

The current dashboard captures and resolves/discards items. The Phase 5 MCP/Codex processor reads raw items only through MCP, preserves an audit trail, handles ambiguity safely, and calls core APIs through MCP tools. Its processing-attempt result endpoint updates the audit record without permitting cross-item changes. Invalid requests return 400 and missing items 404; readiness is at `/ready`. Test with `make db-test SERVICE=inbox` and `make e2e-smoke`.
