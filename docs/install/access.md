# Access Life OS safely

Life OS has no login of its own yet. Whoever can open it acts as you: they can read and change everything, use your AI sign-in, and see your bank data. So the address Life OS listens on, and what stands in front of it, is your security. Choose one of the ways below.

By default (`./setup.sh` without `--bind`), the gateway listens on `127.0.0.1:3000`: only programs on the server itself can reach it. Nothing else on the server publishes a port.

## SSH tunnel: a quick look

Nothing to set up. From your computer, with the SSH access you already have:

```sh
ssh -N -L 3000:127.0.0.1:3000 you@your-server
```

Keep it running and open <http://127.0.0.1:3000>. If port 3000 is busy on your computer, use another one on the left: `-L 3100:127.0.0.1:3000` and <http://127.0.0.1:3100>.

This is as safe as your SSH login. It suits a first look, or a laptop, but not a phone.

## A VPN: WireGuard or Tailscale

With a VPN, your own devices reach the server over a private network. Let Life OS listen on the server's VPN address:

```sh
./setup.sh gateway --bind 10.0.0.1     # the server's WireGuard address
./setup.sh gateway --bind 100.101.102.103   # or its Tailscale address (tailscale ip -4)
./setup.sh up
```

Then open `http://<that address>:3000` from any device on the VPN. `./setup.sh` refuses public addresses here, and accepts private ones (`10.x`, `172.16–31.x`, `192.168.x`, and Tailscale's `100.64–127.x`) with a warning.

Keep in mind:

- **Every device on the VPN is you.** Only add devices you own. Never use an address on a network other people share, such as your home or office LAN.
- **Other containers on the server** can reach that address too. On a server shared with other projects, add the firewall rule from [Networking: other containers on the host](../architecture/networking.md#other-containers-on-the-host).
- **Plain http limits the phone.** Browsers only allow installing Life OS on the home screen, and sharing to it, over https. Over a VPN, use it in the browser.
- **Names.** Life OS answers only the names it knows: `127.0.0.1`, `localhost`, the address it listens on, and the hostname of its address. To open it by a name, such as a Tailscale MagicDNS name, make that its address: `./setup.sh gateway --public-url http://myserver:3000`.

## Cloudflare Tunnel and Access

The way to reach Life OS from anywhere, the phone apps included, without opening a port on the server. Cloudflare Access asks for your email and a one-time code before anything reaches the server; a Cloudflare Tunnel then carries the request to Life OS, which keeps listening on `127.0.0.1`. You need a domain on Cloudflare (a free plan is enough).

**The order matters:** Access must protect the hostname before the tunnel runs.

1. In the [Cloudflare dashboard](https://dash.cloudflare.com/), add your domain and open **Zero Trust** (it asks you to pick a team name).
2. **Access first.** Under **Access → Applications → Add an application → Self-hosted**, add your Life OS hostname, such as `lifeos.example.com`, with a session of 24 hours. Use the **One-time PIN** login, and add an **Allow** policy that includes only your own email address. Access then refuses everyone else.
3. **The tunnel.** Under **Networks → Tunnels**, create a tunnel (type Cloudflared). Skip the install step, but copy the token from the install command (the long value after `--token`). Add a **published application route**: your hostname → service type **HTTP**, URL **`gateway:80`**. Turn on **Enforce Access JSON Web Token (JWT) validation** for that route, with your application.
4. **On the server:**

   ```sh
   ./setup.sh cloudflare
   ./setup.sh up
   ```

   It asks for the address (`https://lifeos.example.com`) and the token, and checks that an anonymous request to the hostname is sent to the Cloudflare login. If not, it saves the token but leaves the tunnel off; fix Access and run it again.

5. Open `https://lifeos.example.com`, enter your email, then the code you receive.

The token is a credential: anyone holding it can run your tunnel. The setup script keeps it only in `.env`. To add a person, add their email to the Allow policy; they share all your data, since Life OS has one user.

The phone and watch apps get through Access with a service token: see [Phone and watch apps](phone-apps.md). Push notifications through ntfy need a second hostname in the same tunnel: see [Notifications](notifications.md#ntfy).

How it works, how to check and diagnose it, and how to rotate the token: [Cloudflare Tunnel and Access](../operations/cloudflare.md) (written for the reference server; its hostnames are examples).

## Your own reverse proxy

If you already run a reverse proxy with TLS, such as Caddy or nginx, keep Life OS on `127.0.0.1` and let the proxy forward to `http://127.0.0.1:3000`. The proxy **must** ask for a login (Basic Auth, or a single sign-on in front of it), and pass the original `Host` header. Then tell Life OS its address:

```sh
./setup.sh gateway --public-url https://lifeos.example.com
./setup.sh up
```

There is an [nginx example](../host-nginx-example.conf). The phone apps don't support a login other than Cloudflare Access yet.

## Never

- Never listen on a public address without something in front of it. `./setup.sh` refuses it unless you add `--allow-public-bind`, and then warns at every start.
- Never forward the port on your router to Life OS.
