# Caddy

[Caddy](https://caddyserver.com/) gets and renews certificates by itself. Use it with a **public domain** (Let's Encrypt, so every device trusts it, the phone app included), or with **a name on your home network** and Caddy's own certificate authority (no domain, nothing reachable from the internet, but each device must trust Caddy's certificate, and the phone app does not).

The recipes run Caddy in Docker, next to Life OS; [Caddy on the server itself](#caddy-on-the-server-itself) is at the end. `tools/test-proxy-recipes` runs this setup, Caddy's own certificate authority included, in front of the gateway in CI.

## Caddy in Docker

A directory of its own, such as `~/caddy`, with this `compose.yaml`:

```yaml
services:
  caddy:
    image: caddy:2.11.9-alpine
    restart: unless-stopped
    # IPv4 only, unless Docker's IPv6 is on (see Good to know).
    ports: ["0.0.0.0:80:80", "0.0.0.0:443:443", "0.0.0.0:443:443/udp"]
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy-data:/data
      - caddy-config:/config
    networks: [default, lifeos-proxy]

networks:
  lifeos-proxy:
    external: true

volumes:
  caddy-data:
  caddy-config:
```

`lifeos-proxy` is Life OS's network for your proxy, on which the gateway is `lifeos-gateway` ([A proxy in Docker](../access.md#a-proxy-in-docker)). Keep `caddy-data`: it holds the certificates, and Caddy's own certificate authority.

### A public domain

Point the domain's DNS (an `A` record) at the server, open ports 80 and 443 to it (on your router, for a server at home), and write the `Caddyfile`:

```caddyfile
lifeos.example.com {
	reverse_proxy lifeos-gateway:80
}
```

Start it with `docker compose up -d` in that directory. Caddy gets the certificate on its own. Life OS's login is then on the internet: do what [Before Life OS faces the internet](../access.md#choose-a-way-in) says first.

### A name on your home network

Pick a name under `home.arpa` (the name reserved for home networks), such as `lifeos.home.arpa`, and point it at the server's address on your network: in your router's or Pi-hole's local DNS, or in each device's hosts file. Publish port 443 on that address only, `ports: ["192.168.1.20:443:443"]`, and write the `Caddyfile`:

```caddyfile
lifeos.home.arpa {
	tls internal
	reverse_proxy lifeos-gateway:80
}
```

`tls internal` makes Caddy sign the certificate with its own certificate authority. Start Caddy, copy that authority's root certificate out, and install it on each device as a trusted certificate authority (on Android, search **Settings** for "CA certificate"; on a computer, the system's certificate store, and Firefox's own):

```sh
docker compose cp caddy:/data/caddy/pki/authorities/local/root.crt ./caddy-root.crt
```

The root lasts ten years; Caddy renews the rest by itself. Browsers then trust `https://lifeos.home.arpa`. **The phone app does not**: it accepts only certificates from the authorities Android comes with. Use the browser on the phone, or a public domain or [Tailscale Serve](tailscale-serve.md) for the app.

## Tell Life OS

```sh
./setup.sh gateway --public-url https://lifeos.example.com --trusted-proxies 10.255.249.48/28
./setup.sh up
```

With the name you chose. `10.255.249.48/28` is the `lifeos-proxy` network: the gateway believes the client's address that Caddy passes on in `X-Forwarded-For`, from there only. Then check it: sign in, the **Status** page, and your device's address in **Settings → Security log** ([Check it](../access.md#your-own-reverse-proxy)).

## Good to know

- **Host and the client's address.** Caddy passes the browser's `Host` on unchanged, sets `X-Forwarded-For` to the client's address, ignoring one a client sent itself, and `X-Forwarded-Proto` to `https`.
- **Uploads and long answers.** Caddy limits neither the size of an upload nor how long an answer takes, and passes streamed answers on at once. Nothing to set.
- **Phone app.** Caddy passes `Authorization` on, so device keys work. Don't add `basic_auth` or `forward_auth` in front of Life OS.
- **IPv6.** With Docker's IPv6 off (the default), a port published on every address takes IPv6 visitors in through Docker's own bridge address: Caddy, and so Life OS, sees all of them as that one address, and they share its sign-in limits. Publish on IPv4 only, as above, and give the hostname no `AAAA` record, or turn on IPv6 in Docker.
- **Caddy's admin endpoint** listens on `localhost:2019` inside its container, and is not published: leave it so.

## Caddy on the server itself

Caddy installed from a package runs on the server, outside Docker. It reaches the gateway where it listens by default:

```caddyfile
lifeos.example.com {
	reverse_proxy 127.0.0.1:3000
}
```

and Life OS believes it as `host`, Docker's address for the server: `./setup.sh gateway --public-url https://lifeos.example.com --trusted-proxies host`. `host` works only with the gateway on `127.0.0.1`, its default, and Docker running as root (the usual install): `./setup.sh gateway` refuses it with another `--bind`, and the gateway leaves it out. With rootless Docker or Docker Desktop, run the proxy in Docker on `lifeos-proxy` instead. For `tls internal` there, `sudo caddy trust` installs the root on the server, and the root is in Caddy's data directory (`/var/lib/caddy/.local/share/caddy/pki/authorities/local/root.crt` for Debian's package).
