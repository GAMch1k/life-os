# Access Life OS safely

Life OS has a login of its own ([Accounts and signing in](accounts.md)), and signing in needs **HTTPS**: the session cookie is a `__Host-` cookie, which browsers keep only for a secure page, and passkeys work only there. The one exception is the server itself, `http://127.0.0.1` or `localhost`, which browsers count as secure (an [SSH tunnel](#ssh-tunnel-a-quick-look)). So for your phone and laptop, put an HTTPS front in front of Life OS.

By default (`./setup.sh` without `--bind`), the gateway listens on `127.0.0.1:3000`: only programs on the server itself can reach it, and nothing else on the server publishes a port. The HTTPS front runs on the same server and forwards to the gateway; the gateway stays where it is.

## Choose a way in

| Way | Good for | You need | Phone app |
| --- | --- | --- | --- |
| [Tailscale Serve](proxies/tailscale-serve.md) | **Recommended at home**: your own devices, at home and away | A free Tailscale account. No domain, no open port | Yes, with Tailscale on the phone |
| [Caddy](proxies/caddy.md) | A public domain, or a name on your home network | A domain and ports 80 and 443; or a local name and Caddy's own certificate authority | Public domain: yes. Caddy's own CA: browsers only |
| [Traefik](proxies/traefik.md) | You already run Traefik | A domain | Yes |
| [Nginx Proxy Manager](proxies/nginx-proxy-manager.md) | You already run Nginx Proxy Manager | A domain | Yes |
| [Cloudflare Tunnel and Access](#cloudflare-tunnel-and-access) | From anywhere, with Cloudflare's login in front, no open port | A domain on Cloudflare | Yes, with an Access service token |
| [SSH tunnel](#ssh-tunnel-a-quick-look) | A first look, from a laptop | Your SSH login | No |

Without any of these, Life OS can also [listen on a VPN or LAN address over plain HTTP](#without-https-a-vpn-or-lan-address), with limits.

**Before Life OS faces the internet** (a public domain with Caddy, Traefik, or Nginx Proxy Manager, or the tunnel without Access), turn [legacy access](accounts.md#legacy-access) off, since while it is on anyone who reaches Life OS acts as you, and turn on two-factor sign-in (**Settings → Security**). Over Tailscale only your tailnet's devices reach Life OS, but legacy access still makes each of them you.

## Your own reverse proxy

Every proxy recipe here does the same five things. With another proxy, such as [nginx on the server](../host-nginx-example.conf), do them yourself:

1. **HTTPS**, with a certificate your devices trust.
2. **Forward to the gateway.** A proxy that runs on the server outside Docker forwards to `http://127.0.0.1:3000`. A proxy in a Docker container cannot reach that address: it joins Life OS's `lifeos-proxy` network and forwards to `http://lifeos-gateway:80` ([below](#a-proxy-in-docker)).
3. **Pass the `Host` header** the browser sent, and set `X-Forwarded-For` to the client's address. Caddy, Traefik, Nginx Proxy Manager, and Tailscale Serve do both by default.
4. **Pass the `Authorization` header** unchanged: the phone app's device key travels in it. So no Basic Auth or other login of the proxy's in front of Life OS; Life OS's own sign-in is the gate.
5. **Let uploads and long answers through:** up to 60 MB in one request (a voice note is up to 25 MB, a share up to five 10 MB photos), and up to 5 minutes for an AI tool's streamed answer over MCP, without buffering it.

Then tell Life OS its address and which proxy to believe, and apply it:

```sh
./setup.sh gateway --public-url https://lifeos.example.com --trusted-proxies 10.255.249.48/28
./setup.sh up
```

- `--public-url` sets `LIFE_OS_PUBLIC_URL` and, from it, `LIFE_OS_PUBLIC_HOSTNAME`. The gateway answers only the hostnames it knows, so this must be the name in the browser's address bar, which the proxy passes on as `Host`.
- `--trusted-proxies` sets `LIFE_OS_TRUSTED_PROXIES`: the gateway believes the client's address in `X-Forwarded-For`, and the scheme in `X-Forwarded-Proto`, from these addresses alone, and ignores both from anyone else. Use `10.255.249.48/28`, the `lifeos-proxy` network, for a proxy in Docker, and `host` for a proxy on the server outside Docker; several, comma-separated. Without it, every visitor looks like the proxy: they share one limit on sign-in attempts, one person's wrong passwords slow down everyone's sign-in, and the security log shows only the proxy's address. Never list an address your devices connect from themselves: they could then claim any address. `--trusted-proxies=` empties it.

**Check it.** Open your address, sign in, and open the **Status** page (`/status`): it shows what works and what needs you. Then **Settings → Security log**: your sign-in shows your device's own address (its address on your home network or tailnet, or your public one). If it shows `10.255.249.x` or `172.x.0.1` instead, `LIFE_OS_TRUSTED_PROXIES` is not set, or not applied yet. (A proxy in Docker can itself see `172.x.0.1` for visitors who come over IPv6, when Docker's IPv6 is off: Docker then hides their address from the proxy.)

### A proxy in Docker

Life OS's compose file has a network for your proxy, `lifeos-proxy` (`10.255.249.48/28`), on which the gateway is `lifeos-gateway`. Nothing else of Life OS is on it, so the proxy reaches the gateway and no service. In your proxy's own `compose.yaml`, add it as an external network:

```yaml
services:
  caddy: # or traefik, npm, …
    networks: [default, lifeos-proxy]

networks:
  lifeos-proxy:
    external: true
```

Life OS creates the network when it starts (`./setup.sh up`), so the first time, start Life OS before the proxy. Trusting `10.255.249.48/28` believes everything on that network, and the server itself: attach only your proxy to it.

## SSH tunnel: a quick look

Nothing to set up. From your computer, with the SSH access you already have:

```sh
ssh -N -L 3000:127.0.0.1:3000 you@your-server
```

Keep it running and open <http://127.0.0.1:3000>. If port 3000 is busy on your computer, use another one on the left: `-L 3100:127.0.0.1:3000` and <http://127.0.0.1:3100>.

This is as safe as your SSH login, and signing in works, since the browser sees `127.0.0.1`. It suits a first look, or a laptop, but not a phone.

## Cloudflare Tunnel and Access

Reach Life OS from anywhere without opening a port on the server. Cloudflare Access asks for your email and a one-time code before anything reaches the server; a Cloudflare Tunnel then carries the request to Life OS, which keeps listening on `127.0.0.1`. You need a domain on Cloudflare (a free plan is enough).

**The order matters:** Access must protect the hostname before the tunnel runs.

1. In the [Cloudflare dashboard](https://dash.cloudflare.com/), add your domain and open **Zero Trust** (it asks you to pick a team name).
2. **Access first.** Under **Access → Applications → Add an application → Self-hosted**, add your Life OS hostname, such as `lifeos.example.com`, with a session of 24 hours. Use the **One-time PIN** login, and add an **Allow** policy that includes only your own email address. Access then refuses everyone else.
3. **The tunnel.** Under **Networks → Tunnels**, create a tunnel (type Cloudflared). Skip the install step, but copy the token from the install command (the long value after `--token`). Add a **published application route**: your hostname → service type **HTTP**, URL **`gateway:80`**. Turn on **Enforce Access JSON Web Token (JWT) validation** for that route, with your application.
4. **On the server:**

   ```sh
   ./setup.sh cloudflare
   ./setup.sh up
   ```

   It asks for the address (`https://lifeos.example.com`) and the token, and checks that an anonymous request to the hostname is sent to the Cloudflare login. If not, it saves the token but leaves the tunnel off; fix Access and run it again. Once Access passes, it turns the `cloudflare` [module](modules.md) on: the connector runs from the next `./setup.sh up`. `./setup.sh modules off cloudflare` stops it (turn `ntfy` off first, if it is on).

5. Open `https://lifeos.example.com`, enter your email and the code you receive, then sign in to Life OS. Check it as for [a proxy](#your-own-reverse-proxy): the Status page, and your address in the security log.

The token is a credential: anyone holding it can run your tunnel. The setup script keeps it only in `.env`.

- **Access and Life OS's sign-in.** Access decides who reaches Life OS's login; Life OS's sign-in decides who gets in. Through the tunnel the gateway refuses a request that did not pass Access. `LIFE_OS_REQUIRE_CLOUDFLARE_ACCESS=off` in `.env` lets Life OS's sign-in alone decide; then do what [Before Life OS faces the internet](#choose-a-way-in) says first.
- **The client's address** needs nothing: the gateway takes it from Cloudflare's own header, and only from the tunnel's network. Leave the tunnel out of `LIFE_OS_TRUSTED_PROXIES`.
- **Uploads and timeouts.** Cloudflare's free plan accepts uploads up to 100 MB, above Life OS's 60 MB, and gives up (error 524) on an answer that has not started within 100 seconds.
- **Phone and watch apps** pair with device keys; while Access is on, the phone also needs an Access service token: see [Phone and watch apps](phone-apps.md). Push notifications through ntfy need a second hostname in the same tunnel: see [Notifications](notifications.md#ntfy).

How it works, how to check and diagnose it, and how to rotate the token: [Cloudflare Tunnel and Access](../operations/cloudflare.md) (written for the reference server; its hostnames are examples).

## Without HTTPS: a VPN or LAN address

If none of the ways above suits you, Life OS can listen on a private address of the server: its WireGuard or Tailscale address, or its address on your home network.

```sh
./setup.sh gateway --bind 10.0.0.1          # the server's WireGuard address
./setup.sh gateway --bind 100.101.102.103   # or its Tailscale address (tailscale ip -4)
./setup.sh gateway --bind 192.168.1.20      # or its address on your home network
./setup.sh up
```

Then open `http://<that address>:3000`. `./setup.sh` refuses public addresses here, and accepts private ones (`10.x`, `172.16–31.x`, `192.168.x`, and Tailscale's `100.64–127.x`) with a warning.

Over plain HTTP, the login refuses to sign you in, because anyone on the way could read your password and your session. A VPN encrypts the way between your devices and the server; a home network's Wi-Fi and cables do not. To sign in at such an address anyway, list it in `LIFE_OS_INSECURE_LOGIN_HOSTS` in `.env` ([Signing in](accounts.md#signing-in)) and apply it with `./setup.sh up`. Keep in mind:

- **Passkeys never work** over plain HTTP, and the **phone app refuses** `http://` servers. Browsers install Life OS on the home screen, and share to it, only over HTTPS.
- **Everyone who reaches the address reaches the login.** On a VPN, only add devices you own. On a network other people share, such as an office or a shared flat's Wi-Fi, use HTTPS instead: [Caddy with its own certificate authority](proxies/caddy.md#a-name-on-your-home-network) needs no domain.
- **Other containers on the server** can reach that address too. On a server shared with other projects, add the firewall rule from [Networking: other containers on the host](../architecture/networking.md#other-containers-on-the-host).
- **Names.** Life OS answers only the names it knows: `127.0.0.1`, `localhost`, the address it listens on, and the hostname of its address. To open it by a name, such as a Tailscale MagicDNS name, make that its address: `./setup.sh gateway --public-url http://myserver:3000`.

## Never

- Never publish the gateway's port (3000) to the internet, or forward a port on your router to it: forward to your HTTPS proxy instead. `./setup.sh` refuses a public `--bind` address unless you add `--allow-public-bind`, and then warns at every start.
- Never list in `LIFE_OS_TRUSTED_PROXIES` a network your own devices connect from: they could then make Life OS see any address they like.
