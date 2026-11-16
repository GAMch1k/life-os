/**
 * Where a request comes from, for the CSRF rule and for the login's secure
 * context (docs/architecture/multi-user.md#browser-security).
 *
 * The gateway serves only the hosts in its server_name list (any other Host
 * is closed with 444) and passes the host on without its port, as Host and
 * X-Forwarded-Host; so, as for /share (src/lib/share.ts), a request's own
 * origin is judged by host name, not by port.
 */

type HeaderSource = Pick<Headers, "get">;

const UNSAFE = new Set(["POST", "PUT", "PATCH", "DELETE"]);

export function isUnsafeMethod(method: string) {
  return UNSAFE.has(method.toUpperCase());
}

/** The host the browser asked for, lowercase, without its port. */
export function requestHost(headers: HeaderSource): string {
  return (headers.get("x-forwarded-host") ?? headers.get("host") ?? "")
    .split(",")[0]
    .trim()
    .replace(/:\d+$/, "")
    .replace(/^\[(.*)\]$/, "$1")
    .toLowerCase();
}

function originHostname(origin: string): string | null {
  try {
    return new URL(origin).hostname.replace(/^\[(.*)\]$/, "$1").toLowerCase();
  } catch {
    return null;
  }
}

/** Whether an Origin header names the request's own host. */
export function isOwnOrigin(headers: HeaderSource, origin: string): boolean {
  const host = requestHost(headers);
  return host !== "" && originHostname(origin) === host;
}

/**
 * Sec-Fetch-Site values a browser sends for a request a page of Life OS
 * made (same-origin), or the person started themselves (none: typing the
 * address, a bookmark, the installed app's share sheet). A page on another
 * site can produce neither.
 */
const OWN_SITE = new Set(["same-origin", "none"]);

/**
 * Origin: null names no page at all, and only a browser sends it: for a
 * sandboxed iframe's request, one that a cross-origin redirect carried on,
 * or a page from a data: or file: URL. A page on any site can make one, so
 * it is not "no Origin" (a client that is not a browser) but a browser's
 * page that cannot be told apart. Both rules below take it as Life OS's
 * own only when Sec-Fetch-Site vouches for it: same-origin, or none (the
 * installed app's share sheet posts with Origin: null and none). Without
 * Sec-Fetch-Site it is refused, with the cookie or without (legacy
 * access): browsers send none to a plain-HTTP address that is not a secure
 * context (a LAN or WireGuard address such as http://10.8.0.1:3000), nor
 * does Safari before 16.4 anywhere, so there a sandboxed iframe on any web
 * page could otherwise post as the owner while legacy access is on.
 */
export const NULL_ORIGIN = "null";

/**
 * The CSRF rule for a request authenticated by the session cookie: an
 * unsafe method needs Origin equal to Life OS's own, or Sec-Fetch-Site
 * same-origin or none (as /share's isShareFromApp, src/lib/share.ts); a
 * null Origin needs the latter (NULL_ORIGIN). A cookie alone proves
 * nothing: SameSite=Lax still lets a sibling subdomain (the ntfy hostname)
 * send it.
 */
export function cookieWriteAllowed(
  method: string,
  headers: HeaderSource,
): boolean {
  if (!isUnsafeMethod(method)) return true;
  const site = headers.get("sec-fetch-site");
  if (site && !OWN_SITE.has(site)) return false;
  const origin = headers.get("origin");
  if (origin === NULL_ORIGIN) return site !== null;
  const ownOrigin = !origin || isOwnOrigin(headers, origin);
  // On loopback, Origin cannot tell Life OS from another web app on this
  // machine (see loopbackNeedsFetchSite): Sec-Fetch-Site must say so too.
  if (isLoopbackLogin(headers)) return site !== null && ownOrigin;
  if (origin) return ownOrigin;
  return site !== null;
}

/**
 * The CSRF rule for a request with no credential at all (legacy access,
 * and signing in): a browser on another site is refused, and so is a null
 * Origin without Sec-Fetch-Site (NULL_ORIGIN); a client that is not a
 * browser, which sends neither Origin nor Sec-Fetch-Site (the phone app's
 * own calls, scripts, MCP clients), is not.
 */
export function anonymousWriteAllowed(
  method: string,
  headers: HeaderSource,
): boolean {
  if (!isUnsafeMethod(method)) return true;
  const site = headers.get("sec-fetch-site");
  if (site && !OWN_SITE.has(site)) return false;
  const origin = headers.get("origin");
  if (origin === NULL_ORIGIN) return site !== null;
  if (origin) {
    // A browser's page on loopback: see loopbackNeedsFetchSite.
    if (isLoopbackLogin(headers) && site === null) return false;
    return isOwnOrigin(headers, origin);
  }
  return true;
}

/*
 * loopbackNeedsFetchSite: on this machine's loopback (an SSH tunnel), the
 * gateway passes the host without its port, and the SSH tunnel's local
 * port is the browser's choice anyway, so Origin cannot tell Life OS from
 * another web app on this machine (http://127.0.0.1:8080 has the same
 * host name). Sec-Fetch-Site can: a page of another port is same-site, not
 * same-origin. So on loopback a browser's write needs Sec-Fetch-Site
 * same-origin (or none, a navigation the person started), which Chromium,
 * Firefox, and Safari 16.4 or newer send; an older browser cannot write
 * there. (Cookies ignore ports, so those other apps see the session
 * cookie; docs/install/accounts.md says so.)
 */

const LOOPBACK = new Set(["localhost", "127.0.0.1", "::1"]);

export function isLoopbackHost(host: string) {
  return LOOPBACK.has(host) || host.endsWith(".localhost");
}

/**
 * Whether a request reached Life OS at one of this machine's own loopback
 * names over plain HTTP: http://127.0.0.1 or http://localhost, as through
 * an SSH tunnel. (::1 is in the list too, but the gateway does not answer
 * the Host [::1]: it is not among its server names.) Only these exact
 * names, never a subdomain of localhost or a name DNS resolves: a browser
 * sends them to this machine alone. There the session cookie is not Secure, because
 * Safari refuses a Secure cookie on plain-HTTP loopback; the traffic never
 * leaves the machine, or travels inside the SSH tunnel. The host is the
 * one the gateway passes on (X-Forwarded-Host, which it sets itself), and
 * X-Forwarded-Proto is https only through Cloudflare's tunnel.
 */
export function isLoopbackLogin(headers: HeaderSource): boolean {
  const proto = headers.get("x-forwarded-proto")?.split(",")[0].trim();
  return proto !== "https" && LOOPBACK.has(requestHost(headers));
}

/** Whether an origin is one of the loopback names isLoopbackLogin takes. */
export function isLoopbackOrigin(origin: string): boolean {
  const host = originHostname(origin);
  return host !== null && LOOPBACK.has(host);
}

/**
 * Whether the browser's page is a secure context, where the Secure session
 * cookie and passkeys work: HTTPS, or a loopback host. Judged by Origin
 * (which the browser sets), else by the gateway's X-Forwarded-Proto.
 */
export function isSecureContext(headers: HeaderSource): boolean {
  const origin = headers.get("origin");
  if (origin && origin !== NULL_ORIGIN) {
    try {
      const url = new URL(origin);
      return (
        url.protocol === "https:" ||
        isLoopbackHost(url.hostname.replace(/^\[(.*)\]$/, "$1"))
      );
    } catch {
      return false;
    }
  }
  const proto = headers.get("x-forwarded-proto")?.split(",")[0].trim();
  return proto === "https" || isLoopbackHost(requestHost(headers));
}
