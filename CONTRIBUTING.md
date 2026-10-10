# Contributing to Life OS

Work one Notion ticket per branch. Start from the current primary branch, make small coherent commits, run the ticket's relevant verification, merge only after the primary branch is healthy, then update the ticket.

Documentation is part of the definition of done. Any change to architecture, API contracts, configuration, networking, persistence, deployment/operations, or user-visible behavior must update the relevant English Markdown page under [`docs/`](docs/README.md) in the same branch. Keep links relative and readable in GitHub and Obsidian. Generated OpenAPI output supplements, never replaces, the human explanation.

Services own their own database, migrations, and domain types. Do not access another service's tables; use APIs. Do not publish individual service ports, modify host Nginx/firewall/DNS, commit secrets, or run destructive data operations without explicit authorization.

## Development tools and live data

Development tools and agents (Claude Code and its subagents, scripts, test runners) are neither the owner nor the Life OS AI, and the Event Log must never show their work as either. Every service write must say who makes it in `X-Life-OS-Actor`; a write without one is rejected with `400 actor_required` (see [Activity](docs/services/activity.md#the-actor-gate)).

- Any write a development tool or agent makes against the live stack sends `X-Life-OS-Actor: dev:<tool>`. Claude Code and its subagents use `dev:claude-code`, for example `curl -H 'X-Life-OS-Actor: dev:claude-code' -H 'content-type: application/json' -d '{...}' http://127.0.0.1:3000/api/tasks/v1/tasks`. The Event Log then shows "Developer tool (claude-code)".
- Never send `user` from a development tool: that is the owner's own actor, used by the Dashboard. `agent:` and `system:` are internal and the gateway drops them.
- `tools/` scripts send `dev:<script name>`; Playwright specs that write through the `request` fixture import `test` from `apps/dashboard/e2e/fixtures.ts`, which sends `dev:e2e`, while the browser keeps the Dashboard's `user`.
- Acceptance, smoke, and benchmark runs belong on the disposable `lifeos-verify` stack (`tools/verify-compose`), not on live data. Touch live data only when a task requires it, and then only with a `dev:` actor.
