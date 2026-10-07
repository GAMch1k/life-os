# Networking and security

```text
Internet
  → host Nginx (systemd): TLS and initial Basic Auth
  → 127.0.0.1:<gateway port>
  → Life OS Docker Nginx
  → private Docker bridge network
  → Dashboard / Finance / Tasks / Inbox / Calendar
```

Only `gateway` publishes a port. Its defaults are `127.0.0.1:3000`; PostgreSQL, the dashboard, and every Go service use private Docker networking and `expose` only. Do not add service `ports` merely for debugging—use `docker compose exec` or a temporary isolated verification stack instead.

| Gateway path | Internal target |
| --- | --- |
| `/` | Dashboard |
| `/api/finance/` | Finance |
| `/api/tasks/` | Tasks |
| `/api/inbox/` | Inbox |
| `/api/calendar/` | Calendar |
| `/api/mcp/`, `/api/voice/` | Reserved; unavailable until implemented |

An example host configuration is `docs/host-nginx-example.conf`. It is documentation only: this repository never modifies the host Nginx, firewall, TLS certificates, or Basic Auth automatically.
