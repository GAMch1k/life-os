# Operations and troubleshooting

Docker Compose runs PostgreSQL, one migration job and one service per domain, Dashboard, and gateway. Migration jobs should exit successfully; long-running services and gateway should remain up. Start with `make life-up`; stop non-destructively with `make infra-down`.

If a page fails, test the gateway (`curl http://127.0.0.1:3000/` by default), then `/api/<domain>/ready`, then inspect `docker compose logs <service>` and its migration job. A 502 normally means an unavailable upstream; a bind error means the configured gateway address/port is unavailable. Do not publish individual service ports as a diagnostic shortcut.

Host Nginx owns public TLS/Basic Auth and proxies to the localhost gateway; [`../host-nginx-example.conf`](../host-nginx-example.conf) is an operator-applied example. Automated backup/restore is not implemented: operators must use an external PostgreSQL process and test restores independently until a supported workflow exists.
