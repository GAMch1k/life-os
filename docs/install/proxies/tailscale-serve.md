# Tailscale Serve

The recommended way at home. [Tailscale](https://tailscale.com/) joins your server, laptop, and phone into a private network, your tailnet, wherever each one is. **Tailscale Serve** then gives Life OS an HTTPS address on it, such as `https://myserver.tail1234.ts.net`, with a certificate every device trusts: no domain to buy, no port to open on your router, and nobody outside your tailnet can reach it.

This page follows Tailscale's own documentation ([Tailscale Serve](https://tailscale.com/kb/1312/serve), [Enabling HTTPS](https://tailscale.com/kb/1153/enabling-https), [the `tailscale serve` command](https://tailscale.com/kb/1242/tailscale-serve)). Life OS's tests can't run it, since that needs a tailnet: if a step differs for you, Tailscale's pages win.

## What you need

- Tailscale on the server (installed on the server itself, not in a container) and on every device you will use Life OS from, signed in to the same tailnet.
- In the [admin console](https://login.tailscale.com/admin/dns), on the **DNS** page: **MagicDNS** on (it is, for tailnets made since October 2022), and under **HTTPS Certificates**, **Enable HTTPS**. Tailscale says before you confirm that your machines' names and your tailnet's name then appear in a public ledger of certificates: name the server so that this does not matter.

## Set it up

1. On the server, serve the gateway on HTTPS, in the background (it comes back after a restart):

   ```sh
   sudo tailscale serve --bg 3000
   ```

   It prints the address, such as `https://myserver.tail1234.ts.net`, and proxies it to `http://127.0.0.1:3000`, where the gateway listens by default. `tailscale serve status` shows it again.

2. Tell Life OS its address, and to believe the client addresses Tailscale passes on, then apply it:

   ```sh
   ./setup.sh gateway --public-url https://myserver.tail1234.ts.net --trusted-proxies host
   ./setup.sh up
   ```

   `host` means a proxy on the server itself, outside Docker: Tailscale reaches the gateway from Docker's own address for the server, which the gateway finds by itself. Tailscale passes on the address you opened (as `Host`), your device's tailnet address (as `X-Forwarded-For`, replacing any a client sent), and `https`.

3. Open the address on a device of your tailnet, sign in, and check it: the **Status** page, and your device's `100.x` address in **Settings → Security log** ([Check it](../access.md#your-own-reverse-proxy)).

To stop serving: `sudo tailscale serve --https=443 off` (or `sudo tailscale serve reset`, which removes every Serve setting of the server).

## Good to know

- **Uploads and long answers.** Serve sets no limit on uploads and no timeout of its own, and passes streamed answers on as they come.
- **Phone app.** Install Tailscale on the phone and keep it connected; then the Life OS app works with the `https://….ts.net` address ([Phone and watch apps](../phone-apps.md)). The watch sends its requests through the phone, so it works while the phone is near; on its own, over Wi-Fi or LTE, it cannot reach a tailnet address.
- **Who gets in.** Only your tailnet's devices, and people you share the server with in Tailscale, reach Life OS, and then Life OS's sign-in. Don't turn on **Tailscale Funnel** for this port: Funnel publishes it to the whole internet. If you do anyway, do what [Before Life OS faces the internet](../access.md#choose-a-way-in) says first.
- **Without sudo.** `sudo tailscale set --operator=$USER` once lets your user run `tailscale serve`.
- **Tailscale's headers.** For a signed-in tailnet user, Serve also adds `Tailscale-User-Login` and `Tailscale-User-Name`; Life OS doesn't use them, and its own sign-in still decides.
