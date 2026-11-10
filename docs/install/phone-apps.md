# Phone and watch apps

Life OS has an Android app and a Wear OS watch app (Galaxy Watch and others), in their own repository, [GAMch1k/life-os-app](https://github.com/GAMch1k/life-os-app). The phone app shows Today (tasks and events), captures text, photos, and voice notes (offline too: they are sent when the phone is back online), shows notifications, and sets up the watch. The watch app records voice notes and shows what is next, as tiles and complications.

You don't need them: the Life OS web app works in any phone browser.

## Install

The apps are installed from their APK files, not from Google Play. They will be published on the [life-os-app releases page](https://github.com/GAMch1k/life-os-app/releases) with a later preview; until then, that page may be empty. On the phone, download the phone app's APK and open it; Android asks you to allow installing apps from your browser. The phone app installs the watch app for you (Settings › Watch).

## Connect to your server

Today, the apps need Life OS behind [Cloudflare Tunnel and Access](access.md#cloudflare-tunnel-and-access), at an https address, and a Cloudflare Access **service token** for the phone. Without Cloudflare, use the web app in the phone's browser over your VPN instead.

1. At the first start, the app offers the official server: replace it with your own address, such as `https://lifeos.example.com`. To change it later: **Settings › Connection › Edit connection**.
2. Make a service token for the phone in Cloudflare Zero Trust, and a **Service Auth** policy for it on your Life OS application. The app's "How to get a token" shows each step; they are also in the [app's README](https://github.com/GAMch1k/life-os-app#first-run).
3. Paste the token's Client ID and Client Secret into the app and tap **Connect**. It tries them and opens Today.

The watch takes the server and the token from the phone. To revoke a phone, delete its service token in Cloudflare.

## Notifications

To get Life OS's notifications as pushes from the app: [Notifications → The Life OS phone app](notifications.md#the-life-os-phone-app).

## Updates

The apps look for updates at the official server, which yours can't use. How they update on other servers is being changed for a later preview (updates from the life-os-app releases page). Until then, install a newer version from that page by hand: it installs over the old one and keeps your settings.

## The web app on a phone

Over https (Cloudflare, or your own proxy), Life OS can be installed on the phone's home screen from the browser's menu (**Add to Home screen** or **Install app**). It then appears in the phone's share menu, so links, text, and photos go straight into Inbox. Browsers allow this only over https, not over a plain http VPN address.
