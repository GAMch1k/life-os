# Devices and tokens: the API for the phone and watch apps

This page is the contract between a Life OS server and the Android and Wear OS apps ([GAMch1k/life-os-app](https://github.com/GAMch1k/life-os-app)): how a phone pairs with an account, how it and its watch prove who they are on every call, and what the answers look like when they are refused. Personal API tokens and MCP clients use the same `Authorization: Bearer` header; they are described in [Accounts](../install/accounts.md#api-tokens) and [MCP](../services/mcp.md#signing-in-with-oauth). Why it is built this way is in [Multi-user design](../architecture/multi-user.md#devices-tokens-mcp).

Everything here is new with P21-06. A server from before it answers `/api/auth/devices/pair` with 404: an app that gets 404 there falls back to what it did before (the Cloudflare Access service token alone, which works while the server's [legacy access](../install/accounts.md#legacy-access) is on).

## In short

1. The phone gets a **pairing code**, from a QR code in **Settings → Devices** (`lifeos://pair?host=…&code=…`) or by signing in in a Custom Tab (`/login?pair=1`).
2. It redeems the code once at `POST /api/auth/devices/pair` and receives its **device key**, `lo_dev_…`, shown this one time. It keeps it in Android Keystore-backed storage.
3. It sends `Authorization: Bearer lo_dev_…` on **every** native call to Life OS: voice uploads, Inbox, Tasks, Calendar, push registration, update checks, APK downloads, and the watch's requests it relays.
4. It may create **one child key for its watch** at `POST /api/auth/devices/child`.
5. A `401` means the key no longer works (revoked, expired, the account disabled, everyone signed out): the app says so and offers to pair again.

The TWA (the web Dashboard in Chrome) signs in through the ordinary web login and keeps Chrome's session cookie. It never sees or sends the device key.

## Headers on every native call

| Header | When | Value |
| --- | --- | --- |
| `Authorization` | always, once paired | `Bearer <device key>`: the phone's own key for its calls, the watch's key for the watch's (relayed or direct) |
| `CF-Access-Client-Id`, `CF-Access-Client-Secret` | when the server is behind Cloudflare Access and the app has a service token | as today. Access lets the request through to Life OS; Life OS's own check is the device key |
| `Idempotency-Key` | on the writes that take one (voice uploads, Food) | as today |
| `X-Life-OS-Actor` | never needed with a key | a request with a device key acts as the account's user (`user` in the Event Log); the header is ignored |

Only the `Bearer` scheme is a Life OS credential. A request that carries `Authorization: Bearer …` is judged by that token alone: if the token is not valid, the answer is `401`, never the cookie's account and never legacy access. A request without `Authorization` is judged by its session cookie, or by legacy access while that is on.

A key is 71 characters: `lo_dev_` and 64 letters (`A–Z`, `a–z`). Personal tokens look the same with `lo_tok_`. Never log a key, put it in a URL, or send it anywhere but the server that issued it (in particular, not to GitHub when downloading an update from there).

## Pairing

### The pairing link

```
lifeos://pair?host=<public URL>&code=<code>[&state=<state>]
```

| Parameter | Meaning |
| --- | --- |
| `host` | the server's public URL (`LIFE_OS_PUBLIC_URL`), URL-encoded, such as `https%3A%2F%2Flifeos.example.com`. `https://`, or `http://` only for `localhost`, `127.0.0.1`, and `[::1]` |
| `code` | the pairing code, such as `K7QM-2XRP-9VDA-H4TC`: 16 characters from `ABCDEFGHJKLMNPQRSTUVWXYZ23456789` in four groups. Case and dashes do not matter when it is redeemed. It works **once**, for **5 minutes** |
| `state` | only in the Custom Tab flow: the `state` the app passed, unchanged |
| `error` | only in the Custom Tab flow, instead of `code`: `access_denied` when the person tapped **Cancel** |

The app shows the host and asks before it pairs ("Pair with lifeos.example.com?"): a pairing link can come from anywhere a link can.

### Flow 1: QR code (a second screen)

1. In the Dashboard, **Settings → Devices → Pair a phone** shows a QR code of the pairing link (and the code as text, to type if the camera fails). The Dashboard asks for it with `POST /api/account/devices/pairing-codes` (below).
2. The app scans it (its own scanner, or the system camera opening the `lifeos://` link), confirms the host with the person, and redeems the code.

### Flow 2: Custom Tab (a phone alone)

1. The app makes a random `verifier` (43 to 128 characters of `A–Z a–z 0–9 - . _ ~`), its `challenge = BASE64URL(SHA-256(verifier))` without padding (43 characters), and a random `state` (8 to 128 characters of `A–Z a–z 0–9 - _`).
2. It opens a Custom Tab at

   ```
   <server>/login?pair=1&challenge=<challenge>&state=<state>
   ```

3. The person signs in as usual (password and two-factor, or a passkey; already signed in in Chrome, they skip this), and sees **Pair this phone with Life OS?** with **Pair** and **Cancel**.
4. **Pair** sends the browser to `lifeos://pair?host=…&code=…&state=…`; **Cancel** to `lifeos://pair?error=access_denied&state=…`. The page never sends the browser anywhere else: no return address is taken from the request.
5. The app checks `state`, then redeems the code **with `verifier`**. A code made with a challenge works only with the verifier: another app that catches the `lifeos://` link cannot use it.

The `host` in the link is the server's public URL. The app redeems the code at the server it opened (normally the same).

### `POST /api/auth/devices/pair`

Public (no credential): the code is the credential. Redeems a pairing code and returns the phone's device key, once.

```http
POST /api/auth/devices/pair
Content-Type: application/json

{"code": "K7QM-2XRP-9VDA-H4TC", "name": "Pixel 9", "verifier": "…"}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `code` | yes | the pairing code |
| `name` | yes | what Settings → Devices calls this phone: 1 to 64 characters, no control characters (the phone's model, or the name the person gave it) |
| `verifier` | when the code was made with a `challenge` (Custom Tab) | the verifier whose SHA-256 is the challenge |

`200`:

```json
{
  "key": "lo_dev_AbCd…",
  "keyId": "0b9e7c1e-5d0a-4a43-9a43-2f5f3e6f1a11",
  "name": "Pixel 9",
  "kind": "phone",
  "createdAt": "2026-11-14T18:02:11.000Z",
  "user": {"id": "00000000-0000-4000-8000-000000000001", "username": "anna", "name": "Anna"},
  "publicUrl": "https://lifeos.example.com"
}
```

The key does not expire; it works until it is revoked. After pairing, the app registers for push again (below), so that its subscription belongs to the key.

### Errors of the pairing endpoints

The `/api/auth/devices/…` endpoints answer errors as the services do:

```json
{"error": {"code": "pairing_code_invalid", "message": "That pairing code is wrong, used, or expired. Make a new one in Settings → Devices."}}
```

| Status | `code` | When |
| --- | --- | --- |
| 400 | `bad_request` | the body is not JSON, or a field is missing or malformed |
| 400 | `pairing_code_invalid` | the code is wrong, already used, expired, or the verifier does not match: one answer for all, so that nothing can be learnt by guessing |
| 401 | `unauthenticated` | (`/child`, `/me`, `/sign-out`) no `Authorization: Bearer`, or the key is not valid |
| 403 | `forbidden_origin` | a browser on another site sent it |
| 403 | `not_a_phone_key` | (`/child`) the key is a watch's key or a personal token |
| 429 | `too_many_attempts` | too many tries from this address: 5 a minute, and after 10 wrong codes within 15 minutes, none for 15 minutes. `Retry-After` says how many seconds to wait |

The gateway also limits `/api/auth/` to 2 requests a second per address (bursts of 20): past that it answers `429` with an HTML body.

## The watch

The phone creates one key for its watch and hands it over the Wear OS Data Layer. Whether the watch calls Life OS directly or the phone relays its requests, the watch's requests carry the **watch's** key, so that Settings shows its own last use and revoking the phone or the watch works as the person expects.

### `POST /api/auth/devices/child`

```http
POST /api/auth/devices/child
Authorization: Bearer lo_dev_<the phone's key>
Content-Type: application/json

{"name": "Galaxy Watch8"}
```

`200`:

```json
{
  "key": "lo_dev_EfGh…",
  "keyId": "6a1f…",
  "name": "Galaxy Watch8",
  "kind": "watch",
  "parentKeyId": "0b9e7c1e-5d0a-4a43-9a43-2f5f3e6f1a11",
  "createdAt": "2026-11-14T18:05:40.000Z"
}
```

- Only a phone's key may create one (a watch's key or a personal token gets `403 not_a_phone_key`).
- A phone has **at most one** child key: creating another revokes the previous one (and its push subscriptions) in the same step. Use this when the watch is reset or replaced.
- Revoking the phone's key revokes its watch's key too. Revoking the watch's key leaves the phone's.
- In Settings → Devices the watch is listed under its phone and can be revoked on its own.

## Using the key

### `GET /api/auth/devices/me`

With a device key: who it is. For a settings screen ("Paired as anna on lifeos.example.com") and to check a key without touching any data.

```json
{
  "keyId": "0b9e7c1e-…",
  "name": "Pixel 9",
  "kind": "phone",
  "parentKeyId": null,
  "createdAt": "2026-11-14T18:02:11.000Z",
  "user": {"id": "00000000-0000-4000-8000-000000000001", "username": "anna", "name": "Anna"}
}
```

`401 unauthenticated` when the key does not work.

### `POST /api/auth/devices/sign-out`

With a device key (or a personal token): revokes **that key** (a phone's together with its watch's) and its push subscriptions. `204`. For **Settings › Disconnect** in the app. The app then forgets the key.

### What a device key may do

Everything the account may do through the API, as the account's user: Tasks, Calendar, Inbox (with photos), Voice, Food, Finance, Notifications and push registration, Activity, the integrations' APIs, and `/android/` (the update manifest and the APKs). Requests through a device key appear in the Event Log as the user, like the Dashboard's.

It may not:

- manage the account: `/api/account/…` (sessions, devices, tokens) and `/api/admin/…` answer `401 sign_in_required`;
- open the server's admin routes: the backups, host health, the AI plan's usage, the diagnostics, and the delivery health behind Status answer `403 admin_only`, even for the owner's phone. Admin views on the phone come through the TWA, whose web sign-in has its own second factor. `/api/status` answers the members' one line (`"view": "member"`);
- use MCP (`/api/mcp/` answers `403`): MCP clients sign in with OAuth.

### The calls the app makes

All with `Authorization: Bearer <key>`; bodies and answers are unchanged from [the API overview](overview.md) and the services' OpenAPI documents.

| What | Request |
| --- | --- |
| Today: tasks | `GET /api/tasks/v1/tasks?…`, `POST /api/tasks/v1/tasks/{id}/complete`, `…/reopen` |
| Today: events | `GET /api/calendar/v1/events?from=…&to=…&timeZone=…` |
| Capture text, photos | `POST /api/inbox/v1/inbox-items`, the media upload, `GET /api/inbox/v1/inbox-items/{id}`, `…/processing-attempts` |
| Voice note | `POST /api/voice/v1/audio-jobs` (multipart, up to 25 MB), `GET /api/voice/v1/audio-jobs/{id}` |
| Push | `GET /api/notifications/v1/push/config`, `POST /api/notifications/v1/push/subscriptions`, `DELETE /api/notifications/v1/push/subscriptions/{id}` |
| Updates | `GET /android/updates` (the manifest), `GET /android/lifeos-phone-<version>-<code>.apk`, `GET /android/lifeos-wear-<version>-<code>.apk` (with `Range` for resuming) |
| The watch's relay | the watch's request, as it is, with the watch's key |
| Who am I | `GET /api/auth/devices/me` |

The share sheet is the TWA's (`POST /share`, a web form with Chrome's cookie), not a native call.

**Long uploads.** The gateway checks the key when the request's headers arrive, and the identity token it hands the service stays valid for at least 4 minutes from then: a voice note that takes up to about 4 minutes to upload is fine; a slower one fails with `401` from the service and should be retried (the `Idempotency-Key` makes a retry safe).

## Push notifications and the key

`POST /api/notifications/v1/push/subscriptions` with a device key stores the subscription **with that key's id** (`deviceKeyId` in the answer). Subscribing again with the same endpoint moves the subscription to the key that sent it: so after pairing, the app registers again, and an endpoint registered before pairing (with the service token alone, under legacy access) now belongs to the key. A subscription made by the TWA (web push in Chrome) has no device key.

**Revoking a key deletes its push subscriptions**: in Settings → Devices, with **Sign out everywhere**, with `./setup.sh sign-out-all` (unless `--keep-devices`), with the key's own sign-out, and when a phone's new child key replaces the old one. The server does it within a minute, also if Notifications was briefly down. The app need not unsubscribe first; if it does, `DELETE …/push/subscriptions/{id}` answers `204` or `404` (already gone).

```json
{
  "id": "f3c1…",
  "deviceLabel": "Android app",
  "deviceKeyId": "0b9e7c1e-5d0a-4a43-9a43-2f5f3e6f1a11",
  "endpoint": "https://fcm.googleapis.com/fcm/send/…",
  "pushService": "fcm.googleapis.com",
  "createdAt": "2026-11-14T18:02:30Z"
}
```

## When a request is refused

| Answer | Comes from | Means | The app |
| --- | --- | --- | --- |
| `401` `{"error":{"code":"unauthenticated",…}}` | the gateway | the key does not work: revoked, its account disabled, everyone signed out, or no key while legacy access is off | says "This phone is no longer connected to Life OS" and offers to pair again; it stops retrying the request |
| `401` `{"error":{"code":"unauthenticated",…}}` | a service | the identity token ran out during a very slow upload (above) | retries once; a second `401` is the row above |
| `403` `{"error":{"code":"module_not_allowed",…}}` | a service | the account may not use this module (an admin turned it off for the person; from P21-12) | shows "Your account can't use <module> on this server" and hides that part; it does **not** re-pair |
| `403` `{"error":{"code":"admin_only",…}}` | a service | an admin route (above) | does not offer it natively |
| `403` `{"error":{"code":"forbidden",…}}` | the gateway | a route the key may not use (`/api/mcp/`), or a write from a browser on another site | a bug in the app: log it |
| `404` `{"error":{"code":"module_off",…}}` | the gateway | the module is not installed on this server | hides that part |
| `503` `{"error":{"code":"auth_unavailable",…}}` | the gateway | the sign-in check is down for a moment | retries later; keeps the key |
| `302` to Cloudflare, or `403` HTML from Cloudflare | Cloudflare Access | the Access service token was refused | as today |

A `401` from Life OS always has the JSON body above. It never redirects: only a page in a browser is sent to `/login`.

## For the Dashboard: managing devices

These are the Dashboard's own calls (`/api/account/…`, with the session cookie of a real sign-in and Life OS's own `Origin`). They are listed so that the contract is complete; the app does not call them.

| Request | Answer |
| --- | --- |
| `POST /api/account/devices/pairing-codes` `{}`, or `{"challenge": "…", "state": "…"}` (the Custom Tab page) | `201 {"code": "K7QM-…", "expiresAt": "…", "link": "lifeos://pair?host=…&code=…"}`. An account has at most 5 unused codes; a sixth replaces the oldest |
| `GET /api/account/devices` | `200 {"devices": [{"id", "name", "kind": "phone" \| "watch", "parentId", "createdAt", "lastUsedAt"}]}`; `lastUsedAt` is precise to a minute |
| `DELETE /api/account/devices/{id}` | `204`; a phone's watch key goes with it; `404 not_found` |
| `POST /api/account/sign-out-everywhere` | `204`: every session (this one too), device key, personal token, and connected app of the account, in one step |

Without a real sign-in they answer `401 sign_in_required`; legacy access, a device key, or a token is not enough.
