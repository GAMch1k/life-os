# MCP and AI (planned)

The Go MCP service is available behind `/api/mcp/mcp` using streamable HTTP, with `/api/mcp/health` and `/api/mcp/ready` probes. Its first typed read tools call Finance monthly summary/transactions, Tasks list, Inbox list, and Calendar list through the services' HTTP APIs. It never owns or queries Finance, Tasks, Inbox, or Calendar tables directly; upstream failures become understandable MCP tool errors.

The non-interactive processor is configured by `LIFE_OS_CODEX_MODEL` (default `gpt-5.6-luna`) and `OPENAI_API_KEY`; it is intentionally separate from domain handlers and targets the MCP endpoint, not service databases. It will use the Inbox audit API to retain attempts, ambiguity, retries, and outcomes.
