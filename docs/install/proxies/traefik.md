# Traefik

For a server that already runs [Traefik](https://traefik.io/traefik/) v3 in Docker, with Let's Encrypt certificates for a domain. Traefik finds Life OS's gateway by labels on its container, which a `compose.override.yaml` next to Life OS's `compose.yaml` adds. `tools/test-proxy-recipes --traefik-labels` runs this setup in front of the gateway in CI (with Traefik's own certificate instead of Let's Encrypt's).

## Traefik

If you have no Traefik yet, a directory of its own, such as `~/traefik`, with this `compose.yaml` (your email for Let's Encrypt):

```yaml
services:
  traefik:
    image: traefik:v3.7.19
    restart: unless-stopped
    command:
      - --providers.docker=true
      - --providers.docker.exposedByDefault=false
      - --entryPoints.web.address=:80
      - --entryPoints.web.http.redirections.entryPoint.to=websecure
      - --entryPoints.web.http.redirections.entryPoint.scheme=https
      - --entryPoints.websecure.address=:443
      - --entryPoints.websecure.transport.respondingTimeouts.readTimeout=300s
      - --certificatesResolvers.le.acme.email=you@example.com
      - --certificatesResolvers.le.acme.storage=/letsencrypt/acme.json
      - --certificatesResolvers.le.acme.tlsChallenge=true
    ports: ["80:80", "443:443"]
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - ./letsencrypt:/letsencrypt
    networks: [default, lifeos-proxy]

networks:
  lifeos-proxy:
    external: true
```

With a Traefik you already have, add two things to it:

- the external network `lifeos-proxy`, as above: Life OS's network for your proxy, on which the gateway is `lifeos-gateway` ([A proxy in Docker](../access.md#a-proxy-in-docker));
- `readTimeout=300s` on the HTTPS entry point. Traefik's default, 60 seconds, counts the whole request, upload included, so a large share from a phone on a slow connection would be cut off.

Point the domain's DNS at the server and open ports 80 and 443 to it. Life OS's login is then on the internet: do what [Before Life OS faces the internet](../access.md#choose-a-way-in) says first.

## The gateway's labels

In Life OS's directory, next to its `compose.yaml`, a `compose.override.yaml` (Docker Compose reads it with `compose.yaml`, and `./setup.sh update` leaves it alone):

```yaml
services:
  gateway:
    labels:
      traefik.enable: "true"
      traefik.docker.network: lifeos-proxy
      traefik.http.routers.lifeos.rule: Host(`lifeos.example.com`)
      traefik.http.routers.lifeos.entrypoints: websecure
      traefik.http.routers.lifeos.tls.certresolver: le
      traefik.http.services.lifeos.loadbalancer.server.port: "80"
```

`traefik.docker.network` is needed: the gateway is on several networks, and Traefik reaches it on `lifeos-proxy` only. `le` is the certificate resolver's name in Traefik's settings.

Rather not touch Life OS's directory? Traefik's file provider takes the same route from a file instead (`--providers.file.directory=/etc/traefik/dynamic`, with that directory mounted):

```yaml
http:
  routers:
    lifeos:
      rule: Host(`lifeos.example.com`)
      entryPoints: [websecure]
      tls:
        certResolver: le
      service: lifeos
  services:
    lifeos:
      loadBalancer:
        servers:
          - url: http://lifeos-gateway:80
```

## Tell Life OS

```sh
./setup.sh gateway --public-url https://lifeos.example.com --trusted-proxies 10.255.249.48/28
./setup.sh up
```

`./setup.sh up` also applies the labels. `10.255.249.48/28` is the `lifeos-proxy` network: the gateway believes the client's address that Traefik passes on in `X-Forwarded-For`, from there only. Then check it: sign in, the **Status** page, and your device's address in **Settings → Security log** ([Check it](../access.md#your-own-reverse-proxy)).

## Good to know

- **Host and the client's address.** Traefik passes the browser's `Host` on (`passHostHeader`, on by default), and adds the client's address to `X-Forwarded-For`; the gateway takes the last address there that is not a trusted proxy's, so one a client wrote itself never counts.
- **Uploads and long answers.** Traefik sets no limit on uploads unless you add its buffering middleware; leave it out, or allow 60 MB. Streamed answers pass as they come.
- **Phone app.** Traefik passes `Authorization` on, so device keys work. Don't put a `basicAuth` or `forwardAuth` middleware on this router.
