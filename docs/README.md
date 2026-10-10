# Life OS technical documentation

Life OS is a local-first personal operating system. This index is the human-readable, repository-native source for developers; all links are ordinary relative Markdown links and work in GitHub and Obsidian.

## Start here

- [Architecture overview](architecture/overview.md) — system boundaries and component map.
- [Networking and data flows](architecture/networking.md) — ingress and common request paths.
- [Editable entities and UI/agent parity](architecture/editable-entities.md) — how editing is added to a domain so the Dashboard and the agent share one implementation.
- [Getting started](development/getting-started.md) — run the stack locally.
- [Services](services/finance.md) — Finance, [Tasks](services/tasks.md), [Inbox](services/inbox.md), [Calendar](services/calendar.md), [MCP](services/mcp.md), [Voice/STT](services/voice-stt.md), [Activity](services/activity.md) (correlated activity/event trace), and [Notifications](services/notifications.md) (the one path for telling the owner something).
- [MCP tool contract](services/mcp-tool-contract.md) — permitted semantic AI actions and safety boundaries.
- [API overview](api/overview.md) — gateway addresses, all current endpoints, examples, and errors.
- [Dashboard](frontend/dashboard.md), [data and code generation](development/data-and-codegen.md), and [operations](operations/deployment.md).
- [Cloudflare Tunnel and Access](operations/cloudflare.md) — the authenticated public path to the gateway. Sign-off: [Phase 9 verification](verification/phase-9.md).
- [ntfy](operations/ntfy.md) — the self-hosted ntfy server that pushes notifications to the owner's phone, on its own network and tunnel route.

## Scope

Implemented: Finance, Tasks, Inbox, Calendar, Voice, and MCP services; PostgreSQL ownership/migrations; generated OpenAPI clients; a Next.js dashboard; Finance CSV import; a single Docker Nginx ingress; local authenticated Codex Inbox processing; and a private multilingual `whisper.cpp` worker. Voice recordings are queued asynchronously, transcribed, captured in Inbox, and can follow the same constrained MCP action path. Phase 8 added an attention-first Home, a Tasks workspace where undated Tasks stay visible, Task editing from both the Dashboard and the agent, Human Review for ambiguous changes, the Activity service and Event Log, a Status page with Codex usage, durable retry scheduling, and a hardened Codex runtime. See [Phase 8 verification](verification/phase-8.md). The routine AI profile was chosen by the [AI runtime benchmark](verification/ai-runtime-benchmark.md). Phase 10 made Calendar a full product: an editable event lifecycle with search, a FullCalendar workspace, AI and MCP parity for events, ICS feeds with ZAW waste collection, and Calendar in the Event Log, Home and Status. See [Phase 10 verification](verification/phase-10.md). Phase 12 adds the Notifications service: one path through which every service lets the owner know about something, stored for the Dashboard and delivered through server-configured channels.

Deferred: Android, Wear OS, public application authentication, external synchronization, and a general-purpose message bus. Android and Wear OS belong in separate repositories.

When a ticket changes behavior, API, configuration, networking, persistence, operations, or a user-visible flow, update the relevant page in this directory in the same branch.
