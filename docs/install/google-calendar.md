# Google Calendar

Life OS can sync with your Google Calendar in both directions. The Google calendars you choose come into Life OS every two minutes, and changing their events in Life OS changes them in Google a few seconds later. Events you create in Life OS go to a "Life OS" calendar that Life OS creates in your Google account, so you see them on your phone's calendar too.

It uses an OAuth client in **your own** Google Cloud project, so nobody else is involved: Google gives the access to your server only.

## 1. Your address and Google

Google sends you back to Life OS after you allow access, to `<your address>/calendar/google/callback`. Google accepts that only on an **https** address, or on `127.0.0.1` or `localhost`:

- **Cloudflare or your own https proxy** ([Access](access.md)): use your https address, such as `https://lifeos.example.com/calendar/google/callback`.
- **SSH tunnel** (the default address `http://127.0.0.1:3000`): use `http://127.0.0.1:3000/calendar/google/callback`, and connect while you use Life OS through the tunnel.
- **A VPN with a plain http address:** Google refuses it. Add `LIFE_OS_GOOGLE_REDIRECT_URI=http://127.0.0.1:3000/calendar/google/callback` to `.env`, register that, and connect once through an SSH tunnel. After that, sync runs on its own whichever way you open Life OS.

## 2. Google Cloud

In the [Google Cloud console](https://console.cloud.google.com/):

1. Create a project, such as "Life OS", and turn on the **Google Calendar API** (APIs & Services → Library).
2. Set up the **consent screen** (Google Auth Platform): user type **External**, then **Publish app** so it is **In production**. In "Testing", Google ends the access after 7 days. Add the scopes `calendar.calendarlist.readonly`, `calendar.events`, and `calendar.app.created`.
3. Create an **OAuth client**: type **Web application**, with the redirect URI from step 1 under **Authorized redirect URIs**. Copy the client ID and the client secret.

Google will warn "Google hasn't verified this app" when you connect. For your own use you don't need its verification: continue with **Advanced → Go to Life OS**.

The [detailed setup](../operations/google-calendar.md) has every screen, written for the reference server.

## 3. On the server

```sh
./setup.sh google
./setup.sh up
```

It asks for the client ID and secret, shows the redirect URI to register, generates the key that encrypts Google's access in Life OS's database, and turns the `google-calendar` [module](modules.md) on. Keep `.env`: with a new key you have to connect again.

## 4. Connect

In Life OS: **Calendar → Google Calendar → Connect Google**. Sign in, allow every permission, and tick the calendars to sync. The Status page then shows Google as connected.

## Disconnect

**Disconnect** in the same dialog removes Life OS's access at Google and deletes it from Life OS. To be sure, also check [your Google account's connections](https://myaccount.google.com/connections). Then `./setup.sh modules off google-calendar` stops the integration altogether.

Problems: [Google Calendar troubleshooting](../operations/google-calendar.md#troubleshooting).
