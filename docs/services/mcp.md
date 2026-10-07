# MCP and AI (planned)

MCP is not implemented yet. Phase 5 will add a Go MCP service behind `/api/mcp/` that exposes safe semantic tools backed by existing service APIs. It will never own or query Finance, Tasks, Inbox, or Calendar tables directly.

Inbox processing will use a lightweight/fast Codex profile for routine RU/DE/EN triage, with dry-run support, ambiguity escalation, retry/idempotency protection, and an audit trail. Until then, `/api/mcp/` deliberately responds unavailable rather than implying an AI service exists.
