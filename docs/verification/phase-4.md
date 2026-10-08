# Phase 4 verification sign-off

Phase 4 establishes the verified baseline before MCP/AI work. The final verification on the merged baseline covered:

- `make check`: OpenAPI linting, Go/TypeScript code generation, sqlc generation, Go tests, dashboard formatting/lint/tests/typecheck/build.
- `make verify-clean-db`: clean disposable database provisioning, migrations, and ownership isolation.
- `make e2e-smoke`: isolated end-to-end Finance CSV import/idempotency, Tasks, Inbox, Calendar, Dashboard, and gateway flow.
- `make life-smoke`, `./tools/smoke-core-services`, and `./tools/smoke-dashboard`: live gateway-facing services and routed dashboard behavior.

The gateway is the only published application listener; domain services and PostgreSQL remain private Docker endpoints. Finance CSV imports, dashboard deep links/routes, generated artifacts, and migrations are covered by the above checks. Repository-native Markdown in `docs/` is the canonical human documentation; GitHub Pages and Wiki are intentionally not dependencies.

Known non-blocking limits: public application authentication, backup/restore automation, external calendar synchronization, Android, and Wear OS are deferred by roadmap scope. MCP/AI and voice/STT are implemented in later phases and documented separately.
