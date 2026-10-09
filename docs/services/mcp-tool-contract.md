# MCP tool contract

The MCP service exposes semantic, structured operations only. It calls the owning HTTP API and never SQL, shells, files, or another service's storage. Tool outputs are JSON-shaped entity/list results or the service error envelope; inputs use the corresponding OpenAPI request fields.

| Tool | Service API | Safety |
| --- | --- | --- |
| `inbox_list`, `inbox_get` | list/get inbox items | read-only |
| `inbox_resolve`, `inbox_discard` | resolve/discard item | state-changing; record audit reason |
| `task_create`, `task_list`, `task_search`, `task_update`, `task_complete`, `task_reopen` | Tasks endpoints | creates/changes work through the same Task operations as the Dashboard; completion and reopening are explicit; an existing Task is changed only when `task_search` finds exactly one match (see [editable entities](../architecture/editable-entities.md)) |
| `calendar_create`, `calendar_list`, `calendar_get`, `calendar_update`, `calendar_delete` | Calendar endpoints | delete is destructive; require explicit intent |
| `finance_month_summary`, `finance_find_transactions` | Finance reads | read-only |
| `finance_create_manual_transaction` | Finance transaction create | high-impact; require explicit amount/account/currency/date confirmation |

Example routing: “Remind me to call the insurer tomorrow” becomes `task_create` with title and due time. “Add a dentist appointment next Tuesday” becomes `calendar_create`. “I paid €20 for fuel” must remain an Inbox item unless the user has explicitly asked to record the transaction; then `finance_create_manual_transaction` is allowed with structured Money.

The processor records a processing attempt before actions and resolves an Inbox item only after a successful downstream API action. Ambiguous requests go to Human Review; retries use the audit/idempotency mechanism rather than duplicate actions. `inbox_record_processing_attempt` accepts `nextRetryAt` (RFC3339) and `retryReason` with a `retryable` status, and Inbox persists them as the item's retry schedule. `inbox_record_attempt_runtime` records an attempt's runtime state, owner, and Codex session, and refreshes its heartbeat. `inbox_list_stale_attempts` lists attempts abandoned by a stopped processor.
