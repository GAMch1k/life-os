# Phone and watch apps

Life OS has an Android app and a Wear OS watch app (Galaxy Watch and others), in their own repository, [GAMch1k/life-os-app](https://github.com/GAMch1k/life-os-app). The phone app shows Today (tasks and events), captures text, photos, and voice notes (offline too: they are sent when the phone is back online), shows notifications, and sets up the watch. The watch app records voice notes and shows what is next, as tiles and complications.

You don't need them: the Life OS web app works in any phone browser.

## Install

The apps are installed from their APK files, not from Google Play, from the [life-os-app releases page](https://github.com/GAMch1k/life-os-app/releases). Take the latest release (until the first one is published, the page is empty). On the phone, download `lifeos-phone-….apk` from it and open it; Android asks you to allow installing apps from your browser. The phone app installs the watch app for you (Settings › Watch).

The release notes list each APK's SHA-256. Both apps are signed with the Life OS release key; to check a download before installing it, `apksigner verify --print-certs lifeos-phone-….apk` must show the certificate SHA-256 given in the [app's README](https://github.com/GAMch1k/life-os-app#signing).

## Connect to your server

Today, the apps need Life OS behind [Cloudflare Tunnel and Access](access.md#cloudflare-tunnel-and-access), at an https address, and a Cloudflare Access **service token** for the phone. Without Cloudflare, use the web app in the phone's browser over your VPN instead.

1. At the first start, the app offers the official server: replace it with your own address, such as `https://lifeos.example.com`. To change it later: **Settings › Connection › Edit connection**.
2. Make a service token for the phone in Cloudflare Zero Trust, and a **Service Auth** policy for it on your Life OS application. The app's "How to get a token" shows each step; they are also in the [app's README](https://github.com/GAMch1k/life-os-app#first-run).
3. Paste the token's Client ID and Client Secret into the app and tap **Connect**. It tries them and opens Today.

The watch takes the server and the token from the phone. To revoke a phone, delete its service token in Cloudflare.

**Pairing with your account.** Life OS can pair a phone with your account (**Settings → Devices**, a QR code, or signing in from the app), giving it and its watch keys of their own; Cloudflare Access's service token, where Access is in front, stays as well. App versions that pair are on their way; until yours does, keep [legacy access](accounts.md#legacy-access) on. Once your phone and watch are paired, turn legacy access off. How it works: [Accounts → Settings → Devices](accounts.md#settings--devices); for app developers: [Devices and tokens](../api/devices.md).

## Notifications

To get Life OS's notifications as pushes from the app: [Notifications → The Life OS phone app](notifications.md#the-life-os-phone-app).

## Updates

Connected to your server, the phone app updates itself, and the watch app, from the latest [life-os-app release](https://github.com/GAMch1k/life-os-app/releases): it checks when it starts and once a day on Wi-Fi, and **Settings › Updates › Check for updates** checks at once (it says where updates come from). It installs a release only when the APK has the size and SHA-256 its `updates.json` gives and is signed with the same key as the installed app, and never an older version; it downloads only from GitHub, without sending your token there. The first update asks you to confirm once (your browser installed the app); later ones install without asking. Apps from 0.4.5 or older don't know about GitHub Releases yet: install the newest release by hand once, over the old one (your settings stay), and it updates itself from then on.

## The web app on a phone

Over https (Cloudflare, or your own proxy), Life OS can be installed on the phone's home screen from the browser's menu (**Add to Home screen** or **Install app**). It then appears in the phone's share menu, so links, text, and photos go straight into Inbox. Browsers allow this only over https, not over a plain http VPN address.
