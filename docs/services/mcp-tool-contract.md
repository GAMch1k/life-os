# MCP tool contract

The future MCP service exposes semantic, structured operations only. It calls the owning HTTP API and never SQL, shells, files, or another service's storage. Tool outputs are JSON-shaped entity/list results or the service error envelope; inputs use the corresponding OpenAPI request fields.

| Tool | Service API | Safety |
| --- | --- | --- |
| `inbox_list`, `inbox_get` | list/get inbox items | read-only |
| `inbox_resolve`, `inbox_discard` | resolve/discard item | state-changing; record audit reason |
| `task_create`, `task_list`, `task_get`, `task_update`, `task_complete` | Tasks endpoints | creates/changes work; completion is explicit |
| `calendar_create`, `calendar_list`, `calendar_get`, `calendar_update`, `calendar_delete` | Calendar endpoints | delete is destructive; require explicit intent |
| `finance_month_summary`, `finance_find_transactions` | Finance reads | read-only |
| `finance_create_manual_transaction` | Finance transaction create | high-impact; require explicit amount/account/currency/date confirmation |

Example routing: “Remind me to call the insurer tomorrow” becomes `task_create` with title and due time. “Add a dentist appointment next Tuesday” becomes `calendar_create`. “I paid €20 for fuel” must remain an Inbox item unless the user has explicitly asked to record the transaction; then `finance_create_manual_transaction` is allowed with structured Money.

The processor may mark an Inbox item `processing`, but only resolves it after a successful downstream API action. Ambiguous requests remain raw for clarification; retries use the later audit/idempotency mechanism rather than duplicate actions.
