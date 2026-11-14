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
 * The CSRF rule for a request authenticated by the session cookie: an
 * unsafe method needs Origin equal to Life OS's own, or Sec-Fetch-Site
 * same-origin or none (as /share's isShareFromApp, src/lib/share.ts). A
 * cookie alone proves nothing: SameSite=Lax still lets a sibling subdomain
 * (the ntfy hostname) send it.
 */
export function cookieWriteAllowed(
  method: string,
  headers: HeaderSource,
): boolean {
  if (!isUnsafeMethod(method)) return true;
  const site = headers.get("sec-fetch-site");
  if (site && !OWN_SITE.has(site)) return false;
  const origin = headers.get("origin");
  if (origin && origin !== "null") return isOwnOrigin(headers, origin);
  return site !== null && OWN_SITE.has(site);
}

/**
 * The CSRF rule for a request with no credential at all (legacy access):
 * a browser on another site is refused; a client that is not a browser,
 * which sends neither header (the phone app's own calls, scripts), is not.
 */
export function anonymousWriteAllowed(
  method: string,
  headers: HeaderSource,
): boolean {
  if (!isUnsafeMethod(method)) return true;
  const site = headers.get("sec-fetch-site");
  if (site && !OWN_SITE.has(site)) return false;
  const origin = headers.get("origin");
  if (origin && origin !== "null") return isOwnOrigin(headers, origin);
  return true;
}

const LOOPBACK = new Set(["localhost", "127.0.0.1", "::1"]);

export function isLoopbackHost(host: string) {
  return LOOPBACK.has(host) || host.endsWith(".localhost");
}

/**
 * Whether the browser's page is a secure context, where the Secure session
 * cookie and passkeys work: HTTPS, or a loopback host. Judged by Origin
 * (which the browser sets), else by the gateway's X-Forwarded-Proto.
 */
export function isSecureContext(headers: HeaderSource): boolean {
  const origin = headers.get("origin");
  if (origin && origin !== "null") {
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
