# Google Calendar setup

Life OS connects to the owner's one Google account through an OAuth client in the owner's own Google Cloud project. The client credentials and the token key live only in the server's ignored `.env`; Calendar stores the refresh token encrypted in its database. How the connection works is described in [Calendar: Google Calendar connection](../services/calendar.md#google-calendar-connection).

Once connected, the calendars you tick sync both ways: their events come into Life OS every two minutes, and editing them in Life OS changes them in Google. Events you create in Life OS go to a dedicated "Life OS" calendar that Life OS creates in your Google account (see [Calendar: Google Calendar sync](../services/calendar.md#google-calendar-sync) and [two-way sync](../services/calendar.md#google-calendar-two-way-sync)).

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
- **In Calendar** the events carry the calendar's name (a "Google · Owner" badge) and say "In Google: Owner · Synced both ways". Events of calendars you may edit can be edited, moved, and deleted here: the change is written to Google first, and the event then shows what Google saved. Changes made in Google show here within two minutes. Calendars you can only read (holidays, shared read-only calendars) stay read-only here with the reason; only their reminders and priority can be changed, in Life OS alone. A recurring Google event repeats like one created in Life OS (with its skipped and changed occurrences) and takes the same "This event / This and following / All events" choices; a rule Life OS cannot hold, such as "every second Tuesday", shows as separate read-only events for the next 400 days. Work location markers (Home, Office) are not imported.
- **Changed on both sides at once:** if an event changed in Google since Life OS last read it, Life OS does not overwrite it: the save fails with "Changed in Google meanwhile; showing the latest version", the event shows Google's version, and the Event Log records "Changed in Google meanwhile". Make your change again if you still want it.
- **Afterwards** every two minutes Life OS asks Google only for what changed. Syncs that change nothing leave no trace; one that changes something adds one Event Log entry ("Source refreshed: Family — 2 added, 1 changed"). Deleted events are cancelled in Life OS and kept as history.
- **Unticking** a calendar removes its events from Life OS (they stay in Google; Event Log: "Source removed: Family — 42 events removed"). Ticking it again imports them again. **Disconnect** removes the events of every synced calendar.
- **Status** (Calendar sources card, Google Calendar part) lists what syncs, the last sync, and the export ("up to date in “Life OS”", "3 waiting", "2 failing, retried automatically"); a calendar that fails three syncs in a row turns the card to Needs attention with Google's error.

## 7. The Life OS calendar

With **Export events created in Life OS** on (the default, in the Google dialog's "Life OS calendar" section):

- **Within a few minutes of connecting** (or of deploying P11-03 for a connection made earlier) Life OS creates a calendar named **Life OS** in your Google account, in your timezone, and copies every Life OS event that is still ahead into it, repeating events included; past events stay in Life OS. It appears in the dialog's calendar list as "Created by Life OS · Owner", ticked, and the Event Log shows "Life OS calendar created in Google". Give it a color on your phone, or hide it there.
- **Every event you create or change in Life OS** (in the Dashboard or by capture) is copied there within moments; its details say "In Google: Life OS" with "Exported", "Waiting to be exported", or "Export failed: … Retrying automatically." while Google cannot be reached (the change is kept in Life OS and retried after 2, 4, 8 … up to 60 minutes; the Event Log records "Export to Google failed" once per streak). Its reminders are copied as phone notifications (at most five, minutes before; a "day before at 19:00" reminder only on all-day events). Cancelling or deleting the event removes the copy.
- **Changing a copy on your phone** (moving it, renaming it, skipping one occurrence) changes the Life OS event within two minutes; deleting it there cancels the Life OS event (restore it in Life OS to copy it again). Reminder changes made on the phone are not brought back.
- **Deleting the whole Life OS calendar** in Google: the next export creates it again, copies your events into it again, and says so in the dialog and on Status ("The Life OS calendar was deleted in Google; Life OS created it again …").
- **Export off:** nothing more is written to the Life OS calendar; what is there stays. Switching it on again copies the upcoming events. Disconnecting Google leaves the calendar in Google; connecting again adopts it instead of creating a second one.

## Checking two-way sync with your own calendars

After deploying P11-03, with Google connected:

1. Open Calendar → Google Calendar: the "Life OS calendar" section shows the toggle on and, within a few minutes, "Up to date in “Life OS”"; the calendar list shows "Life OS · Created by Life OS · Owner", ticked. On your phone a "Life OS" calendar appears with your upcoming Life OS events.
2. Create an event in Life OS for tomorrow: its details say "In Google: Life OS · Exported" within moments, and it appears on your phone.
3. Move that copy on your phone by an hour: within two minutes it moves in Life OS (Event Log: "Event rescheduled … by Google Calendar sync").
4. Open an event of your own Google calendar in Life OS: no read-only note, "In Google: <calendar> · Synced both ways". Rename it: the phone shows the new name.
5. Rename the same event on your phone, then (before the next sync) rename it in Life OS: Life OS answers "Changed in Google meanwhile; showing the latest version" and shows the phone's name; the Event Log shows "Changed in Google meanwhile".
6. Open a holiday (a calendar you can only read, if ticked): it stays read-only with the reason; only reminders and priority can be edited.
7. Skip one occurrence of a repeating Google event ("This event"): it disappears from that day on your phone.

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
| Life OS refuses to change or delete a Google event | The event's details say why: a calendar you can only read, or one occurrence of a rule Life OS cannot hold ("every second Tuesday"); change it in Google. Reminders and priority can be changed. An AI change of such an event waits in Human Review. |
| Save fails: "Changed in Google meanwhile; showing the latest version" | Someone (or your phone) changed the event in Google since Life OS read it. The event now shows Google's version; make your change again. |
| Save fails: "Google could not be reached" or "Google access expired" | Nothing was changed, in Google or in Life OS. Try again later, or reconnect Google. Life OS events are not affected: their changes wait in the export and go out later. |
| Event details: "Export failed: …" | Google or the network failed; the change is kept and retried automatically. Status shows the count. A lasting "Life OS could not create its calendar in Google: …" usually means the "manage the calendars Life OS creates" permission was unticked: reconnect Google and allow it. |
| A Life OS event is missing on the phone | Is the export on (Google dialog, Life OS calendar)? Is the "Life OS" calendar shown on the phone? Past events are not exported. |

**Rotating the token key:** Disconnect in the dialog, change `LIFE_OS_GOOGLE_TOKEN_KEY`, `docker compose up -d calendar`, Connect again.

**Removing Google:** Disconnect in the dialog (Life OS revokes its access at Google and deletes the stored token and choices), then remove the `LIFE_OS_GOOGLE_*` lines if you like. Disconnecting after the settings are gone only forgets the connection; remove Life OS at [myaccount.google.com/connections](https://myaccount.google.com/connections) as well.
