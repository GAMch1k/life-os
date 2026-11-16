# Nginx Proxy Manager

For a server that already runs [Nginx Proxy Manager](https://nginxproxymanager.com/) in Docker, with Let's Encrypt certificates for a domain. This page follows Nginx Proxy Manager 2.17's own configuration; Life OS's tests don't run it.

## Nginx Proxy Manager

Its container joins Life OS's network for your proxy, `lifeos-proxy`, on which the gateway is `lifeos-gateway` ([A proxy in Docker](../access.md#a-proxy-in-docker)). If you have no Nginx Proxy Manager yet, a directory of its own, such as `~/npm`, with this `compose.yaml`:

```yaml
services:
  npm:
    image: jc21/nginx-proxy-manager:2.17.0
    restart: unless-stopped
    # 80 and 443 on IPv4 only, unless Docker's IPv6 is on (below); the admin
    # page, 81, on the server itself only.
    ports: ["0.0.0.0:80:80", "0.0.0.0:443:443", "127.0.0.1:81:81"]
    environment:
      # Don't believe CDNs' ranges (below).
      IP_RANGES_FETCH_ENABLED: "false"
    volumes:
      - ./data:/data
      - ./letsencrypt:/etc/letsencrypt
    networks: [default, lifeos-proxy]

networks:
  lifeos-proxy:
    external: true
```

With one you already have, add the `lifeos-proxy` network, `IP_RANGES_FETCH_ENABLED`, and those `ports` to it as above.

**The admin page, port 81, never on the network.** It is plain HTTP, Docker's published ports get past a firewall such as `ufw`, and a new Nginx Proxy Manager makes whoever opens it first its admin: that person could point your Life OS hostname at a server of theirs and collect your password and two-factor codes. Publish it on `127.0.0.1` as above, and reach it through SSH from your computer:

```sh
ssh -N -L 8181:127.0.0.1:81 you@your-server
```

then open <http://127.0.0.1:8181> and set up the admin account at once.

Point the domain's DNS at the server and open ports 80 and 443 to it. Life OS's login is then on the internet: do what [Before Life OS faces the internet](../access.md#choose-a-way-in) says first.

## The proxy host

In the admin page, **Hosts → Proxy Hosts → Add Proxy Host**:

- **Details:** Domain Names `lifeos.example.com`; Scheme `http`; Forward Hostname / IP `lifeos-gateway`; Forward Port `80`. Leave **Block Common Exploits off**: it refuses some of Life OS's own addresses, such as `/login?next=/android/`. Websockets Support makes no difference. Access List: **Publicly Accessible**. An access list with users asks for a password of its own and takes away the `Authorization` header the phone app's device key travels in; Life OS's sign-in is the gate.
- **SSL:** Request a new SSL certificate (Let's Encrypt), **Force SSL**, and **HTTP/2 Support**.
- **Advanced**, Custom Nginx Configuration:

  ```nginx
  proxy_read_timeout 300s;
  proxy_buffering off;
  ```

  Nginx Proxy Manager waits 90 seconds for an answer, and holds streamed answers back until they end; an AI tool's answer over MCP may stream for up to 5 minutes. Uploads need nothing: it allows up to 2000 MB.

## Tell Life OS

```sh
./setup.sh gateway --public-url https://lifeos.example.com --trusted-proxies 10.255.249.48/28
./setup.sh up
```

`10.255.249.48/28` is the `lifeos-proxy` network: the gateway believes the client's address that Nginx Proxy Manager passes on in `X-Forwarded-For`, from there only. Then check it: sign in, the **Status** page, and your device's address in **Settings → Security log** ([Check it](../access.md#your-own-reverse-proxy)).

## Good to know

- **Host and the client's address.** Nginx Proxy Manager passes the browser's `Host` on and adds the client's address to `X-Forwarded-For`; the gateway takes the last address there that is not a trusted proxy's, so one a client wrote itself never counts.
- **Its own trust.** Nginx Proxy Manager itself believes an `X-Real-IP` header from any private address (`10/8`, `172.16/12`, `192.168/16`, in its `nginx.conf`), and, while it downloads them, from CloudFront's and Cloudflare's ranges. Whoever reaches it from there can make it pass on an address of their choosing, and Life OS then records that one: they get around the per-address sign-in limits and fill the security log with made-up addresses. So:
  - `IP_RANGES_FETCH_ENABLED: "false"`, as above, so it believes no CDN; put no CDN in front of it.
  - **IPv6:** with Docker's IPv6 off (the default), a port published on every address takes IPv6 visitors in through Docker's own bridge address, which is private: every IPv6 visitor could claim any address. Publish 80 and 443 on IPv4 only (`0.0.0.0:443:443`, as above) and give the hostname no `AAAA` record, or turn on IPv6 in Docker so that IPv6 visitors keep their own address.
  - A device on your own network (a private address) can still claim another address. Visitors from the internet over IPv4 cannot.
- **Phone app.** Without an access list, `Authorization` passes unchanged, so device keys work.
