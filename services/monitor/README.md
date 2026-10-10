# Monitor service

Tells the owner when Life OS breaks or recovers. Every
`MONITOR_INTERVAL_SECONDS` it reads the Dashboard's runtime status
(`GET /api/status`, the checks the Status page shows) and, once a check has
held a new state for `MONITOR_CONFIRMATIONS` evaluations in a row, sends a
notification through the Notifications service (`pkg/notify`): high for a
problem, medium for a warning, low for a recovery. When the status itself
cannot be read, it says so ("Life OS status is unreachable").

The rules that make something a problem live only in the Dashboard
(`apps/dashboard/src/lib/runtime-status.ts`); the monitor compares their
results over time. It has no database, no public API, and no gateway route:
only `/health` and `/ready` (ready while evaluations keep running).

See [`docs/services/monitor.md`](../../docs/services/monitor.md) for the
thresholds, importance mapping, dedupe keys, and the restart rule.

## Layout

```text
cmd/server/            # process entrypoint: health endpoints and the polling loop
internal/config/       # configuration
internal/monitor/      # status client, transition tracker, notifications
```

## Tests

```sh
GOWORK=off go test -count=1 ./...
```
