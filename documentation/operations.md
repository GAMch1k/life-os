# Operations and troubleshooting

Use `make life-up` to build and start the stack, `docker compose ps` to inspect health, and `docker compose logs <service>` for logs. The migrate containers are expected to exit successfully; the long-running domain services and gateway should remain up. Check `/api/<domain>/ready` through the gateway before diagnosing dashboard pages.

If the dashboard cannot load, verify the gateway listener first (`curl http://127.0.0.1:3000/` by default), then its upstream readiness. A 502 commonly means an upstream container has not started or failed migration; inspect that service and its migration container. A bind failure means the configured gateway address/port is occupied or unavailable.

For safe functional verification, `make e2e-smoke` uses an isolated Compose project, isolated named volume, and a temporary `127.0.0.1:13001` gateway. It removes only those disposable verification resources after completion. Production-like data is not reset by ordinary stop/start commands.

Backups/restores are not implemented as an automated Life OS workflow yet. Until such tooling exists, take database backups using an operator-controlled PostgreSQL procedure and test restore separately; document the exact process when it becomes supported.
