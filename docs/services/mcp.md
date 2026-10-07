# MCP and AI (planned)

The Go MCP service is available behind `/api/mcp/mcp` using streamable HTTP, with `/api/mcp/health` and `/api/mcp/ready` probes. Its first typed read tools call Finance monthly summary/transactions, Tasks list, Inbox list, and Calendar list through the services' HTTP APIs. It never owns or queries Finance, Tasks, Inbox, or Calendar tables directly; upstream failures become understandable MCP tool errors.

Inbox processing will use a lightweight/fast Codex profile for routine RU/DE/EN triage, with dry-run support, ambiguity escalation, retry/idempotency protection, and an audit trail. Until then, `/api/mcp/` deliberately responds unavailable rather than implying an AI service exists.
