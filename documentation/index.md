# Life OS

Life OS is a local-first personal operating system. Its current verified scope is a dashboard backed by independent Finance, Tasks, Inbox, and Calendar services. The monorepo deliberately keeps each domain's data and behavior separate while presenting one browser experience.

Start with [Getting started](getting-started.md), then read [Architecture](architecture.md). The service guide explains the live HTTP contract; the operations guide explains how to run and diagnose it.

## Current and deferred scope

Implemented: PostgreSQL provisioning, migrations, OpenAPI-driven services, the Next.js dashboard, CSV import, and one Docker Nginx ingress. Deferred: public authentication, queues, shared domain tables, MCP/AI processing, voice/STT, Android, and Wear OS clients. Reserved gateway routes make future MCP and voice additions explicit without pretending they are running today.

## Repository map

| Path | Role |
| --- | --- |
| `apps/dashboard` | Next.js browser interface and generated TypeScript API types |
| `services/*` | Independently deployable Go + Fiber domain services |
| `pkg` | Shared runtime, configuration, and database infrastructure only |
| `infra` | Compose, PostgreSQL provisioning, and gateway configuration |
| `contracts` | HTTP conventions and contract tooling |
| `tools` | generation, checks, and smoke scripts |
