# Google Calendar setup

Life OS connects to the owner's one Google account through an OAuth client in the owner's own Google Cloud project. The client credentials and the token key live only in the server's ignored `.env`; Calendar stores the refresh token encrypted in its database. How the connection works is described in [Calendar: Google Calendar connection](../services/calendar.md#google-calendar-connection).

Once connected, the calendars you tick sync into Life OS every two minutes, one way: from Google into Life OS (see [Calendar: Google Calendar sync](../services/calendar.md#google-calendar-sync)). Changing those events from Life OS, and a dedicated "Life OS" calendar in Google for events created in Life OS, come later (P11-03).

## 1. Google Cloud project and API

1. In the [Google Cloud console](https://console.cloud.google.com/), create a project (for example "Life OS") or pick an existing one.
2. **APIs & Services → Library → Google Calendar API → Enable.**

## 2. OAuth consent screen (Google Auth Platform)

1. **Branding:** app name "Life OS", the owner's address as user support and developer contact email, and the links Google asks for:
   - App home page: `https://lifeos.gamch1k.com`
   - Privacy policy: `https://lifeos.gamch1k.com/privacy`
   - Terms of service: `https://lifeos.gamch1k.com/terms`
   - Authorized domain: `gamch1k.com`
2. **Audience:** User type **External**, then **Publish app** so the status is **In production**. In "Testing", Google expires refresh tokens after 7 days and the connection would turn `expired` every week.
3. **Data access:** add the three scopes Life OS asks for:
   - `https://www.googleapis.com/auth/calendar.calendarlist.readonly`
   - `https://www.googleapis.com/auth/calendar.events`
   - `https://www.googleapis.com/auth/calendar.app.created`

`calendar.events` is a sensitive scope. Without Google's verification (not needed for one personal user) the consent page warns "Google hasn't verified this app"; continue with **Advanced → Go to Life OS**. If you ever apply for verification, Google's reviewers must be able to open the home, privacy, and terms pages, which Cloudflare Access currently hides behind its login.

## 3. OAuth client

**Clients → Create client:**

- Application type: **Web application**, name for example "Life OS Calendar".
- **Authorized redirect URIs:** exactly `https://lifeos.gamch1k.com/calendar/google/callback` (the Dashboard's callback route). No JavaScript origins are needed.

Copy the client ID and the client secret. The secret is shown in full only now; keep it in a password manager as well.

## 4. Server `.env`

Add to the server's `.env` (never to the repository):

```sh
LIFE_OS_GOOGLE_CLIENT_ID=<client id>.apps.googleusercontent.com
LIFE_OS_GOOGLE_CLIENT_SECRET=<client secret>
LIFE_OS_GOOGLE_TOKEN_KEY=<output of: openssl rand -base64 32>
# Only if the callback lives elsewhere (it must match the client's redirect URI):
# LIFE_OS_GOOGLE_REDIRECT_URI=https://lifeos.gamch1k.com/calendar/google/callback
```

Generate the token key once with `openssl rand -base64 32` and keep it: it decrypts the stored refresh token, so a new key means reconnecting Google. Then apply the settings with `docker compose up -d calendar` (a normal redeploy also does). Calendar logs at start whether Google is configured, naming any missing or invalid setting but never its value (`docker compose logs calendar | grep -i google`).

## 5. Connect

1. Open **Calendar → Google Calendar → Connect Google**.
2. Sign in to Google and allow every permission. Unticking "See the list of calendars" makes the connection fail; unticking another permission connects, but the dialog lists what is missing.
3. Back in Life OS the dialog shows "Connected as …" and the account's calendars. The primary calendar is chosen to sync; tick the others you want.

Check: the Status page's **Calendar sources** card has a "Google Calendar" part, "Connected as …", and the Event Log shows "Google Calendar connected".

## 6. Sync

What you see once a calendar is ticked:

- **Within moments** the calendar's first sync runs: its events from a year back and every future event appear in Life OS. The dialog shows under each ticked calendar "Waiting for its first sync…", then "Synced 10 Oct, 12:05 CEST · 143 events" (reopen the dialog to see it change), and the Event Log shows "Source refreshed: <calendar> — 143 added" by "Google Calendar sync".
- **In Calendar** the events carry the calendar's name (like "ZAW" for the waste feed) and are read-only: change them in Google and Life OS follows within two minutes. Only their reminders can be changed here, and your own reminder changes stay even when Google changes the event. A recurring Google event repeats like one created in Life OS (with its skipped and changed occurrences); a rule Life OS cannot hold, such as "every second Tuesday", shows as separate events for the next 400 days. Work location markers (Home, Office) are not imported.
- **Afterwards** every two minutes Life OS asks Google only for what changed. Syncs that change nothing leave no trace; one that changes something adds one Event Log entry ("Source refreshed: Family — 2 added, 1 changed"). Deleted events are cancelled in Life OS and kept as history.
- **Unticking** a calendar removes its events from Life OS (they stay in Google; Event Log: "Source removed: Family — 42 events removed"). Ticking it again imports them again. **Disconnect** removes the events of every synced calendar.
- **Status** (Calendar sources card, Google Calendar part) lists what syncs and the last sync; a calendar that fails three syncs in a row turns the card to Needs attention with Google's error.

**Resync.** Not normally needed: Life OS lists a calendar in full by itself when Google asks for it (an expired sync token). To force a full listing of one calendar and keep your own reminder changes, run on the server:

```sh
docker compose exec calendar wget -qO- http://localhost:8080/v1/google/calendars   # find the calendar's "id"
docker compose exec calendar wget -qO- --post-data= --header='X-Life-OS-Actor: user' 'http://localhost:8080/v1/google/calendars/<id>/sync?full=true'
```

The answer's `sync` says how it went. Unticking and ticking the calendar in the dialog also imports everything again, but drops your own reminder changes on its events and records the removal and the re-import in the Event Log.

The sync interval is `LIFE_OS_CALENDAR_GOOGLE_SYNC_SECONDS` in `.env` (default 120, at least 30), applied with `docker compose up -d calendar`.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| Dialog: "not set up on this server" | One of the three settings is missing or the key is not 32 bytes of base64. Read `docker compose logs calendar`, fix `.env`, `docker compose up -d calendar`. |
| Google: `Error 400: redirect_uri_mismatch` | The client's redirect URI differs from `LIFE_OS_GOOGLE_REDIRECT_URI` (scheme, host, path, trailing slash). Make them identical. |
| Callback: "This sign-in expired or was already used" | More than 10 minutes on Google's page, a reload of the callback, or a second tab. Start again with Connect Google. |
| Status and dialog: "expired or was revoked" | Google refused the refresh token: Life OS was removed at [myaccount.google.com/connections](https://myaccount.google.com/connections), the app was left in Testing (7 days), or the token went unused for 6 months. **Reconnect Google** in the dialog; calendar choices are kept. |
| Status: "Google refused the connection" with `invalid_client` | The client secret changed or the client was deleted. Put the current secret in `.env`, restart Calendar, reconnect if needed. |
| "cannot be decrypted with LIFE_OS_GOOGLE_TOKEN_KEY" | The key changed. Restore the old key, or reconnect Google with the new one. |
| Refresh list: "Google could not be reached" | Calendar has no outbound https or Google is down. The connection is unchanged; try later. |
| Dialog: "Sync failed: Google is unavailable …" | Google or the network failed. Nothing to do: the events from the last good sync stay, and the calendar is retried after 2, 4, 8 … up to 60 minutes. Status warns after three failures in a row. |
| Dialog: "Sync failed: Google no longer has this calendar …" | The calendar was deleted or unshared in Google. **Refresh list**: a calendar Google no longer lists is dropped with its events. |
| A Google event is missing in Life OS | Is its calendar ticked? Events that ended more than a year before the calendar's first sync, and work location markers, are not imported. A sync failure shows in the dialog and on Status. |
| Life OS refuses to change or delete a Google event | Expected: synced events are changed in Google until two-way sync (P11-03). Reminders can be changed. An AI change of such an event waits in Human Review. |

**Rotating the token key:** Disconnect in the dialog, change `LIFE_OS_GOOGLE_TOKEN_KEY`, `docker compose up -d calendar`, Connect again.

**Removing Google:** Disconnect in the dialog (Life OS revokes its access at Google and deletes the stored token and choices), then remove the `LIFE_OS_GOOGLE_*` lines if you like. Disconnecting after the settings are gone only forgets the connection; remove Life OS at [myaccount.google.com/connections](https://myaccount.google.com/connections) as well.
