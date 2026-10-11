# Host health service

Tells the Status page how the server is doing: memory, load, disks, uptime,
and every container on the host (all compose projects, not only Life OS).
It samples once a minute, keeps the last 24 hours in memory, and reports
findings against thresholds: low memory, low disk, a 15-minute load above
the CPUs, a container restarting in a loop or unhealthy.

It only reads. Memory, load, uptime, and CPUs come from its own `/proc`
(Docker does not namespace those files, so they are the host's); disks from
statfs on read-only, non-recursive bind mounts of `/` and the Docker data
root; containers from the docker gateway, `/service docker-gateway`, a
separate container that alone opens the Docker socket and answers three
fixed questions, re-encoding only the fields host-health needs. It has no
database, no writes, and no gateway route.

See [`docs/services/host-health.md`](../../docs/services/host-health.md).

## Layout

```text
api/openapi.yaml       # HTTP contract (GET /v1/status, GET /v1/history)
cmd/server/            # host-health, and `docker-gateway`
internal/api/          # HTTP adapters
internal/config/       # configuration
internal/docker/       # the read-only docker gateway and its client
internal/health/       # sampling, 24-hour history, findings, fixtures
internal/host/         # /proc parsing and statfs
```

## Tests

```sh
GOWORK=off go test -count=1 ./...
```

`make run` serves a made-up host (`HOST_HEALTH_FIXTURE=healthy` or
`low-memory`), as the verification stack does.
