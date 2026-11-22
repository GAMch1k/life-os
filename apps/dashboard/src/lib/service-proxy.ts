import { type NextRequest, NextResponse } from "next/server";

import { ACTOR_HEADER, PUBLIC_ACTOR } from "@/lib/api/actor";

type RouteContext = { params: Promise<{ path: string[] }> };

export type ProxyOptions = {
  /**
   * More request headers to pass on, such as the owner's browser details
   * the banking service needs to mark a bank read as owner-present.
   */
  forwardHeaders?: readonly string[];
};

// Idempotency-Key makes a retried Finance write return the first result.
// X-Life-OS-Identity is the token the Dashboard's proxy (src/proxy.ts) put
// there for this request, as the gateway does; a client's own never gets
// this far.
const FORWARDED_REQUEST_HEADERS = [
  "accept",
  "content-type",
  "idempotency-key",
  "x-life-os-identity",
] as const;

/**
 * What a service's answer may be shown as, in the browser, from the
 * Dashboard's origin: JSON and the images and audio the services serve
 * (Inbox's photos). Anything else is sent as a download.
 */
const INLINE_CONTENT_TYPE =
  /^(?:application\/(?:[a-z0-9.+-]+\+)?json|image\/(?:jpeg|png|webp|gif|avif)|audio\/(?:mpeg|mp4|ogg|webm|wav|x-wav|aac))$/;

/**
 * Every answer of the proxy is a service's data, never a page: it runs no
 * script and loads nothing as the Dashboard (a sandboxing CSP), and is
 * never read as another type than it says.
 */
function protect(headers: Headers, status: number) {
  headers.set("content-security-policy", "sandbox");
  headers.set("x-content-type-options", "nosniff");
  if (status === 204 || status === 304) return;
  const type = (headers.get("content-type") ?? "")
    .split(";")[0]
    .trim()
    .toLowerCase();
  if (!INLINE_CONTENT_TYPE.test(type)) {
    headers.set("content-disposition", "attachment");
  }
}

function problem(status: number, code: string, message: string) {
  const headers = new Headers({
    "content-type": "application/json",
    "cache-control": "no-store",
  });
  protect(headers, status);
  return new NextResponse(JSON.stringify({ error: { code, message } }), {
    headers,
    status,
  });
}

function hasControlCharacter(value: string) {
  for (let i = 0; i < value.length; i++) {
    const code = value.charCodeAt(i);
    if (code < 0x20 || code === 0x7f) return true;
  }
  return false;
}

/**
 * A path segment the proxy passes on: one name, as a service's routes
 * have them (an id, a date, a word). Empty and dot segments, separators
 * (`/`, `\`), a scheme's or port's `:`, and control characters are never
 * part of one, whatever encoding they arrived in (Next.js decodes the
 * segments of a catch-all route).
 */
export function isSafeSegment(segment: string) {
  return (
    segment !== "" &&
    segment !== "." &&
    segment !== ".." &&
    !/[/\\:]/.test(segment) &&
    !hasControlCharacter(segment)
  );
}

/**
 * The service URL a request names: the service's base URL and the
 * segments, each encoded, with the request's query. Null when a segment
 * is not one (isSafeSegment) or the URL would leave the service.
 */
export function serviceTarget(
  serviceUrl: string,
  segments: readonly string[],
  search: string,
): URL | null {
  if (!segments.length || !segments.every(isSafeSegment)) return null;
  const base = new URL(`${serviceUrl.replace(/\/+$/, "")}/`);
  const target = new URL(base);
  target.pathname = `${base.pathname}${segments.map(encodeURIComponent).join("/")}`;
  target.search = search;
  if (
    target.origin !== base.origin ||
    !target.pathname.startsWith(base.pathname)
  ) {
    return null;
  }
  return target;
}

/**
 * A service's Location, when it stays on the service; another origin is
 * never passed on.
 */
function sameOriginLocation(location: string, target: URL): string | null {
  try {
    return new URL(location, target).origin === target.origin ? location : null;
  } catch {
    return null;
  }
}

/**
 * Passes a request on to a service: its path below the route, its query,
 * method, body, and the headers above. The path must be plain segments
 * (isSafeSegment), the target is always the service's own origin, and a
 * redirect is never followed.
 */
export async function proxyServiceRequest(
  request: NextRequest,
  context: RouteContext,
  serviceUrl: string,
  options: ProxyOptions = {},
) {
  const { path } = await context.params;
  const target = serviceTarget(serviceUrl, path, request.nextUrl.search);
  if (!target) {
    return problem(400, "forbidden_path", "This path is not allowed");
  }

  const headers = new Headers();
  for (const name of [
    ...FORWARDED_REQUEST_HEADERS,
    ...(options.forwardHeaders ?? []),
  ]) {
    const value = request.headers.get(name);
    if (value) {
      headers.set(name, value);
    }
  }
  // Like the gateway, pass on only owner and development-tool actors.
  const actor = request.headers.get(ACTOR_HEADER)?.trim();
  if (actor && PUBLIC_ACTOR.test(actor)) {
    headers.set(ACTOR_HEADER, actor);
  }

  const init: RequestInit = {
    cache: "no-store",
    headers,
    method: request.method,
    redirect: "manual",
  };
  if (request.method !== "GET" && request.method !== "HEAD") {
    const body = await request.arrayBuffer();
    if (body.byteLength > 0) {
      init.body = body;
    }
  }

  try {
    const response = await fetch(target, init);
    const responseHeaders = new Headers();
    const type = response.headers.get("content-type");
    if (type) responseHeaders.set("content-type", type);
    const location = response.headers.get("location");
    const kept = location ? sameOriginLocation(location, target) : null;
    if (kept) responseHeaders.set("location", kept);
    protect(responseHeaders, response.status);
    return new NextResponse(response.body, {
      headers: responseHeaders,
      status: response.status,
    });
  } catch {
    return problem(503, "service_unavailable", "Service unavailable");
  }
}

/**
 * The development proxy of a service the gateway routes itself
 * (`/api/<service>/`, infra/nginx/life-os.conf.template): with `next dev`,
 * without the gateway, the browser's calls reach the service through it.
 * Behind the gateway no client request comes here, so outside development
 * it answers 404 and passes nothing on.
 */
export function developmentProxy(
  request: NextRequest,
  context: RouteContext,
  serviceUrl: string,
  options: ProxyOptions = {},
) {
  if (process.env.NODE_ENV !== "development") {
    return Promise.resolve(problem(404, "not_found", "Not Found"));
  }
  return proxyServiceRequest(request, context, serviceUrl, options);
}
