# Life OS technical documentation

Life OS is a local-first personal operating system. This index is the human-readable, repository-native source for developers; all links are ordinary relative Markdown links and work in GitHub and Obsidian.

## Start here

- [Architecture overview](architecture/overview.md) — system boundaries and component map.
- [Networking and data flows](architecture/networking.md) — ingress and common request paths.
- [Getting started](development/getting-started.md) — run the stack locally.
- [Services](services/finance.md) — Finance, [Tasks](services/tasks.md), [Inbox](services/inbox.md), [Calendar](services/calendar.md), [MCP](services/mcp.md), and [Voice/STT](services/voice-stt.md).
- [API overview](api/overview.md) — gateway addresses, all current endpoints, examples, and errors.
- [Dashboard](frontend/dashboard.md), [data and code generation](development/data-and-codegen.md), and [operations](operations/deployment.md).

## Scope

Implemented: four Go/Fiber domain services, PostgreSQL ownership/migrations, OpenAPI generation, a Next.js dashboard, Finance CSV import, and one Docker Nginx ingress. MCP/AI, voice/STT, Android, Wear OS, public application authentication, queues, and external sync remain deferred. Android and Wear OS belong in separate repositories.

When a ticket changes behavior, API, configuration, networking, persistence, operations, or a user-visible flow, update the relevant page in this directory in the same branch.
