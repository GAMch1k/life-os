# Tasks service

Tasks owns actionable work in `tasks_db`: title, optional notes/due date, completion timestamps, and optional daily/weekly/monthly recurrence. It accepts dashboard/API input and returns Task JSON; it does not interpret Inbox items or access any other database. Gateway base: `/api/tasks`.

Create/list tasks, patch editable fields, then use the explicit completion action. Input limits and date/recurrence validation produce 400; unknown UUIDs produce 404. `/health` and `/ready` support operations. Test with `make db-test SERVICE=tasks` and the integrated smoke suites. Future MCP may call these APIs, never tables.
