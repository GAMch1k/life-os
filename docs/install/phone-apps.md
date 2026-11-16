# Phone and watch apps

Life OS has an Android app and a Wear OS watch app (Galaxy Watch and others), in their own repository, [GAMch1k/life-os-app](https://github.com/GAMch1k/life-os-app). The phone app shows Today (tasks and events), captures text, photos, and voice notes (offline too: they are sent when the phone is back online), shows notifications, and sets up the watch. The watch app records voice notes and shows what is next, as tiles and complications.

You don't need them: the Life OS web app works in any phone browser.

## Install

The apps are installed from their APK files, not from Google Play, from the [life-os-app releases page](https://github.com/GAMch1k/life-os-app/releases). Take the latest release (until the first one is published, the page is empty). On the phone, download `lifeos-phone-….apk` from it and open it; Android asks you to allow installing apps from your browser. The phone app installs the watch app for you (Settings › Watch).

The release notes list each APK's SHA-256. Both apps are signed with the Life OS release key; to check a download before installing it, `apksigner verify --print-certs lifeos-phone-….apk` must show the certificate SHA-256 given in the [app's README](https://github.com/GAMch1k/life-os-app#signing).

## Connect to your server

From app **0.5.0**, the phone **pairs with your Life OS account**: the phone and its watch get keys of their own, which Life OS checks on every request, and Cloudflare Access is no longer needed. Life OS needs an **https** public address (`LIFE_OS_PUBLIC_URL`) that the phone reaches: Cloudflare Tunnel, your own proxy with a certificate, or an https address over your VPN. The app refuses plain `http://` servers, `http://localhost` included.

1. At the first start, the app offers the official server: replace it with your own address, such as `https://lifeos.example.com`. To change it later: **Settings › Connection › Edit connection**.
2. Pair the phone, in one of two ways:
   - **With a second screen**: in the Dashboard, **Settings → Devices → Pair a phone** shows a QR code, and the code as text. In the app, scan the QR code or type the code. The app shows the server's address and asks before it pairs.
   - **On the phone alone**: **Sign in on this phone** opens Life OS's sign-in page in a Custom Tab. Sign in (if you are signed in already, you skip this step) and tap **Pair**. The app pairs right away, without asking again.

   A pairing code works once, for 5 minutes. Making one needs a sign-in, or your password, from the last 12 hours. If it has been longer, the page asks for your password.
3. The watch gets a key of its own from the phone (**Settings › Watch** in the app).

The Dashboard opens in a Custom Tab and signs in through the web login, as in a browser. The share sheet sends what you share to Inbox with the phone's key.

**Settings → Devices** in the Dashboard lists the phone and, under it, its watch, each with when it was last used. **Revoke** signs one out (a phone together with its watch), and their push notifications stop. These also sign out every device:

- **Sign out everywhere**;
- a password reset;
- a password change, unless you untick "Also sign out my devices, API tokens, and connected apps".

Once your phone and watch are paired, turn [legacy access](accounts.md#legacy-access) off (**Settings → Security**). How pairing works: [Accounts → Settings → Devices](accounts.md#settings--devices). For app developers: [Devices and tokens](../api/devices.md).

**Cloudflare Access, as an optional outer layer.** If Life OS is behind [Cloudflare Tunnel and Access](access.md#cloudflare-tunnel-and-access), Access stays in front, and the phone also needs an Access **service token**:

1. Make a service token for the phone in Cloudflare Zero Trust.
2. Add a **Service Auth** policy for it on your Life OS application. The app's "How to get a token" shows each step; they are also in the [app's README](https://github.com/GAMch1k/life-os-app#first-run).
3. In the app, paste the token's Client ID and Client Secret and tap **Connect**.
4. Then pair: Access must let the pairing request through.

The watch takes the token from the phone. To shut a phone out at Access too, delete its service token in Cloudflare.

**Apps older than 0.5.0** know only the Access service token. They work only while legacy access is on. Update them (below) and pair before you turn legacy access off.

## Notifications

To get Life OS's notifications as pushes from the app: [Notifications → The Life OS phone app](notifications.md#the-life-os-phone-app).

## Updates

Connected to your server, the phone app updates itself, and the watch app, from the latest [life-os-app release](https://github.com/GAMch1k/life-os-app/releases): it checks when it starts and once a day on Wi-Fi, and **Settings › Updates › Check for updates** checks at once (it says where updates come from). It installs a release only when the APK has the size and SHA-256 its `updates.json` gives and is signed with the same key as the installed app, and never an older version; it downloads only from GitHub, without sending your token there. The first update asks you to confirm once (your browser installed the app); later ones install without asking. Apps from 0.4.5 or older don't know about GitHub Releases yet: install the newest release by hand once, over the old one (your settings stay), and it updates itself from then on.

## The web app on a phone

Over https (Cloudflare, or your own proxy), Life OS can be installed on the phone's home screen from the browser's menu (**Add to Home screen** or **Install app**). It then appears in the phone's share menu, so links, text, and photos go straight into Inbox. Browsers allow this only over https, not over a plain http VPN address.
