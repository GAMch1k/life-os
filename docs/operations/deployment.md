# Operations and troubleshooting

Docker Compose runs PostgreSQL, one migration job and one service per domain, Dashboard, and gateway. Migration jobs should exit successfully; long-running services and gateway should remain up. Start with `make life-up`; stop non-destructively with `make infra-down`.

If a page fails, test the gateway (`curl http://127.0.0.1:3000/` by default), then `/api/<domain>/ready`, then inspect `docker compose logs <service>` and its migration job. A 502 normally means an unavailable upstream; a bind error means the configured gateway address/port is unavailable. Do not publish individual service ports as a diagnostic shortcut.

Host Nginx owns public TLS/Basic Auth and proxies to the localhost gateway; [`../host-nginx-example.conf`](../host-nginx-example.conf) is an operator-applied example. Automated backup/restore is not implemented: operators must use an external PostgreSQL process and test restores independently until a supported workflow exists.

## Safe verification on a deployed host

Do not use raw `docker compose down --volumes` with the normal deployment to
test migrations or smoke flows. `make verify-clean-db` and `make e2e-smoke`
run through `tools/verify-compose`, which refuses a Compose plan unless every
test network and persistent volume begins with `lifeos-verify-`. Its cleanup
may remove only those disposable resources. `make verify-isolation` also
creates temporary sentinel files in normal PostgreSQL, Voice-audio, and
Whisper-model volumes, runs the disposable smoke suite, and confirms the
sentinels remain before cleaning them up.
## Codex Inbox processor authentication

Automatic Inbox processing is a normal Compose service and uses the host user's existing authenticated Codex CLI session. Before `docker compose up --build`, configure the ignored `.env` without copying its contents into Git:

```sh
LIFE_OS_CODEX_HOME_HOST_PATH=/absolute/path/to/.codex
LIFE_OS_CODEX_RUNTIME_UID=1000 # output of: id -u
LIFE_OS_CODEX_MODEL=gpt-5.6-terra
```

The processor bind-mounts that path read-only, runs as the owning UID, and copies only the authentication and optional Codex configuration to a container tmpfs. Do not mount a credential as a writable volume and do not add `auth.json` or API keys to `.env`. After deployment, verify from the processor container itself with `docker compose exec processor codex login status`; it should report the already authenticated ChatGPT session. Its private readiness probe only becomes ready after a successful polling pass through MCP.
