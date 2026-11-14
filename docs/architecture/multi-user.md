# Multi-user design

Status: design for Phase 21 (P21-02). P21-03 is built: accounts, sign-in, server roles, the gateway's auth check, identity tokens, and legacy access ([As built in P21-03](#as-built-in-p21-03) lists where it differs from this page); so are P21-04, the admin-only surfaces, and P21-05, the security log, diagnostics, and `actor_user_id` ([As built in P21-04 and P21-05](#as-built-in-p21-04-and-p21-05)), and P21-06's server side, device pairing and keys, personal tokens, MCP's OAuth, and the `mfa` claim ([As built in P21-06](#as-built-in-p21-06)); the tickets P21-07 to P21-14 build the rest in the order of [Rollout](#rollout). The owner's product decisions of November 2026 are binding and are not argued again here; this page turns them into an architecture and records the technical decisions taken on the way. The login trial (P21-01) chose the library and measured it; its numbers are quoted where they matter.

## Where we start

Life OS has no identity today. There is no user table, no login, no session, and no API token. Whoever reaches the gateway is the owner, and the boundary is outside the app: Cloudflare Access, WireGuard peers, or an SSH tunnel ([Networking](networking.md)).

- The gateway strips Cloudflare's headers (`infra/nginx/life-os.conf.template:114-116`) and passes on one unauthenticated attribution header, `X-Life-OS-Actor`, letting public traffic claim only `user` or `dev:<name>` (`life-os.conf.template:48-52`). Twelve services reject writes without it (`RequireActor`, `pkg/activity/activity.go:104`); the Dashboard always sends `user` (`apps/dashboard/src/lib/api/actor.ts:8`).
- The gateway sends `/api/{finance,tasks,inbox,calendar,food,activity,notifications,voice}/`, `/api/integrations/<name>/`, and `/api/mcp/` straight to the Go services, never through the Dashboard. The Dashboard serves `/`, `/share`, the two OAuth callbacks, and its own `/api/status`, `/api/backups/`, and `/api/processor/`. The gateway itself serves `/android/`.
- Every service allows CORS from any origin (`pkg/httpserver/app.go:125-132`), which is safe only because there are no credentials ([networking.md:55](networking.md) asks for Origin/CSRF checks before cookie auth).
- PostgreSQL has one database and one role per service (`infra/postgres/service-databases.conf`). Each role is `NOBYPASSRLS` (`infra/postgres/provision-service-databases.sh:54`) but owns its tables and runs its own migrations, so row-level security would not apply to it as things stand. No table has an owner, user, or space column, and nine tables are singletons.
- The AI runs on one server-wide account, one run at a time (`LIFE_OS_CODEX_MAX_CONCURRENCY` default 1, `services/mcp/internal/processor/config.go:97`), polls every Inbox item, and its prompts describe the owner (`services/mcp/internal/processor/codex.go:22`: "the owner lives in Germany", English/Russian/German; Germany also in `budget_plans.go:598` and `categories.go:469`). MCP has no authentication and makes every caller `agent:mcp` (`services/mcp/cmd/server/main.go:151-152`).
- Notifications have no recipient (`pkg/notify/notify.go`), push subscriptions have no owner, and ntfy has one topic.
- Personal settings are `.env` values (`LIFE_OS_TIMEZONE`, `LIFE_OS_FINANCE_CURRENCY`, the summary, reminder, expiry, ideas, and bank sync times, `LIFE_OS_ZAW_ICS_URL`) or database singletons; the theme is in `localStorage`.
- The phone and watch apps (`GAMch1k/life-os-app`) reach the owner's server with a Cloudflare Access service token.

## The shape of it

```mermaid
sequenceDiagram
  participant C as Browser or app
  participant G as Gateway (nginx)
  participant A as Dashboard: Better Auth + /internal/auth
  participant S as Go service
  participant P as PostgreSQL
  C->>G: GET /api/tasks/v1/tasks (cookie, device key, or token)
  G->>A: auth_request /internal/auth/check
  A-->>G: 204 + identity token (EdDSA, 5 min) | 401 | 403
  G->>S: request + X-Life-OS-Identity (any client copy dropped)
  S->>S: pkg/identity verifies with pinned public keys
  S->>P: BEGIN; set_config(lifeos.space_ids, ...); queries; COMMIT
  P-->>S: rows of the caller's spaces only (RLS)
```

- **Better Auth 1.7.x (MIT) inside the Next.js Dashboard**, with its own `auth_db` and role in our PostgreSQL. No new containers. The trial measured +22 MiB idle and a 182 MiB peak during ten parallel logins.
- **Spaces own data.** Every user has a personal space; a family is a second space. Every row of user content carries a `space_id` (or a `user_id` for the few things that belong to a person, not a space), and PostgreSQL row-level security enforces it.
- **The gateway authenticates every service route** with `auth_request` and forwards a short-lived signed identity token. Services trust only that token, never the network or a header a client can write.
- **Background work has an identity too**: services sign their own assertions, and act for a person only with a delegation that the auth server mints from an explicit grant.

## Principals and credentials

