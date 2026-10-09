# Phase 9 verification

Phase 9 made Life OS reachable from outside the private network through Cloudflare Access and a Cloudflare Tunnel. See [Cloudflare Tunnel and Access](../operations/cloudflare.md). The sign-off on 2026-10-09 used the real public hostname `https://lifeos.gamch1k.com`, never a local shortcut.

| # | Required | Evidence |
| --- | --- | --- |
| 1 | Anonymous requests cannot reach Life OS | `tools/check-cloudflare-access`: every page and API, writes, and requests with a forged `CF_Authorization` cookie or `Cf-Access-Jwt-Assertion` header get a 302 to `damp-surf-d6be.cloudflareaccess.com`. Nothing was captured. |
| 2 | Authorized Access login works | The owner logged in with the email one-time PIN from outside the WireGuard path. |
| 3 | Dashboard loads | That session: `GET /` ×16 → 200, arriving through the connector. |
| 4 | API-backed Tasks, Calendar, and Inbox views work | Same session: `/tasks`, `/calendar`, `/inbox`, `/finance` → 200. A Task edit (`PATCH /api/tasks/v1/tasks/{id}`) → 200 and was persisted. |
| 5 | Capture works | `POST /api/inbox/v1/inbox-items` through the public route → 201. The automatic processor handled it (Human Review, a test text), and it was then discarded through the public route (200). |
| 6 | Voice upload works within the limits | A WAV upload through the public route → 202, transcribed ("This is a remote voice test, please ignore it."), and captured into Inbox. A 27 MB upload is refused by the gateway (413, 26 MB limit). Cloudflare reports it to the client as 502 (see below). |
| 7 | Activity and Status work | The owner's session: `/activity` ×14, `/status` ×11 → 200. |
| 8 | No public origin bypass | The gateway port is refused on the public IP. The host Nginx serves no Life OS route for the hostname. From the connector's network, only the gateway is reachable. Cloudflare headers are trusted only from that network (a spoofed `CF-Connecting-IP` over WireGuard is ignored). |
| 9 | The private and maintenance paths behave as documented | WireGuard `http://10.66.66.1:3001` → 200 on pages and APIs. The host can use `docker compose` and the WireGuard address. |

## Findings outside Phase 9

Both are tracked as bug tickets in Notion.

- **Voice recordings from real browsers fail.** Firefox Ogg and Safari MP4 are rejected at upload, and whisper.cpp decodes only WAV, so Chrome's WebM fails at transcription. Earlier acceptance runs used WAV fixtures. This is not caused by Cloudflare; WAV uploads work through the public route.
- **An over-limit upload shows as 502 through Cloudflare.** The gateway answers 413, but it closes the connection while the client is still sending, and Cloudflare turns that into 502.

## Follow-ups

- Enable **Protect with Access** on the tunnel route, so the connector validates the Access token itself.
- Delete the temporary `life-os-acceptance` service token and its `Acceptance test` policy, and remove the two `LIFE_OS_CF_ACCESS_CLIENT_*` lines from `.env`, unless they are kept on purpose for `tools/check-cloudflare-access`.