| Credential | Held by | Arrives as | Principal |
| --- | --- | --- | --- |
| Session | a browser: the Dashboard, the app's TWA | `__Host-lifeos.session` cookie | the user, `via: session` |
| Device key | one paired phone or watch | `Authorization: Bearer lo_dev_…` | the user, `via: device` |
| Personal API token | a script or a development tool | `Authorization: Bearer lo_tok_…` | the user with the token's scopes, `via: token` |
| MCP access token | an MCP client after OAuth consent | `Authorization: Bearer <JWT>` on `/api/mcp/` | the user with the grant's scopes, `via: mcp` |
| Service key | one Life OS service | signs a service assertion | the service, `system:<name>` or `agent:<name>` |
| Delegation | a service, for a user or space that granted it | an identity token the auth server minted on request | the user (or the space), acting through `act` |
| Legacy access | anyone who reaches the gateway, while it is on | nothing | the first account, `via: legacy` ([Rollout](#legacy-access)) |

Device keys and personal tokens are Better Auth API keys (the `api-key` plugin; stored hashed, shown once) told apart by their metadata. Sessions, keys, and grants are all revocable from Settings, and by an admin.

## The identity token

The auth check, and the auth server when it mints a delegation, issue the same kind of token. It travels in one header, `X-Life-OS-Identity`, which is the only thing a service reads to learn who calls.

Header `{"alg":"EdDSA","typ":"lifeos-id+jwt","kid":"auth-20261201"}`. Claims:

| Claim | Meaning |
| --- | --- |
| `iss` | `lifeos-auth`: a fixed name, not the public URL, so changing the hostname changes nothing |
| `aud` | `lifeos-services` |
| `sub` | the user id (UUID); absent for a space-bound delegation |
| `sid` | the credential: session id, API key id, OAuth grant id, or delegation grant id |
| `via` | `session`, `device`, `token`, `mcp`, `delegation`, or `legacy` |
| `act` | on delegations only: the service acting, such as `agent:processor` |
| `role` | the server role: `owner`, `admin`, or `member` |
| `sp` | the spaces and the space role in each: `{"<uuid>":"o","<uuid>":"m"}` (`o`wner, `a`dmin, `m`ember) |
| `psp` | the personal space id |
| `dsp` | where new items of a module go when a request names no space, only when not `psp`: `{"food":"<family uuid>"}` (P21-09) |
| `scp` | scopes, on tokens, MCP grants, and delegations: `tasks:read`, `food:write`, `finance:import`, …; absent means all the user's rights |
| `mods` | the optional modules the user is allowed (P21-12); absent means every installed module |
| `lim` | the user's finite limits (P21-12), such as `{"ai":200,"storMiB":2048}`; unlimited ones are left out |
| `mfa` | (P21-06) `true` only on a browser session that signed in with a second factor (an authenticator code, a recovery code, or a passkey), or turned one on while signed in; never on a device key, a personal token, or an MCP grant. Services' admin routes require it with the role ([As built in P21-06](#as-built-in-p21-06)) |
| `dev` | (P21-06) on a personal token marked as a development tool: its name, which makes the activity actor `dev:<name>` |
| `tz`, `lng` | the user's timezone and language (P21-08) |
| `iat`, `nbf`, `exp`, `jti` | `exp` is `iat` + 300 s |

No name, username, or email is in it. With a personal space and one family the token is under 1 KiB; the check refuses to mint one over 2 KiB (nginx keeps the check's response headers in `proxy_buffer_size`, 4 or 8 KiB), which caps the spaces a user can be in far above what decision 7 allows.

**Lifetime and caching.** Five minutes. The check keeps minted tokens in memory per credential and re-mints one with under four minutes left (so no service gets one older than a minute), or when the user's `authz_version` changed. That counter is a column on the user, bumped by every change a token reflects: role, ban, membership, scopes, limits, modules, profile. The check reads the session (with the user) from the database on every request; Better Auth's cookie cache stays off. So a ban, a revoked session, or a removal from a family takes effect on the next request, not five minutes later. The trial measured a cookie check at 2.4 ms median behind nginx (0.37 ms without auth), an API key check at 3.5 ms, and 613 cookie checks/s at ten concurrent; signing a token on every check cost about 1.4 ms more, which the cache saves. If the check is noticeably slower inside Next.js than in the trial's plain Node server, it moves to a second listener in the same process (started from `instrumentation.ts`), sharing the Better Auth instance.

**Signing and keys: pinned, not fetched.** The trial verified Better Auth's `jwt` plugin tokens against its JWKS. This design deliberately does not: identity tokens are signed by our own code in the Dashboard (`jose`, which Better Auth already depends on; Ed25519) with a key from `.env`, and services verify them with public keys from `.env`. Fetching a JWKS from `dashboard:3000` would make every service depend on the Dashboard at start and on refresh, and would trust whatever answers to that name on the backend network, where most containers keep Docker's default capabilities (only 9 of the 37 services in `compose.yaml` set `cap_drop`), raw sockets (`NET_RAW`) included, which is what spoofing a neighbour needs. Pinned keys need neither. Better Auth's `jwt` plugin stays, but only for the OAuth access tokens of MCP clients, which only the Dashboard itself verifies.

| Setting | Holds | Given to |
| --- | --- | --- |
| `LIFE_OS_AUTH_SECRET` | Better Auth's secret (cookie signing, encrypted TOTP secrets) | the Dashboard |
| `LIFE_OS_IDENTITY_SIGNING_KEY` | the Ed25519 private key that signs identity tokens and delegations | the Dashboard |
| `LIFE_OS_<SERVICE>_SERVICE_KEY` | each service's own Ed25519 private key | that service only (as `LIFE_OS_SERVICE_KEY`), as each gets only its own database password today |
| `LIFE_OS_TRUSTED_KEYS` | the public keys of all of the above, as a compact JWKS | every service and the Dashboard |

`setup.sh` generates them at the first setup and adds missing ones on `update` (`add_new_settings`), like database passwords. **Rotation** is `./setup.sh rotate-keys [auth|<service>|all]`: it adds the new public key to `LIFE_OS_TRUSTED_KEYS` while keeping the old one, recreates every service, then switches the signer; the old key is removed at the next rotation. Tokens live five minutes and assertions one, so the overlap is always enough. Routine rotation is on demand (and after a suspected leak), not scheduled.

**If the Dashboard is down,** nobody can sign in and no new identity token is minted, so the gateway answers every service route `503 {"error":{"code":"auth_unavailable"}}` (an `error_page 500` on the service locations, which without `proxy_intercept_errors` catches only nginx's own 500 for a failed check, never a service's). Services keep running and keep verifying: work that needs no delegation, such as Calendar reminders and Food expiry notices to one person, goes on; work that needs a delegation waits and retries with backoff ([Background work](#background-work)). The Dashboard was already needed for every page, so this adds no new kind of outage, but it does make the Dashboard the one component every API call needs: it gets a memory limit with headroom (512 MiB) and a health check.

### pkg/identity

A new `pkg/identity` Fiber middleware, used by every service through `pkg/httpserver`, replaces `RequireActor`:

- It reads `X-Life-OS-Identity`, checks `alg` is `EdDSA`, finds the key by `kid` in `LIFE_OS_TRUSTED_KEYS`, and checks `iss`, `aud`, `exp`/`nbf` (30 s leeway), and `typ`. Anything else is `401 unauthenticated`. Only `/health` and `/ready` are open.
- It puts a `Principal` in the context: `Kind` (`user`, `delegated`, or `service`), `UserID`, `Role`, `Spaces` (id → role), `PersonalSpace`, `Scopes`, `Modules`, `Limits`, `Act`, and `Service`.
- Routes say who may call them. The default for a public route is users and delegations, with the scope the route's module and method imply (`GET` needs `<module>:read`, other methods `<module>:write`); routes for service mechanics name the services allowed (`identity.Services("processor")`). A service checks `Modules` for its own module (`403 module_not_allowed`).
- The service never caches a principal beyond its request.

## The gateway

Every location that proxies to a Go service, and `/android/`, gets:

```nginx
auth_request /internal/auth/check;
auth_request_set $lifeos_identity $upstream_http_x_life_os_identity;
```

and the server block sets, once, `proxy_set_header X-Life-OS-Identity $lifeos_identity;`, where `$lifeos_identity` is empty unless `auth_request_set` filled it. Setting it at server level matters: nginx inherits `proxy_set_header` only into locations that set none of their own, and the existing header hygiene (`life-os.conf.template:107-120`) must keep applying everywhere. So a client-supplied `X-Life-OS-Identity` never reaches a service. Service locations also drop `Cookie` and `Authorization` (services never need the raw credential), through a `map` on `$uri` feeding the same server-level `proxy_set_header`; the Dashboard's locations and the check keep them.

The internal check location is `internal`, passes no body, and sends the original method and URI (`X-Original-Method`, `X-Original-URI`); since it sets headers of its own, it includes the common header set from a shared file, so the check still gets the client's address. It may answer only 204, 401, or 403 (anything else is a 500 in nginx). The Dashboard serves the check and every other service-to-auth endpoint under `/internal/`, and the gateway answers `location ^~ /internal/ { return 404; }`, so none of them is reachable from outside; they also require a service assertion or come from the gateway's subrequest.

`/api/mcp/` needs one more thing: an MCP client must get `401` with `WWW-Authenticate: Bearer resource_metadata="https://<host>/.well-known/oauth-protected-resource/api/mcp"`. The check returns that header, the location copies it with `auth_request_set` and an `error_page 401` to a named location that adds it.

**Public routes,** without `auth_request` (the Dashboard still applies its own rules):

| Route | Why |
| --- | --- |
| `/login`, `/setup`, `/invite/<token>`, `/reset/<token>`, `/privacy`, `/terms` | signing in, the first account, invites, admin-issued reset links |
| `/api/auth/*` | Better Auth: sign-in, two-factor, passkeys, OAuth authorize, token, and registration; its own location with a strict rate limit |
| `/api/auth/devices/pair` | a phone redeeming a pairing code (rate-limited, single-use, 5 minutes) |
| `/.well-known/oauth-authorization-server`, `/.well-known/openid-configuration` | OAuth metadata, mounted at the site root (Better Auth serves it only server-side, the trial found) |
| `/.well-known/oauth-protected-resource/api/mcp` | the MCP resource metadata (served by the Dashboard, [As built in P21-06](#as-built-in-p21-06)) |
| `/_next/static/*`, the manifest, icons, the service worker | so the installed app and the login page load |

There is no public health route; services' `/health` and `/ready` stay unrouted, and the login page is the liveness probe.

`/android/` is **not** public: it needs a session or a device key. Installs connected to any server but the official one take updates from GitHub Releases, unauthenticated ([Android releases](../operations/android-releases.md#where-the-app-looks-for-updates)); the official server's phones check `/android/updates` with their device key after pairing; and the first download happens in a signed-in browser.

**The Dashboard's own routes.** Pages, `/share`, `/calendar/google/callback`, `/finance/bank/callback`, `/api/status`, `/api/backups/`, and `/api/processor/` stay on the Dashboard's location. Next.js middleware sends a request without a session to `/login?next=…`; API routes answer 401. Server-side rendering calls services with an identity token the Dashboard mints for the current session, never with the cookie. The development proxies (`apps/dashboard/src/lib/service-proxy.ts`, used without the gateway) run the same check and attach the token, as the gateway does.

- `/share` keeps `isShareFromApp` (`apps/dashboard/src/lib/share.ts:76`). The share sheet's POST is a same-origin navigation of the installed app, so the `SameSite=Lax` session cookie comes with it. Without a session it ends on the login page and `/share/about`, as an expired Access session does today.
- The OAuth callbacks are top-level GET navigations from Google or the bank, so the `Lax` cookie is sent (a `Strict` one would not be: this is why `Lax`). The state row of each flow (google-calendar's `google_oauth_states`, banking's `bank_auth_states`) records the user who started it, and the callback is refused for anyone else, so a forged link cannot attach someone's Google account or bank to another person.

## Background work

Schedulers, the processor, the Voice worker, integrations, the monitor, and backups act without a browser. Two mechanisms cover them, and neither trusts the network.

**Service assertions** say "this is service X". Each service signs, with its own key, a token `{"iss":"svc:tasks","aud":"svc:notifications","iat":…,"exp":iat+60,"jti":…}` (`typ: lifeos-svc+jwt`) and sends it in `X-Life-OS-Identity`. The receiver checks the key belongs to `iss`, that `aud` is itself, and that its route policy lets that service call that route. Assertions carry no user and grant no access to anyone's content by themselves. They are for mechanics:

- posting a notification (to a user, a space, or the server admins), recording activity and diagnostics;
- the processor's queue: listing pending Inbox items across users and recording attempts (`identity.Services("processor")` on those routes only);
- the monitor reading the full status, the Dashboard calling the backup service and host-health;
- the auth server's internal API: membership and recipients lookups, delegation requests.

**Delegations** say "service X acts for user U (or space S), with these scopes". A service asks the auth server, with a service assertion: `POST /internal/auth/v1/delegations {"subject":{"user":"<uuid>"},"purpose":"captures"}`. The auth server mints a delegation only if a grant exists for that service, subject, and purpose, and the user is not banned; the result is an ordinary identity token with the user's current spaces (or only space S for a space-bound grant), `via: delegation`, `act: <service actor>`, and the grant's scopes. Services cache a delegation until a minute before it expires.

Grants (`delegation_grants` in `auth_db`: service, purpose, user or space, scopes, created by, created at) are created by the action that needs background work and deleted when it is undone. Settings → Security lists them as "Background access".

| Work | Acts as | Grant created when | Scopes |
| --- | --- | --- | --- |
| Processor: plan and execute a capture | delegation for the capture's user | sign-up, when AI is allowed for the user | the MCP tools' modules, read and write |
| Processor: recipe ideas, budget plans, categories | delegation for the user who turned the job on for that space | turning the job on | `food:*` or `finance:*` of that space |
| Voice worker: put a transcript in Inbox | delegation for the recording's user | sign-up, when Voice is allowed | `inbox:write` |
| Tasks morning summary: read the day's events | delegation for the user | turning the summary on | `calendar:read` |
| Banking: import into Finance | delegation for the user who connected the bank | connecting a bank | `finance:import` |
| google-calendar: sync both ways | delegation for the user who connected Google | connecting Google | `calendar:integration` |
| calendar-feeds: sync a feed (the ZAW one) | space-bound delegation for the feed's space | a space admin adds the feed | `calendar:integration` |
| recipe-import | the caller's own token, forwarded | (a user request) | as the caller |
| Calendar reminders, Food expiry, Finance alerts, task due notices, Banking's sync schedule, Notifications' delivery worker | the service itself, on its own data (system scope) | none | none outside its own database |
| Monitor, backup, host-health | service assertions only | none | none |

So a compromised integration can at most do what its grants' scopes allow, for the users who granted it, and only while the auth server agrees. Anything that creates or changes someone's content in another service carries a delegation, never a bare assertion; a capture in particular is an instruction to the AI, so only the user (or a delegation from them) can create one.

**Telling user calls from system calls.** `Principal.Kind` is `service` for an assertion, `delegated` for a delegation, and `user` otherwise. A route that accepts services names them; a route that accepts users rejects assertions.

**The activity actor** comes from the principal; the `X-Life-OS-Actor` header is retired (the gateway drops it; `dev:` claims become personal tokens marked as development tools, whose actor is `dev:<token name>`). Events keep `actor_kind` and `actor_name` and gain `actor_user_id` (the person the action is for) and `space_id`:

| Principal | actor_kind / actor_name | actor_user_id | Shown as |
| --- | --- | --- | --- |
| user (session, device) | `user` | the user | "Anna" |
| user via token marked dev | `dev` / token name | the user | "claude-code for Anna" |
| user via MCP | `agent` / `mcp` | the user | "AI (MCP) for Anna" |
| delegation | `agent` or `system` / from `act` | the subject | "AI for Anna", "Bank sync for Anna" |
| service assertion | `system` / service | none | "Calendar" |

## Spaces

**The source of truth** is Better Auth's `organization` and `member` tables in `auth_db`. A space is an organization with an extra `kind` column (`personal` or `family`); space roles are the organization roles `owner`, `admin`, `member`. Better Auth's own invitations are not used (they are keyed by email). Better Auth is configured to make UUIDs (`advanced.database.generateId`) so ids match the services' `uuid` columns; P21-03 verifies this, and otherwise wraps generation to produce UUIDs.

- **Personal space**: created with the account (an `after` hook on sign-up, as in the trial; the check creates it if it is missing, so a crash between the two cannot leave an account without one), exactly one per user, one member, never shared, left, or deleted while the account exists.
- **Family**: created by a user whose family-size limit is above zero. Up to 6 members, adults only (stated at sign-up; not technically enforceable), and one family per person: the hook refuses a second family membership with a clear message, and a trigger on `member` refuses it in the database.
- **The first account** gets fixed ids on every server: user `00000000-0000-4000-8000-000000000001`, personal space `00000000-0000-4000-8000-000000000002`. This is what lets the services' migrations give the existing single-user data an owner without reading `auth_db` ([The upgrade path](#the-upgrade-path)); P21-03 verifies Better Auth's create hook can set them.

**How services learn membership:** from the token's `sp` claim, on every request, so they hold no copy. Membership changes take effect on the next request (the `authz_version` bump makes the check re-mint). A token already in flight stays valid at most five minutes; delegations are re-minted at most every five minutes, each time from current membership. Notifications asks the auth server for a space's members when it fans a notification out ([Notifications](#notifications)).

**The merged view.** A request covers every space in the token unless it narrows them: list endpoints accept `space=<uuid>` (repeatable) and use the intersection with `sp`. Every entity in every contract gains `spaceId`. The Dashboard shows "mine + family" together and badges items whose `spaceId` is not `psp`, with a "Mine / Family / All" filter. Create endpoints take an optional `spaceId` (which must be in `sp`), else `dsp[module]`, else `psp`, so the phone, MCP, and the AI behave like the Dashboard without knowing the user's preference.

**Moving items** between spaces is one endpoint per owning service, `POST /v1/<entities>/{id}/move {"spaceId":…}`. The transaction needs both spaces in the caller's set, moves dependent rows with the item (a recipe's ingredients, an event's exceptions, an account's transactions), and records an activity event in both spaces. Personal → family: the user. Family → personal: the item's creator, or a family admin or owner. Where a unique name would collide (a store, a staple, an aisle rule, a category), the target's row wins and references are re-pointed to it; an open shopping item with the same name is merged, its amount and note appended.

**Leaving a family:** the membership goes; the personal space and everything in it go with the person; what they put in the family stays; family Tasks assigned to them become unassigned. A bank-synced account they moved into the family stays there with its history, but syncing it stops, because the consent is theirs and they are no longer in that space ([Families by module](#families-by-module)). A family owner must hand ownership to another member before leaving; the last member leaving deletes the family and its content after an explicit confirmation.

## Row-level security

**Two roles per database.** The existing `<service>_user` stays the owner and keeps running the migrations (`<service>-migrate` in `compose.yaml`). A new login role, `<service>_app` (`NOSUPERUSER NOBYPASSRLS`, owner of nothing), is what the service connects as at runtime. Provisioning (`provision-service-databases.sh`, as the PostgreSQL admin) creates it, grants it `CONNECT` and DML on existing tables and sequences, and sets `ALTER DEFAULT PRIVILEGES FOR ROLE <service>_user IN SCHEMA public` so tables later migrations create are granted too. That would include golang-migrate's `schema_migrations`, so each service's first P21-07 migration revokes the runtime role's rights on it. The registry `service-databases.conf` gains two fields (app role and its password variable), and `setup.sh` generates the passwords.

**Every table has RLS enabled and forced** (`ENABLE` and `FORCE ROW LEVEL SECURITY`). The runtime role is subject to policies either way; forcing also binds the owner, so a tool or a misconfigured service using the migration credentials sees nothing rather than everything. A later migration that must change rows of every space (a backfill) says so explicitly: `ALTER TABLE … NO FORCE ROW LEVEL SECURITY`, the change, `FORCE` again, all in the same migration file. At start each service checks its connection's role owns no table and lacks `BYPASSRLS`, and refuses to run otherwise (`pkg/database`).

**The per-transaction scope.** Each database gets the same two functions in its first P21-07 migration (`tools/check-rls` checks they are identical everywhere):

```sql
CREATE FUNCTION lifeos_space_visible(space uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
  SELECT current_setting('lifeos.scope', true) = 'system'
      OR space = ANY (string_to_array(NULLIF(current_setting('lifeos.space_ids', true), ''), ',')::uuid[])
$$;
CREATE FUNCTION lifeos_user_visible(owner uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
  SELECT current_setting('lifeos.scope', true) = 'system'
      OR owner = NULLIF(current_setting('lifeos.user_id', true), '')::uuid
$$;

ALTER TABLE tasks ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks FORCE ROW LEVEL SECURITY;
CREATE POLICY tasks_by_space ON tasks TO tasks_app
  USING (lifeos_space_visible(space_id)) WITH CHECK (lifeos_space_visible(space_id));
```

Unset settings match nothing: a query outside a scoped transaction returns no rows and an insert fails. `WITH CHECK` stops a row being written into, or moved to, a space outside the caller's set. The owner role has no policy, so under `FORCE` it sees no rows at all. Foreign-key checks are not subject to policies; a trigger that writes a row (Calendar's change log) copies the space of the row that fired it.

**Applying it with pgx and sqlc.** Settings are transaction-local (`set_config(…, true)`), so they can never leak to the next user of a pooled connection; this is the pattern Calendar already uses for its actor (`services/calendar/db/queries/changes.sql:7`, read by the trigger at `services/calendar/db/migrations/000014_integration_api.up.sql:156`). `pkg/database` gains:

```go
type Scope struct { Spaces []uuid.UUID; User uuid.UUID; Actor string }
func ScopeOf(p identity.Principal, narrow []uuid.UUID) Scope   // sp ∩ ?space=
func InScope(ctx context.Context, pool *pgxpool.Pool, s Scope, fn func(pgx.Tx) error) error
func SystemTx(ctx context.Context, pool *pgxpool.Pool, reason string, fn func(pgx.Tx) error) error
```

`InScope` begins a transaction, sets `lifeos.space_ids`, `lifeos.user_id`, and `lifeos.actor` in one statement, runs `fn` with `repository.New(tx)`, and commits. Handlers stop using `repository.New(pool)`; a test per service fails if a handler package refers to the pool. The extra statement is one round trip per request.

**System scope** is for a service's own schedulers reading across spaces: Calendar finding due reminders, Food the day's expiring items, Finance its alerts, the bank sync schedule. `database.SystemTx` is the only code that sets `lifeos.scope = 'system'`, and `tools/check-rls` fails on any call site not listed, with a reason, in `tools/rls-system-allowlist`. A scheduler reads across spaces, then does each space's work in that space's scope.

**Checks.** `tools/check-rls` runs in the integration job (`tools/test-integration`), on databases its migrations have just built, and fails when a table in `public`:

- lacks `relrowsecurity` or `relforcerowsecurity`;
- has no policy for the runtime role;
- has neither `space_id uuid NOT NULL` nor `user_id uuid NOT NULL` used by its policy;
- or the runtime role owns a table, has `BYPASSRLS`, or can reach `schema_migrations`.

A table that is server-wide on purpose (a dispatcher's cursor, `schema_migrations`) is listed in the service's `db/rls-exempt` with a reason; an exempt table may hold no user content. `tools/check` adds a quick static rule before CI's database job: a migration that creates a table must also force RLS on it or exempt it.

**Backups.** `lifeos_backup` reads through `pg_read_all_data` (`infra/postgres/provision-backup-role.sh:81`) and is `NOBYPASSRLS` (line 54). With RLS, `pg_dump` would refuse to dump policy-filtered tables (it sets `row_security = off` and errors rather than dump part of a table), and the backup's row counts would be zero, so the backup role becomes `BYPASSRLS`. It stays read-only: `BYPASSRLS` grants no privilege, only skips policies. Restores already run as the PostgreSQL admin (`tools/restore-database`), which policies do not bind. `auth_db` is backed up like the rest, but its TOTP secrets and signed cookies need `LIFE_OS_AUTH_SECRET`, so the `.env` must be kept with the backups ([Backups runbook](../operations/backups.md) gains this).

**auth_db** has no RLS: it is the identity store, only the Dashboard connects to it, and Better Auth reads across users by design. `tools/check-rls` exempts the whole database.

## Per-table plan

"Space" means a `space_id` column with the space policy; "user" a `user_id` column with the user policy; "system" an exempt, content-free table. Every idempotency and dedupe key gains the space or user in front of it, so one person's keys can neither collide with nor reveal another's.

| Service | Tables | Owner | Keys and singletons |
| --- | --- | --- | --- |
| finance | `accounts` | space | `UNIQUE(name)` → `(space_id, name)`; Cash `system_key` → `(space_id, system_key)`: one Cash account per space |
| finance | `transactions`, `low_balance_states`, `subscription_transactions` | space (its account's, enforced by trigger) | import identity and automation idempotency keys → with `space_id` |
| finance | `financial_categories`, `category_rules`, `finance_seeds` | space | `(name, kind)` and category `system_key` → per space; a new space gets the default categories |
| finance | `budget_plans`, `budget_plan_lines`, `ai_budget_plans` | space | `(month, scope)` → `(space_id, month, scope)`; one scheduled AI plan per `(space_id, month)` |
| finance | `subscriptions` | space | `(merchant_key, match_cadence)` → with `space_id` |
| finance | `finance_settings` (singleton) | space | one row per space (alert thresholds) |
| finance | `payday_settings` (singleton) | user | one row per user |
| finance | `alert_notifications`, `budget_notifications` | space | dedupe keys per space |
| tasks | `tasks` | space | + `created_by`, `assignee_user_id`; automation key → with `space_id` |
| tasks | `task_notification_dispatch` (singleton) | user | one row per user (their summary state) |
| tasks | `task_notification_log` | user | dedupe per recipient |
| calendar | `events`, `event_exceptions`, `calendar_event_changes` | space | automation and external identity keys → with `space_id`; integrations read the change feed of their spaces |
| calendar | `calendar_sources` | space | `(integration, external_id)` and `preset` → with `space_id`; + `connected_by` for a person's integration |
| calendar | `calendar_settings` (singleton) | user | default reminders per user |
| calendar | `calendar_reminder_log` | user | dedupe per recipient |
| calendar | `calendar_reminder_dispatch`, `calendar_change_feed` (singletons) | system | cursors, stay server-wide |
| calendar-feeds | `feeds` | space | `preset` → `(space_id, preset)`: one ZAW feed per family |
| google-calendar | `google_connections` (singleton) | user | `singleton` → `UNIQUE(user_id)`: one Google account per person |
| google-calendar | `google_calendars`, `google_events`, `google_exports`, `google_sent_events`, `google_source_removals`, `google_pushes`, `google_rekeys`, `google_oauth_states` | user | + `user_id`; each synced calendar names its target space (personal by default) |
| banking | `bank_connections`, `bank_auth_states`, `bank_consent_failures` | user | live-institution index → `(user_id, provider, institution, country)` |
| food | `stores`, `aisle_rules`, `staples`, `recipes`, `recipe_ingredients`, `meal_plan_entries`, `shopping_items`, `pantry_items`, `food_ideas` | space | every name, keyword, and title index and `(day, slot)` → with `space_id` |
| food | `food_preferences` (singleton) | space | one row per space |
| food | `food_notification_dispatch` (singleton) | system | cursor |
| food | `food_notification_log` | space | dedupe per space |
| food | `idempotency_keys` | user | key → `(user_id, key)` |
| inbox | `inbox_items`, `inbox_processing_attempts` | space (personal) + `user_id` | `source_dedupe_key` → `(user_id, key)` |
| inbox | `inbox_media` | space | `sha256` → `(space_id, sha256)`; files move to `<space_id>/<sha256>` on the volume, so identical photos of two people are two files and a space can be shredded alone |
| voice | `audio_jobs` | space (personal) + `user_id` | idempotency key → `(user_id, key)`; audio under `<space_id>/` |
| activity | `activity_events` | space | + `space_id`, `actor_user_id` |
| activity | `diagnostic_events` (new) | system | content-free by type ([Logs](#logs)) |
| notifications | `notifications`, `deliveries`, `push_outcomes` | user (recipient) | one row per recipient; `dedupe_key` → `(recipient_user_id, dedupe_key)` |
| notifications | `push_subscriptions` | user | + `user_id`, `device_key_id`; `endpoint` stays unique (subscribing again moves it to the current user) |
| auth (new) | Better Auth's tables; `setup_codes`, `invites`, `pairing_codes`, `delegation_grants`, `security_events`, `server_settings`, `limit_defaults`, `user_limits`, `user_profiles` | exempt | the one server-wide `owner`: a partial unique index on `"user"(role)` where `role = 'owner'` |
| ai (new, P21-11) | `ai_profiles`, `ai_keys`, `ai_usage`, `ai_schedules` | user | the processor's own `ai_db` ([AI](#ai-per-user)) |

### Notifications

`pkg/notify` gains a recipient: `User(id)`, `Space(id)`, or `ServerAdmins()`. Notifications resolves a space or the admins to users with the auth server's internal recipients API at the time it stores the notification, and stores one row per recipient, so read state is per person and someone who joins later gets nothing old. If the auth server is unreachable, the notification waits and fan-out retries, as deliveries already do. Who is told:

- Personal space items: its user. Banking consent and sync problems: the user who connected the bank. Google problems: the user who connected Google.
- Family Tasks: the assignee; unassigned ones go to every member. Family Calendar reminders and the ZAW reminder: every member, each of whom can mute family reminders. Family Food expiry and family Finance alerts: every member.
- Monitor alerts and server diagnostics: server admins.

Web push goes to the recipient's own subscriptions only. ntfy, which has one topic, carries only server-admin alerts until per-person topics are designed.

## Accounts

**Registration** is a server setting in `auth_db` (`server_settings`):

| Setting | Default | Meaning |
| --- | --- | --- |
| `registration` | `invite` | `invite`: an account needs an invite link; `open`: anyone may sign up (later, for a hosted version) |
| `space_admins_may_invite` | `false` | family owners and admins may create invite links that also create accounts; otherwise only server admins can |

A `before` hook on Better Auth's sign-up enforces it, as the trial did.

**The first account** needs a one-time **setup code**. `./setup.sh setup-code` runs a small command inside the Dashboard's container that stores the code's SHA-256 with a 24-hour expiry in `setup_codes` and prints the code; `setup.sh` runs it at the end of the first setup, and at the end of an `update` that finds no account. `/setup` accepts it only while no account exists. Whoever has the host has the server: that is the trust root, and also the way back in ([Recovery](#recovery-without-email)). The first account becomes the server owner and gets the fixed ids above.

**Invite links** are our own table (`invites`: SHA-256 of a 32-byte token, kind `account` or `space`, `space_id` and `space_role`, `created_by`, `expires_at` (default 72 h, at most 7 days), `used_at`, `used_by`, `revoked_at`). One link, `/invite/<token>`, both creates the account and joins the family; opened by someone already signed in, it only joins (and the one-family rule applies). Links are single-use and revocable; whoever made one sees whether it was used. Creating one checks the family's size: members plus open invites may not exceed the family owner's family-size limit (6 at most).

**Usernames and email.** People sign in with a username (Better Auth's `username` plugin, case-insensitive). Email is optional, but Better Auth requires one, so an account without one stores `u-<random>@users.invalid` (`.invalid` is reserved and never delivers); a real address, if given, replaces it and is informational only. Nothing keyed by email is enabled: no email sign-in, magic links, email verification, or Better Auth invitations. With no SMTP, admins copy invite and reset links from the admin panel and hand them over themselves.

**Sign-in**: password, passkeys (WebAuthn, relying party = the public hostname, or `localhost`), TOTP with ten recovery codes, all as the trial ran them. A passkey sign-in counts as two-factor. Server owners and admins must have TOTP or a passkey before the admin surfaces open. Sessions last 30 days, renewed daily while used; sensitive actions (changing roles, deleting a user, transferring ownership, setting the server AI key) need a session younger than 12 hours, else a fresh sign-in.

### Recovery without email

- Forgotten password, TOTP still at hand: a server admin creates a one-time reset link (24 h) in the admin panel and hands it over; the security log records it.
- Lost TOTP: a recovery code; with none left, an admin resets two-factor (a small custom endpoint; Better Auth has none, the trial found). The user is told at the next sign-in.
- The owner locked out: `./setup.sh recover-owner` on the host prints a one-time reset link for the owner, and with `--reset-2fa` also removes their two-factor.

### Sign everyone out

For an emergency (a leaked database copy, a stolen laptop, anything that looks like someone else got in), the server owner has **Admin → Security → Sign everyone out**, and the host has `./setup.sh sign-out-all [--keep-devices]` for when the admin panel itself cannot be trusted or reached. Both do, in one transaction, what "Sign out everywhere" does for one user, for every account: delete every session, API key (device keys too, unless `--keep-devices`), personal API token, OAuth refresh token, and consent, revoke open invite links and pairing codes, and bump every `authz_version`, so it takes effect on the next request. The owner is signed out too and signs in again with their password and two-factor. Background-access grants stay (they are permissions, not credentials); `./setup.sh rotate-keys all` is the separate step when the signing or service keys may have leaked. The security log records who ran it. While Cloudflare Access is in front, its own sessions are ended separately ([Cloudflare → Sign everyone out](../operations/cloudflare.md#sign-everyone-out)).

## Devices, tokens, MCP

**Pairing a phone or watch.** Settings → Devices → Pair shows a QR code for `lifeos://pair?host=<public URL>&code=<code>`, a single-use code valid 5 minutes (`pairing_codes`, stored hashed). The app posts the code and a device name to `/api/auth/devices/pair` and receives its own API key once. A phone with no second screen opens `/login?pair=1` in a Custom Tab instead; after signing in, the page creates a code and returns to the app with it. The phone may create one child key for its paired watch (`POST /api/auth/devices/child`), listed and revoked separately. Every key shows its name, kind, and last use; revoking one also deletes its push subscriptions (`device_key_id`).

The trial found two holes, and the design closes both:

- A banned user's API key still produced a session. The check tests `user.banned` (and the `authz_version`) for every credential, not only sessions.
- Revoking all sessions left API keys working. The admin action "Sign out everywhere" deletes the user's sessions, API keys, OAuth refresh tokens, and OAuth consents together, in one transaction; banning does the same, and the auth server mints no delegation for a banned user.

**Personal API tokens** (Settings → API tokens): a name, scopes (`<module>:read` or `:write`), an expiry (90 days by default, at most a year), shown once, prefixed `lo_tok_`. A token can be marked as a development tool (its actor is then `dev:<name>`). Tokens cannot manage the account, devices, tokens, or anything under `/api/auth/` but their own sign-out.

**MCP clients** use OAuth (Better Auth's OAuth provider plugin, as the trial ran it): discovery from `/.well-known/oauth-protected-resource/api/mcp` (served by the MCP service from its configuration), authorization server metadata at the site root, dynamic client registration, the consent page in the Dashboard, PKCE, and JWT access tokens bound to the resource `<public URL>/api/mcp` (15 minutes) with rotating refresh tokens. Loopback redirect URIs are allowed (`allowInsecureRedirectUri` for `127.0.0.1`, `localhost`, `[::1]` only), and the MCP resource is declared statically. Consent offers two scopes, `mcp:read` and `mcp:write`, over the modules the user is allowed. The check verifies the access token, looks up the grant (so revoking it in Settings works at once), and mints the identity token with `via: mcp`. The MCP service lists only the tools of modules the user is allowed and forwards that token to the domain services with each tool call; client-ID metadata documents (`@better-auth/cimd`) are left out until they are tested.

**Cloudflare Access** becomes an optional outer layer. The gateway's rule that tunnel traffic must carry Access's token (`life-os.conf.template:29-32`) becomes a setting, `LIFE_OS_REQUIRE_CLOUDFLARE_ACCESS`, on wherever the `cloudflare` module is (as today); turning it off makes Life OS's login the only gate on the tunnel. With Access on, every family member must also be allowed by the Access policy, and the apps send the Access service token as today besides their device key.

**The Android app** (`GAMch1k/life-os-app`, separate tickets) needs: the pairing screen (QR and Custom Tab), the device key in Android Keystore-backed storage, `Authorization: Bearer` on every native call (voice upload, push registration, update checks), the Access headers kept when configured, the TWA signing in through the web login (its cookie is Chrome's), a child key for the watch, re-pairing on 401, a clear message on `403 module_not_allowed`, and registering push again after pairing so the subscription gets its owner. One account per installed app.

## Browser security

- **Cookies**: `__Host-lifeos.session`, `HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`, no `Domain`. A browser is a secure context on HTTPS and on `localhost`/`127.0.0.1`, so the public hostname and the default loopback bind both work. A WireGuard address over plain HTTP (`http://10.8.0.1:3000`) is not: there the login page says to use the HTTPS name, unless the owner lists that host in `LIFE_OS_INSECURE_LOGIN_HOSTS`, which allows password and TOTP sign-in with a non-`Secure` host-only cookie and no passkeys (WebAuthn needs a secure context). P21-03 verifies Better Auth can set its cookie per request this way; if it cannot, plain-HTTP hosts get no login and WireGuard users use the public name.
- **CSRF**: a write authenticated by the cookie must come with `Origin` equal to one of the gateway's own origins, or `Sec-Fetch-Site: same-origin`; the auth check enforces it for service routes (it has the original method and headers) and the Dashboard for its own routes, as Better Auth does on `/api/auth/*`. Requests authenticated by `Authorization` carry no ambient credential and are exempt. `SameSite=Lax` alone would not stop a sibling subdomain such as the ntfy hostname.
- **CORS**: removed. `pkg/httpserver` stops sending `Access-Control-Allow-Origin: *`, and nothing replaces it: the Dashboard is same-origin, development uses its proxies, and native apps do not need CORS.
- **Login rate limits**: a gateway zone for `/api/auth/` (per client address, which the gateway already derives correctly on the tunnel), Better Auth's in-memory rate limit per address and path, and a per-username delay that grows after failures (to 15 minutes), so guessing is slow without letting someone lock another person out. Failures go to the security log.
- **Password hashing off the event loop**: Better Auth's hashing is pure-JS scrypt, which blocked the event loop about 110 ms per sign-in in the trial (111 ms per login, 239 ms with ten at once). The Dashboard gives Better Auth `password.hash`/`verify` functions on `node:crypto`'s asynchronous `scrypt` (libuv's thread pool) with Better Auth's parameters and format, so hashes stay compatible. Each hash needs 32 MiB (`maxmem` raised to 64 MiB), and at most two run at once.
- **Supply chain**: exact versions of Better Auth and its plugins (21 advisories in a year, breaking changes between 1.x minors), only the plugins this page names (`username`, `admin` with impersonation removed from every role, `organization`, `twoFactor`, `passkey`, `api-key`, `jwt`, OAuth provider), and the trial's flow test in CI.
- **Schema**: Better Auth's schema is generated once and committed as our own SQL migrations, applied by an `auth-migrate` container like every other database; Better Auth never migrates at start (it refuses to start on a mismatch, which stays a guard). CI compares Better Auth's expected schema with ours, so an upgrade that changes it fails until a migration is written.

## Roles and checks

Server roles (owner, admin, member) and space roles (owner, admin, member) are independent: a server admin has no access to a family's content unless they are in it, and a family owner has no server powers. **Admins manage accounts, never content**, and nobody can impersonate anyone (Better Auth's impersonation is in no role; the trial's attempt got 403). That said honestly: whoever controls the machine (root, the `docker` group, PostgreSQL's admin, the `.env`) can read every database and every backup. The app keeps admins out of content; it cannot keep the host's operator out. Encryption ([P21-14](#encryption-p21-14)) narrows what a stolen backup reveals, not what the host operator can do.

| Check | Where |
| --- | --- |
| Signed in at all (and not banned, credential not revoked) | the auth check behind the gateway; Next.js middleware for pages and the Dashboard's routes |
| Cookie writes come from Life OS's own origin | the auth check; the Dashboard's routes |
| Module installed | the gateway (`404 module_off`, as today) |
| Module allowed for the user | services and MCP (`mods`, `403 module_not_allowed`); the Dashboard hides it |
| Module hidden by the user | the Dashboard only (a preference, not a permission) |
| Token, MCP, and delegation scopes | `pkg/identity` on each route |
| Which rows | PostgreSQL RLS, from the scope the service sets |
| Space role (family settings, the ZAW feed, removing members, moving items out) | the owning service from `sp`; membership changes in the auth server |
| Server role | the Dashboard for admin pages and actions; services' admin routes (usage, diagnostics) from `role` |
| Limits | the service that meters each, before use ([Limits](#limits-and-metering)) |

**Admin-only surfaces**: Status details, host health, backups and Back up now, modules, version and updates, users and invites, limits, server settings (registration, the server AI key, AI provider), server-wide integrations (Google's OAuth client, Enable Banking's application, web push keys, ntfy), the full security log, diagnostics, and the AI subscription's usage. Members see Status as one line for the modules they have: "Everything works" or "Calendar has a problem", without detail. The monitor reads the full status with a service assertion (`/internal/status`), and the backup service accepts only the Dashboard's assertion, which the Dashboard sends after checking the admin and with the admin as the activity actor.

**The server owner** is exactly one (the partial unique index), can transfer ownership to an admin in one transaction, and alone can make or unmake admins. Admins create and disable members, issue reset links, reset two-factor, set limits, and see the security log.

## Families by module

- **Food** is shared. Fridge, pantry, shopping list, recipes, meal plan, stores, aisle rules, staples, ideas, and preferences live in the family space. Creating a family offers to move your Food into it (Food's `POST /v1/spaces/move-all`, with the merge rules of [Moving items](#spaces)), and sets `dsp.food` to the family.
- **Tasks**: personal Tasks and one family list, whose Tasks may be assigned to a member.
- **Calendar**: personal events and the family calendar. The ZAW feed belongs to the family; creating a family offers to move it there. Each Google calendar a person syncs names its target space, personal by default.
- **Finance and banking**: private by default. An account, such as a joint one, can be moved into the family on purpose, with its transactions; their categories are re-pointed to the family's categories of the same name (created if missing). Family budgets are the family space's. The bank consent stays with the person who connected it: only they renew or remove it, and Banking's rows are theirs (`user_id`). Banking imports with their delegation, and Finance accepts an import only into accounts in spaces the delegation covers; when the person leaves the family, imports into the family's account are refused, Banking marks it "not synced", tells them, and the family keeps the history.
- **Inbox and Voice** are private. The AI may still put "buy milk" on the family list ([AI](#ai-per-user)).
- **Notifications** are personal ([Notifications](#notifications)).

## AI per user

The processor gets its own database, `ai_db` (role `ai_user`, part of the `ai` module), for what is now per person: profiles, keys, usage, and schedules. It stays one process with one run at a time (`LIFE_OS_CODEX_MAX_CONCURRENCY`), because of the host's memory.

- **Fairness.** Captures (someone waiting) come before background jobs (ideas, budget plans, categories). Among captures, the processor asks Inbox for the oldest pending item of each user and serves the user it served least recently, so one person's burst of 30 photos cannot hold everyone else up behind it. Background jobs run only while no capture waits, staggered across spaces.
- **Context per person.** The fixed facts about the owner leave the prompts (`codex.go:22`, `budget_plans.go:598`, `categories.go:469`) for an AI profile per user: name, country (the letter rules are about German paperwork; for another country they are general), languages (the "other two languages" of `taskQueryAlternatives` become the user's), and a short household note. Family members' names, for assignment, come from the auth server's members API under the delegation. Account #1's profile is migrated as "Germany; English, Russian, German", so the owner's results do not change. Recent captures (`recent.go`) are the capture's user's only.
- **Whose rights.** A capture is executed with a delegation for its user, through MCP, with the tools' scopes; RLS bounds it to that user's spaces.
- **Cross-space routing.** The capture and its Inbox item stay personal. Food actions go to `dsp.food` (the family list when Food was moved there); Tasks and events go to the personal space unless the capture says it is for the family ("for us", "family", a member's name as assignee); cash spent goes to the personal Cash account; a family Finance account only when named. When the space is unclear, Human Review asks. Family members see the resulting item and its activity ("AI for Anna"), never the capture's text: its event is in Anna's personal space.
- **Which account pays**, in order: the user's own key for the server's provider, if they set one; else a server API key an admin set in the admin panel; else the owner's subscription (the CLI's existing sign-in), if the admin allows it for that user, which is the beta's default for everyone. A failing key never silently falls back to one someone else pays for: the capture waits with the reason ("Your API key was refused"). The provider (`LIFE_OS_AI_PROVIDER`, Codex or Claude) stays server-wide for now. Each run gets only its key, in a per-run environment and home directory, as `ClaudeCLI` already takes an API key (`services/mcp/cmd/processor/main.go:133`).
- **Key storage.** `ai_keys` holds keys sealed with AES-256-GCM under `LIFE_OS_AI_KEY_SEAL` (as Banking seals its session with `LIFE_OS_BANKING_TOKEN_KEY`), shows only the last four characters, and is replaced by per-space envelope keys in P21-14. Admins never see a user's key.
- **Background AI for a family** (the night's recipe ideas, a family budget plan) counts against, and runs with the delegation of, the member who turned it on (`ai_schedules`).

## Limits and metering

Limits are set by admins: server defaults (`limit_defaults`) and per-user exceptions (`user_limits`) in `auth_db`. A self-hosted server is unlimited by default, except families, which have at most 6 members. The finite ones travel in the token (`lim`), and each metered service counts its own usage in its own database and checks before the work starts.

| Limit | Counted by | Recorded in | Checked | When reached |
| --- | --- | --- | --- | --- |
| AI requests per month | processor | `ai_db.ai_usage`: one row per run (user, kind, provider, key source, tokens); retries after a provider failure do not count | before each run | the capture waits, "AI limit reached for this month" |
| Voice minutes per month | voice | `audio_jobs`: the recording's duration, per user | at upload, against the minutes used so far | `429 voice_limit_reached` |
| File storage (exact bytes) | inbox (later documents) | a per-user total updated in the same transaction as `inbox_media` rows | at upload | `413 storage_limit_reached` |
| Bank connections | banking | live `bank_connections` per user | when connecting | `409 bank_limit_reached` |
| Allowed modules | auth server | `user_limits` | every request (`mods`) | `403 module_not_allowed` |
| Family size | auth server | members plus open invites | creating and accepting an invite | the invite is refused |
| Database size per space | each service | not stored | never | only shown, approximately |

Each metered service has `GET /v1/usage/me` (the user's own numbers) and `GET /v1/usage?user=` for admins (numbers only, no content). Settings → Usage shows a bar for each finite limit and a notice at 80%. The admin panel's approximate size per space is each service's row count per space times the table's average row size from `pg_class`; it is never enforced.

## Logs

Three logs, always on, with no "send to admin" button:

- **Activity** (the Event Log) is user data: every event has a `space_id`, and reads are filtered by the caller's spaces like everything else. You see your spaces' activity; admins see only their own spaces'.
- **Server diagnostics** are admin-only and content-free: `diagnostic_events` in `activity_db` (service, an error code from a fixed list, ids, counts, durations, time). The Go type has no free-text field, so content cannot be written by accident; a test plants marker titles, amounts, and texts in a scenario and asserts none reaches diagnostics or their API, as Banking's `TestSecretsNeverLeave` (`services/banking/internal/banking/integration_test.go:637`) does for secrets.
- **Security** in `auth_db` (`security_events`): sign-ins, failed sign-ins, two-factor and passkey changes, setup code use, invites made and used, pairing and revoking devices, tokens, role changes, bans, resets, ownership transfer, limit and server-setting changes, delegation grants. Each records who did it, whom it concerned, the address and a short device label, and the outcome. Admins see all of it; each user sees what concerns them. Kept for one year.

## Settings

| Setting | Today | Level | Lives in |
| --- | --- | --- | --- |
| Timezone, language | `LIFE_OS_TIMEZONE`, the browser | user (the server's value is the default for new accounts) | `user_profiles`; in the token (`tz`, `lng`); services keep the last value they saw per user for their schedulers |
| Theme, hidden modules, where new items go per module | `localStorage`, none | user | `user_profiles` (`localStorage` as a cache) |
| Summary, morning, evening times, lead minutes | `LIFE_OS_TASKS_*` | user | tasks |
| Default reminders | `calendar_settings` | user | calendar |
| Payday | `payday_settings` | user | finance |
| Food expiry time | `LIFE_OS_FOOD_EXPIRY_TIME` | user | food |
| Notification devices, muting family reminders | none | user | notifications |
| AI profile, own AI key | the prompts | user | `ai_db` |
| Finance currency, alert thresholds, AI categories, monthly AI budget plan | `LIFE_OS_FINANCE_CURRENCY`, `finance_settings`, `LIFE_OS_AI_CATEGORIES`, `LIFE_OS_AI_BUDGET_PLAN*` | space | finance |
| Food preferences, recipe ideas time and web search | `food_preferences`, `LIFE_OS_IDEAS_*` | space | food, `ai_db` |
| ZAW and other feeds | `LIFE_OS_ZAW_ICS_URL`, `feeds` | space (the family) | calendar-feeds |
| Family name, members | none | space | `auth_db` |
| Registration, invites by space admins, limits, server AI key, AI provider | `.env` (the provider) | server | `auth_db`, `ai_db` |
| Modules installed, public URL, bind, secrets, backups, bank sync times, Inbox context minutes, host-health thresholds, Google and Enable Banking applications, web push, ntfy | `.env` | server | `.env`, unchanged |

At the migration the `.env` values become account #1's settings and the server's defaults, so nothing changes for the owner. Schedulers in each service work in each user's own timezone.

## Encryption (P21-14)

Later, and outlined only so earlier tickets leave room for it: each space gets a data key, wrapped by a master key kept outside the database and outside backups (envelope encryption). It encrypts bank sessions and Google refresh tokens (today sealed by one server key each), AI keys, Inbox photos, voice recordings, and documents. Off-site backups are encrypted (Cloudflare R2, P14-04). Deleting a space destroys its key, which also makes the copies in old backups unreadable (crypto-shredding). P21-07's per-space media directories are the part needed now.

## Rollout

| Ticket | Delivers | Visible to the owner |
| --- | --- | --- |
| P21-03 | `auth_db`, Better Auth in the Dashboard, setup code, sign-in, roles, the gateway's `auth_request`, `pkg/identity` accepting tokens, CORS removed, Origin checks, legacy access | a login; everything else as before |
| P21-04 | admin-only surfaces; the members' one-line Status | admin pages need a signed-in admin |
| P21-05 | security log, diagnostics, `actor_user_id` | the security log |
| P21-06 | device pairing and keys, personal tokens, MCP OAuth; the Android app's changes; the `mfa` claim, which services' admin routes (diagnostics, the delivery health behind Status) then require; whether a paired device of an admin sees Status's details (no: the owner's decision, the phone's admin views come through its web sign-in) | phones paired; legacy access can be turned off |
| P21-07 | service keys and delegations, `space_id` everywhere, runtime roles, forced RLS, the data migration, Notifications recipients | nothing (the point) |
| P21-08 | per-user settings out of `.env` | Settings page |
| P21-09 | families, invites into a family, shared Food | families |
| P21-10 | family calendar, family Tasks with assignment, shared bank accounts | the family calendar and list |
| P21-11 | AI per user: `ai_db`, profiles, keys, fairness, routing | AI profile and own key in Settings |
| P21-12 | limits and metering | Settings → Usage |
| P21-13 | the admin panel completed | users, invites, limits, logs in one place |
| P21-14 | encryption | nothing, except encrypted off-site backups |

Second accounts open only after P21-07 and with legacy access off: until then invites are refused. So the data migration always meets a single-user server.

**Two enforcement steps in services.** From P21-03, services verify `X-Life-OS-Identity` when present and take the actor from it; a request without one is still accepted, because only an internal caller can reach a service without one (the gateway authenticates every service route or refuses it). From P21-07, every request needs a verified principal, internal callers sign assertions or carry delegations, and RLS is on.

### Legacy access

An upgraded server must not lock its owner out or break the phones. While **legacy access** is on, the auth check answers a request with no credential with the first account's identity (`via: legacy`, recorded as such in the security log), which is exactly today's trust model; a request with a credential is that credential's. Legacy access covers what Life OS does today; the pages that are new with accounts (users, invites, server settings, devices, tokens) need a real sign-in. It is on after the upgrade of an existing install and off on a new one. The owner turns it off in Settings → Security once their devices are paired; the Dashboard reminds them daily while it is on and an account exists. Inviting anyone requires it off. Turning it back on is the host's alone (`./setup.sh legacy-access on`): it turns sign-in off for the whole server, which no browser session may do, however old or stolen; it is refused while more than one account exists.

### The upgrade path

For the live server and for preview installs (`v0.1.0-preview.6` and later) alike, through `./setup.sh update`, which backs up every database first (`cmd_update`):

1. `add_new_settings` generates the new secrets and keys, and the app roles' passwords; provisioning creates `auth_db`, the app roles, and their grants; `lifeos_backup` becomes `BYPASSRLS`.
2. Each service's migration adds `space_id` (and `user_id`, `created_by` where the plan says), fills it with the first account's fixed ids, makes it `NOT NULL` without a default, rebuilds the unique indexes with the space in front, turns the singletons into per-space or per-user rows, and forces RLS last, all in one transaction per service. Activity events get the first space and user; notifications and push subscriptions the first user. Inbox moves its media files into the first space's directory at start, idempotently (the backup's copy of the media and `tools/restore-media` learn the layout).
3. `update` prints a setup code when no account exists. Until the owner uses it, legacy access keeps everything working, phones included; afterwards the owner's account owns all the data under the ids it already carries.
4. The first account gets, with the account, the delegation grants for everything that runs today: captures, voice, the Tasks summary, the bank and Google connections, the ZAW feed. While legacy access is on and no account exists yet, the auth server mints delegations for the first account's fixed id without a grant, which is today's trust model.

Each step is checked by row counts: every table has as many rows after as before, all in the first space.

**Rollback.** Before P21-07 a rollback is `./setup.sh update <previous version>`: `auth_db` stays unused and the services' changes are additive. From P21-07 the down migrations exist and drop the columns, which is lossless only on a single-account server; `setup.sh` refuses to go below the P21-07 schema when `auth_db` has more than one account, and the way back is the backup `update` took (`tools/restore-database`). Locked out: `./setup.sh recover-owner`; legacy access on a single-account server is the last resort.

**Rehearsal.** Every step is rehearsed on staging (P20-09) from a preview install with fixture data. The real data is rehearsed once, before P21-07 reaches the live server, in a sealed throwaway stack: the latest backup restored into a separate Compose project with no tunnel and no outbound network, the update run, the checks above, then the stack, its volumes, and the copied dumps deleted. It runs alone on the host (7.7 GiB, shared): no Whisper, no AI, nothing else heavy at the same time.

### Tests and CI

- **The auth flow suite**: the trial's 45 checks (`test.mjs`) become a Dashboard integration test against a throwaway PostgreSQL in CI, plus the holes it found (a banned user's device key, "sign out everywhere" killing keys), CSRF and Origin, rate limits, setup code and invite expiry, the one-family rule, legacy access on and off.
- **RLS**: `tools/check-rls` (the catalog check above) in the integration job; in every service's integration tests, a two-space fixture where B reading, changing, moving, or deleting A's rows finds nothing; one browser test with two users and a family on the full stack.
- **The gateway**: `tools/test-gateway-auth`, in throwaway containers like `tools/test-android-gateway`: every service location requires the check, a client's `X-Life-OS-Identity` is dropped, public routes stay public, `/internal/` is 404, MCP's 401 carries `WWW-Authenticate`. `tools/check-gateway-hosts` gains a static rule: every `proxy_pass` to a service has `auth_request`.
- **`pkg/identity`**: expired, not yet valid, wrong audience, `alg: none`, unknown `kid`, an assertion replayed to another service, a rotation with two keys.
- **Upgrade**: `tools/test-upgrade` starts the previous release's images on the verification stack with fixtures, updates to the build, and checks row counts, legacy access, the setup code, and a login.
- **Content-free diagnostics**, as in [Logs](#logs).

### As built in P21-03

P21-03 follows this page, with these differences and details, each for the reason given:

- **Better Auth 1.7.22**, pinned exactly with its plugins, and `@better-auth/core` pinned by an npm override (the passkey plugin's peer range would otherwise pull 1.7.23's core next to 1.7.22's). 1.7.23 fixes no advisory against 1.7.22 (only MCP and OAuth-provider bugs), so the trial's version stayed.
- **No `organization` plugin yet.** Spaces come with P21-07; until then the check computes `sp` and `psp`: the first account owns its personal space `…0002`, by the fixed id, and no other account has one. No other account can exist yet: sign-up needs the setup code while no account exists, and invites, Better Auth's `admin/create-user`, and email sign-in are off.
- **Identity tokens are signed with `node:crypto`** (Ed25519, compact JWS), not `jose`: the same format with no extra dependency. `pkg/identity` verifies a token the Dashboard minted (a fixture) to prove both sides agree.
- **The check is a Next.js route handler** (`/internal/auth/check`), not a second listener; its latency was measured to decide (see the P21-03 ticket). The Dashboard's own pages and routes run the same check in Next.js's proxy (`src/proxy.ts`, Node runtime), which also renews the session cookie once a day; the gateway's check only reads.
- **Legacy access before any account** mints a token for the first account's fixed ids, as it will once the account exists, rather than sending none. **Under legacy access the activity actor still comes from `X-Life-OS-Actor`** (the gateway still lets `user` and `dev:<name>` through), so development tools keep their `dev:` attribution until personal tokens exist (P21-06); a signed-in session's actor is `user` whatever the header says.
- **`Authorization` headers are no credential until P21-06**: the check ignores them (a request with one and no cookie is legacy access, or 401), so nothing that sends one today breaks. Better Auth's `bearer` plugin is off. (Since P21-06 `Authorization: Bearer` is a credential; other schemes are still ignored.)
- **The per-username delay** after failed sign-ins (5 free, then 15 s doubling to 15 minutes) is kept per username *and* address, in memory, so that a stranger cannot lock the person out from elsewhere; Better Auth's own limit (3 sign-ins per 10 s per address) and the gateway's `lifeos_auth` zone (2 r/s, burst 20) bound guessing from many addresses.
- **Every browser write to `/api/auth/` needs Life OS's own origin**, with or without a cookie (Better Auth checks only requests that carry its cookies), so a form on another site cannot sign anyone in or out (login CSRF).
- **Better Auth's admin endpoints** are open to the owner and admins only with TOTP or a passkey and a session younger than 12 hours (`SECOND_FACTOR_REQUIRED`, `SESSION_NOT_FRESH`); only the owner changes roles, never to or from owner and never their own; the owner cannot be banned or deleted; impersonation, `create-user`, `set-user-password`, and `update-user` are off. P21-04 builds the admin pages on them; `checkAdmin` (`src/lib/auth/roles.ts`) is the gate its pages use.
- **`LIFE_OS_INSECURE_LOGIN_HOSTS`** works: on a listed plain-HTTP host a second Better Auth instance sets a non-Secure host-only `lifeos.session` cookie, which the check reads only on that host; it has no passkeys. Empty by default, as decided.
- **The legacy access reminder** is a banner in the Dashboard, once a day while legacy access is on and an account exists, but it stays hidden until P21-06 ships device pairing (it shows since P21-06) (the owner's decision: until the apps can pair, it asks for something that cannot be done yet). One switch turns it on: `DEVICE_PAIRING_AVAILABLE` in `apps/dashboard/src/lib/auth/constants.ts`, which P21-06 sets to `true`. Before the first account, the header's account button points to `/setup`.
- **The monitor reads `/internal/status`** on the private network (no service assertion until P21-07), and the Dashboard's server-side rendering still calls services without a token, which they accept until P21-07; the development proxies attach the token, as designed.
- **MCP's `401` carries no `WWW-Authenticate` yet**: there is no OAuth for it to point to until P21-06 (which adds it). MCP verifies a token when present like every service (`identity.Handler`).
- **`./setup.sh sign-out-all`** deletes every session and bumps every `authz_version` (keys, tokens, grants, invites, and pairing codes do not exist yet; P21-06 adds all but invites), then says whether legacy access is on and offers to turn it off; **`rotate-keys`** takes `auth` (or `all`, the same), and in a clone of the repository prints the two `docker compose` commands to run instead of running them.
- **Legacy access goes on from the host only.** Settings → Security turns it off; `POST /api/account/legacy-access` with `{"enabled": true}` answers `403 host_only`, for the owner too, and `./setup.sh legacy-access on` is the way.
- **The token cache hands out only young tokens.** The check reuses a cached token only while at least 4 of its 5 minutes are left (`IDENTITY_REMINT_SECONDS`), so a service never gets a token older than a minute, whichever method it serves; one signature a minute per credential is what that costs.
- **The setup code is used up right before the first account is stored**, in one `UPDATE … WHERE used_at IS NULL` in the user's create hook (after Better Auth's and the plugins' checks of the input): of two sign-ups racing with one code, one gets the account and the other `SETUP_CODE_INVALID`; a sign-up refused for its input leaves the code working. The same hook refuses any account once one exists (until invites, P21-07), and the first account's fixed id is a primary key besides.
- **`/share` is not in the proxy's matcher.** For a POST it matches, Next.js clones the body for the proxy and caps the clone at 10 MiB, and a share of two or three photos is larger. The share route runs the same check itself, on the headers alone, before it reads the form: without a sign-in it sends the share to the login and then to `/share/about`. The proxy's body limit stays at its default.
- **Better Auth's `disabledPaths` compares the request's path**, so a route with a parameter cannot be listed there: the reset email's callback (`/reset-password/:token`) is refused in the before hook (404); a reset link is a Dashboard page, `/reset/<token>`.
- **The Dashboard checks its key pair when it starts** (`src/instrumentation.ts`): it signs a probe token and verifies it with `LIFE_OS_TRUSTED_KEYS`. A missing `kid`, or the right `kid` with another public key, logs the reason and leaves every request failing, rather than every service refusing every token.
- **`setup.sh` never replaces a value someone set**: `account-keys` and `update` fill only empty settings and the example's `replace-with-…` placeholders, and stop when openssl cannot make a key (rather than writing an empty one).
- **`./setup.sh up` and `update` wait for the services from inside the Dashboard's container**, since their routes at the gateway need a sign-in now.

### As built in P21-04 and P21-05

P21-04 makes the server's surfaces the admins', and P21-05 adds the security log, server diagnostics, and `actor_user_id`, as this page says, with these details:

**Which surfaces, and legacy access.** This page says both that owners and admins need TOTP or a passkey before the admin surfaces open, and that legacy access covers what Life OS does today. Both hold, by when a surface came:

| Surface | Signed-in owner or admin, with two-factor | Signed in without two-factor | Member | Legacy access |
| --- | --- | --- | --- | --- |
| Status's details (`/status`, `/api/status`, Home's line): containers, the host's memory and disks, backups, the AI plan's usage | every check | the members' line, with a notice to turn on two-factor | one line | every check, as before |
| The backups' status and Back up now (`/api/backups/`) | yes | `403 second_factor_required` | `403 admin_only` | yes, as before |
| The admin pages (`/admin/…`) and the admin API (`/api/admin/…`): every account's security log, diagnostics | yes | refused, with what to turn on | refused | `401 sign_in_required`: new with accounts |

So the owner's pages, the phone and watch apps (which send no Life OS sign-in yet), and the monitor (`/internal/status`, unchanged and always complete) work as before while legacy access is on. A signed-in owner without two-factor sees less than legacy access does; that is the design's rule for a real sign-in, and the notice says what to turn on. None of these is a sensitive action: Back up now and the logs need no sign-in from the last 12 hours (Better Auth's admin endpoints still do, as in P21-03); `adminGate` (`src/lib/auth/admin.ts`) takes `sensitive` for P21-13's pages.

- **Modules, the version and update check, users, limits, and the server-wide integrations** have no Dashboard surface yet (they are `.env` and `./setup.sh`, and the users are Better Auth's admin endpoints, admin-only since P21-03). P21-12 and P21-13 build their pages behind the same gate.
- **The members' line** is one per module they use (every module that is on, until P21-12): `"Everything works."`, or `"Calendar has a problem."` (`"Calendar and Food have a problem."`). Only a check in the problem state counts, and a service that is not ready counts for its module (Calendar feeds and Google Calendar for Calendar, Recipe import for Food); the server's own checks (Server, Backups, the Backup, Host health, and Monitor services) never show. A warning (a backlog, a consent that expires soon) is the admins' to act on. `/api/status` answers a member `{"view":"member","checkedAt",…,"overall","problems":["Calendar"],"checks":[]}`; the full answer is unchanged, without `view`.
- **Behind the Dashboard**, the backup service, host-health, the processor's `/status` (the AI plan's usage), and the delivery health that only Status reads (Notifications' `GET /v1/channels`, whose errors can name the ntfy server; Calendar's `/v1/reminders/status`; Tasks' and Food's `/v1/notifications/status`) refuse a token whose role is not owner or admin (`403 admin_only`, `pkg/identity`'s `AdminOnly` and `RequireAdmin`); legacy access is the owner. A request without a token still passes (only internal callers reach them, until P21-07). The processor verifies tokens now (`LIFE_OS_TRUSTED_KEYS` in its environment).
- **Two-factor is the Dashboard's check, until P21-06** (which adds the `mfa` claim and makes these routes require it: [As built in P21-06](#as-built-in-p21-06)). The identity token carries the role, not whether the person has a second factor, so services check the role only, as the table in [Roles and checks](#roles-and-checks) says: Activity's diagnostics (`GET /v1/diagnostic-events` and `/summary`), the delivery health behind Status (Notifications' channels, Calendar's reminder status, Tasks' and Food's notification status), the backup service, host-health, and the processor's `/status`. A signed-in admin without two-factor can therefore call the diagnostics and the delivery health through the gateway directly (both carry no content), though the Dashboard shows them neither. P21-06 adds the `mfa` claim ([The identity token](#the-identity-token)), and these routes then require it.

**The security log** (`security_events` in `auth_db`, migration `000003`):

- Each entry has a kind, an outcome (`ok`, `failed`, `refused`), who did it (`actor_user_id`), whom it concerned (`subject_user_id`), how they came (`via`: `session`, `legacy`, `host`, …), the address, a short device label ("Chrome on Android", never the User-Agent), a code (`detail`: the way in, the new role, a reason), and a count. The table's checks allow only an address, a label of letters, and a lowercase code in those columns, so no title or message fits. A typed username is never kept: a failed sign-in names the account only when the name is one.
- **Kinds today**: sign-ins and failed ones (password, authenticator code, recovery code, passkey; a retry the per-username delay refuses is not one: the failures before it are), sign-outs, sessions signed out, password changes and resets, two-factor on and off, new recovery codes, passkeys added and removed, the setup code made, refused, and used, legacy access's use and changes, the owner's reset link from the host, signing everyone out, a rotated signing key, role changes, disabling, enabling, deleting, and signing out an account, refused admin actions, and Back up now. Invites, devices, tokens, limits, server settings, ownership transfer, and delegation grants add their kinds with their tickets.
- **Where they are written**: Better Auth's endpoints by a before hook (who asks) and an after hook in a plugin of its own listed last (after the two-factor plugin's, so a password step that still needs a code is not taken for a sign-in); the account API and the backups route; the host's commands (`scripts/lifeos-auth.mjs`, which never lets a failed entry stop an emergency command); and the Dashboard at start, which notes the signing key's `kid` in `server_settings` and logs `keys_rotated` when it changed (`./setup.sh rotate-keys` edits only `.env`).
- **Failed sign-ins are grouped**, like legacy access: one entry a day (UTC) for each address, account (or none, when the name typed is none), and code (the way in and the reason), whose count and last time grow. An address gets at most 20 such entries a day; past them, its further failures count together in one more entry, without the account or the reason (`other:too_many_entries`), so an install without Cloudflare Access in front cannot have its log filled from one address. Writes run one after another, so two failures at once make one entry.
- **Legacy access is counted, not logged per request**: one entry a day (UTC) for each address and device label, with the first and last time and the number of requests. The first request of a new address, device, or day is written at once, the counts every minute. Past 200 addresses and devices in a day, the new ones count together in one entry without either, so a flood cannot fill the log. The checks a page or route repeats after the proxy are not counted twice.
- **Who reads what**: everyone reads the entries whose actor or subject they are (Settings → Security log, `GET /api/account/security-log`); legacy access's entries concern the first account. In that personal view, an entry someone else did to the account (an admin disabling it, changing its role) shows no address or device: those are the admin's. The address and device show where the reader did it, or where the actor is not known (a failed sign-in, legacy access). Entries are paged by their exact time (PostgreSQL's microseconds) and id. Admins with two-factor read all of them, filtered by account, kind, and days (`/admin/security-log`, `GET /api/admin/security-log`).
- **Kept for a year**: the Dashboard deletes entries whose last time is older than 365 days at start and once a day.

**Diagnostics** (`diagnostic_events` in `activity_db`, migration `000004`):

- `pkg/diagnostics` counts each service's server errors (answers 500 to 599) in memory by code, route pattern, and status, and sends them to Activity once a minute and at shutdown, so a burst is one row. `pkg/httpserver` does it for every service whose `ACTIVITY_SERVICE_URL` is set (`ACTIVITY_DIAGNOSTICS=off` turns it off); Activity itself records none.
- The code comes from a fixed list (`diagnostics.Codes`, the contract's `DiagnosticCode`, a test keeps them equal): a service's own 5xx code when listed, else one per status. The Go `Event` has no free-text field (a test fails when one is added), and Activity checks every field again with the same rules; the columns' checks refuse text as well.
- Rows are kept 90 days (this page set no time): Activity deletes older ones at start and once a day, and at most once an hour as it records.
- `GET /v1/diagnostic-events` and `/summary` are for owners and admins who signed in (legacy access gets `401 sign_in_required`, members `403 admin_only`); the Dashboard's Diagnostics page (`/admin/diagnostics`) reads them on the private network after its own check. `POST` is the services' only: the gateway passes only `GET` to Activity, and a person's token is refused.
- Containers, health, backups, and the AI's queue are already on Status's admin view, so Diagnostics links there rather than repeating them.

**Service logs carry no content.** Beyond `pkg/httpserver`, the services' own log lines name things by id and code: the processor (a repeated capture, the captures a capture waits for, a failed AI run, whose error stays on Status), Banking (no bank's free-text description, nor which bank), google-calendar (Google's and Calendar's messages become codes such as `google_http_400`, and a Google calendar's id, often an email address, a short hash), calendar-feeds (Calendar's status and code), and Notifications (no push service's or ntfy's answer, nor a device's name). `pkg/httpserver` logs each answer with its ids, the route pattern (never the path, which may hold a search), the status, and the error code; a client error's message (which often quotes what was sent) is no longer logged, a server error's cause is (the service's own failures), and a panic's value only when it is the runtime's own error, else its type. Tests plant made-up titles, notes, amounts, and searches through `pkg/httpserver`, Tasks (`TestLogsAndDiagnosticsNeverCarryContent`, against its database), and Activity's diagnostics API, and fail when any reaches a log or a diagnostic.

**`actor_user_id`** (`activity_db` migration `000004`, nullable): `pkg/activity`'s emitter fills it from the request's verified identity token, the user of a session or the first account under legacy access, also when a development tool or the AI writes for them; a service's own work and every event from before have none. The Activity API returns it as `actorUserId`. `space_id` comes with P21-07. The migration's index on it (`activity_events_actor_user_idx`) is built without `CONCURRENTLY`, so it holds a lock that blocks recording events while it reads the table: a fraction of a second on the reference server, longer on a table of millions of events. Services retry an event for about half a second, then log it as not recorded and go on; run `activity-migrate` at a quiet time on a big table.

There is no "send to admin" button and no switch to turn any of this off: a single self-hoster is the owner and the only user, and sees everything they see today.

### As built in P21-06

P21-06 builds the server side of devices, tokens, and MCP as [Devices, tokens, MCP](#devices-tokens-mcp) describes; the phone and watch apps' side follows against [the API contract](../api/devices.md). The differences and details, each with its reason:

**Better Auth.** `@better-auth/api-key` and `@better-auth/oauth-provider` 1.7.22 (separate packages in 1.7.x), pinned like the rest, and the jwt plugin; `auth_db` migration `000004` is their schema as Better Auth's generator writes it (`test/auth/print-schema.mts` prints what a new migration must add), plus `session.secondFactor`, `pairing_codes`, and `device_key_cleanup`. The HTTP endpoints Life OS does not use are off: the api-key plugin's (keys are made through Life OS's own endpoints, which set their kind and scopes; the plugin's would let anyone signed in make a key of any kind), the jwt plugin's `/token` (no session JWTs), the OAuth provider's client and consent management, introspection, userinfo, and end-session (no `openid` scope), its admin resource endpoints (by prefix in the before hook), and `/revoke-sessions` (Sign out everywhere is Life OS's own, below). On a plain-HTTP host allowed for signing in there is no OAuth.

**Device keys and personal tokens** are the api-key plugin's keys (`apikey`, SHA-256, shown once) with the prefix `lo_dev_` or `lo_tok_` and 64 letters, told apart by their metadata: `{"kind":"phone"}`, `{"kind":"watch","parentId":…}`, `{"kind":"token","scopes":[…],"devTool":…}`. The auth check verifies them itself, with one query that also reads the account (a ban counts at once), rather than through the plugin's verify endpoint, which writes the key's row on every request; it notes a key's last use at most once a minute. Expired tokens stay listed (as expired) for 30 days, then the daily upkeep deletes them.

**Pairing.** Codes look like setup codes (16 characters, 80 bits), are stored as SHA-256, work once, for 5 minutes, at most 5 unused per account (a sixth replaces the oldest), and are redeemed in one `UPDATE`, which also refuses a disabled account's. Not in the design: **the Custom Tab flow binds its code to the app's challenge** (`BASE64URL(SHA-256(verifier))`, as PKCE does), because any app can register the `lifeos://` scheme: a code caught on the way is useless without the verifier. `/login?pair=1` needs the challenge, asks the person to confirm, and only ever sends the browser to `lifeos://pair` (with `error=access_denied` on Cancel); it takes no return address from the request. Redeeming is limited per address, in memory: 5 tries a minute, and after 10 wrong codes within 15 minutes none for 15 minutes (`429`, `Retry-After`), on top of the gateway's `/api/auth/` zone; refused codes go into the security log grouped like failed sign-ins (`pairing_refused`). `/api/auth/devices/…` are Life OS's own handlers, mounted before Better Auth in the same route, and answer errors in the services' envelope, since the app reads both. A phone has one watch key; a new one replaces the old (and its push subscriptions).

**The check and Bearer.** Only the `Bearer` scheme is a credential; `Basic`, which a proxy in front may send, is ignored as before. A request with a bearer is judged by it alone: unknown, revoked, expired, a disabled account's, or malformed is `401` (with MCP's challenge on `/api/mcp/`), never the cookie's account and never legacy access. The route a credential may reach is the gateway's `X-Original-URI` (nginx sets it; the Dashboard's own routes pass their path, never a client's header): a device key reaches every service route, `/android/`, and the Dashboard's API but not `/api/mcp/` (`403`); a token only the modules its scopes name (`<module>:write` includes reading; the integrations' routes count as the module they feed: banking as finance, Google Calendar and the calendar feeds as calendar, recipe import as food), never `/android/`, MCP, or the Dashboard's API (`403 insufficient_scope`); an MCP access token only `/api/mcp/`. Pages are browsers' only: a key or token opens none. `pkg/httpserver` installs `identity.RequireScope` with each service's module (`identity.ServiceModule`), so a scoped token, MCP's forwarded one included, is checked again where it lands; the server's own services (backup, host-health, monitor) have no module and refuse every scoped token.

**The `mfa` claim** is true only on a session that signed in with an authenticator code, a recovery code, or a passkey, kept as `session.secondFactor` (a Better Auth additional field that the security log's after hook sets, never a client). A detail the design did not settle: a session in which the person turns on the authenticator app (the first code) or adds a passkey counts too, since that proves the factor; otherwise the owner would have to sign out right after turning it on. **Device keys, personal tokens, and MCP grants never carry it and never pass an admin gate**, the owner's included (the owner's decision: the phone's admin views come through the TWA, whose web sign-in has its own second factor), so a paired admin phone sees Status's one line. `pkg/identity`'s `CheckAdmin`, and with it every admin-only service route (backup, host-health, the processor's `/status`, the delivery health behind Status, Activity's diagnostics), requires a browser session with the role **and** `mfa` (`403 second_factor_required`; a key or token `403 admin_only`); legacy access keeps its place from P21-04 (the owner, on the surfaces from before accounts). The Dashboard's admin gate and Better Auth's admin endpoints require the same: an account with a second factor whose session did not sign in with it gets `second_factor_sign_in_required` ("sign out, and sign in again with it"). Sessions from before the update have no `mfa`: the owner signs in again with the second factor to see Status's details.

**The `dev` claim** (new, not in the claims table before): a token marked as a development tool carries `dev: <name>` (from the token's name, letters, digits, `.`, `_`, and `-`), and `pkg/activity` makes its actor `dev:<name>`; an MCP grant's actor is `agent:mcp`; a device key's, and a token's that is no development tool, `user`. `actor_user_id` is the person in every case.

**MCP's OAuth.** The issuer is the public URL itself, so the authorization server's metadata is at `/.well-known/oauth-authorization-server` (RFC 8414). **The protected resource metadata is the Dashboard's, not MCP's** (a deviation): the gateway's routes of an optional module are `/api/` prefixes and every location that proxies to a service must ask the auth check, while the Dashboard's location is public and serves the other metadata already; it answers 404 while the `ai` module is off. `offline_access` is a scope too, named in the challenge and the metadata, since the provider issues refresh tokens only for it. Access tokens are EdDSA JWTs (the jwt plugin's key in `auth_db`'s `jwks`, which the check reads and caches, never fetches), 15 minutes, `aud` the resource; the check refuses a token issued before its grant's consent, so connecting an app again after disconnecting it does not revive its old tokens. Refresh tokens rotate (a spent one is refused). The registration is open (5 a minute per address, besides the gateway's zone), and clients nobody connected within a week are deleted daily. MCP's `401` carries the check's `WWW-Authenticate`: nginx's `auth_request` passes it on with the 401 by itself, through the server's `error_page` too, so no `auth_request_set` is needed (`tools/test-gateway-auth` checks it is there once). MCP lists the tools the identity token's module scopes allow (`mcp:read` the read tools, `mcp:write` all; a server per set of allowances, built once) and checks every call again; until `mods` exists (P21-12) every installed module is allowed. The token reaches the domain services with each call (`coreclient`). Client-ID metadata documents (`@better-auth/cimd`) are left out, as designed.

**Revoking.** Settings → Devices revokes a key (a phone's with its watch's), Settings → API tokens a token, Settings → Connected apps a grant (its consent and every refresh and access token, in one transaction). **Sign out everywhere** (Settings → Security, `POST /api/account/sign-out-everywhere`) and `./setup.sh sign-out-all` delete, in one transaction, the sessions, device keys (unless `--keep-devices`), tokens, OAuth tokens and consents, and pairing codes, and bump `authz_version`. Better Auth's admin `revoke-user-sessions`, `ban-user`, and `remove-user` delete the sessions themselves; the account's keys, tokens, and grants follow in a transaction of their own right after (API keys name their account without a foreign key, so a deleted account's go this way too) (the check refuses a banned account's keys in between all the same). Every revoked device key is noted in `device_key_cleanup` in the same transaction, and the Dashboard asks Notifications to delete its push subscriptions (`DELETE /v1/push/device-keys/{id}`, refused with any identity token until P21-07's service assertions) at once and every minute until it answers.

**Legacy access** can be turned off for real: `DEVICE_PAIRING_AVAILABLE` is on, so the daily reminder shows; Settings → Security still only turns legacy access off, and turning it on stays the host's. With it off, a paired phone's key does every native call the phone made with the Access service token (voice upload, Inbox and its photos, Tasks, Calendar, push registration, the update manifest and the APKs, the watch's relay with the watch's key), and the TWA's cookie does the share sheet and the web pages (the auth flow suite and `tools/smoke-accounts` check each).

**The security log** gains `device_paired` (`phone:qr`, `phone:sign_in`, `watch`), `device_revoked` (`phone`, `watch`, `watch:replaced`, `…:self`), `pairing_refused` (grouped), `token_created` (`dev_tool`), `token_revoked`, `oauth_client_registered`, `oauth_consent_given` (`mcp:read`, `mcp:read_write`), `oauth_consent_revoked`, and `signed_out_everywhere`. No device's or token's name, no client's name or address: codes only.

## Risks and decisions

Risks:

- **One subscription for several people.** ChatGPT and Claude plans are personal; serving family members' requests from the owner's subscription may break the provider's terms, and their data goes to the provider under the owner's account. The owner chose it for the beta; onboarding says which account serves the AI. Revisit before anything hosted.
- **The Dashboard carries every API call** (2.4 ms each) and is a single point of failure. Mitigated by its memory headroom, health check, restart policy, and background work that does not need it.
- **Better Auth churns** (21 advisories in a year, breaking minors). Pinned versions, a minimal plugin set, our own migrations, and the flow suite in CI keep upgrades deliberate.
- **The backend network is plain HTTP.** Something able to sniff it (root on the host, or a container with raw sockets) could replay a token for its lifetime (5 minutes; 1 for assertions). P21-07 adds `cap_drop: [ALL]` to every service that does not need a capability; TLS between containers is out of scope.

Decided by the owner (2026-11-14):

1. **WireGuard over plain HTTP**: require the HTTPS name. `LIFE_OS_INSECURE_LOGIN_HOSTS` stays as an opt-in and is empty by default.
2. **Cloudflare Access on the live server**: it stays in front during the beta, also once family members exist, until Life OS's login has run for a while and had an outside security test. Each family member is added to the Access group and passes its email code; the owner set the Access session to one week. Signing everyone out of Access in an emergency: [Cloudflare → Sign everyone out](../operations/cloudflare.md#sign-everyone-out).
