import { type NextRequest, NextResponse } from "next/server";

import { checkRequest } from "@/lib/auth/check";
import { IDENTITY_HEADER } from "@/lib/auth/constants";
import { authRuntime } from "@/lib/auth/runtime";

/**
 * The sign-in in front of the Dashboard's own pages and routes
 * (docs/architecture/multi-user.md#the-gateway): the same check the gateway
 * asks for the services' routes, on every request the matcher below lets
 * through. Without a session (and without legacy access), a page goes to
 * /login?next=…, an API route answers 401. A request on its way to a
 * service through the development proxies (src/lib/service-proxy.ts) gets
 * the identity token, as the gateway gives it.
 *
 * Not here, by the matcher: the login, setup, and reset pages, Better
 * Auth's /api/auth/, /api/account/ and /share (which check themselves),
 * /internal/ (the gateway answers it with 404 from outside), the privacy
 * and terms pages, and the files the installed app and the login page load
 * (/_next/static, the service worker, the manifest, the icons). /share
 * must stay out: for a POST the proxy matches, Next.js copies the body and
 * caps the copy at 10 MiB, which a share of two or three photos exceeds.
 */

/** Pages that are new with accounts: legacy access is not enough there. */
const ACCOUNT_PAGES = ["/settings"];

function needsAccount(pathname: string) {
  return ACCOUNT_PAGES.some(
    (page) => pathname === page || pathname.startsWith(`${page}/`),
  );
}

function isApi(pathname: string) {
  return pathname === "/api" || pathname.startsWith("/api/");
}

function toLogin(request: NextRequest) {
  const { pathname, search } = request.nextUrl;
  const next = `${pathname}${search}`;
  // Next.js sends the Location relative, since it is on the request's own
  // host: it stays on the host the owner used (behind the gateway the
  // request URL names the Dashboard's container). A form's POST becomes a
  // GET of the login page (303).
  const response = NextResponse.redirect(
    new URL(`/login?next=${encodeURIComponent(next)}`, request.url),
    request.method === "GET" || request.method === "HEAD" ? 307 : 303,
  );
  response.headers.set("cache-control", "no-store");
  return response;
}

function problem(status: number, code: string, message: string) {
  return NextResponse.json(
    { error: { code, message } },
    { status, headers: { "cache-control": "no-store" } },
  );
}

export async function proxy(request: NextRequest) {
  const { pathname } = request.nextUrl;
  let result;
  try {
    result = await checkRequest(
      authRuntime(),
      request.method,
      request.headers,
      {
        refresh: !isApi(pathname) && request.method === "GET",
      },
    );
  } catch (error) {
    console.error(
      "auth check failed",
      error instanceof Error ? error.message : error,
    );
    return problem(
      503,
      "auth_unavailable",
      "Sign-in is unavailable right now.",
    );
  }

  if (result.status === 403) {
    return problem(
      403,
      "forbidden_origin",
      "This request did not come from Life OS.",
    );
  }
  if (result.status === 401) {
    return isApi(pathname)
      ? problem(401, "unauthenticated", "Sign in first.")
      : toLogin(request);
  }
  if (result.principal.via !== "session" && needsAccount(pathname)) {
    return isApi(pathname)
      ? problem(
          401,
          "sign_in_required",
          "Sign in with your account to do this.",
        )
      : toLogin(request);
  }

  // Only the token the check minted reaches a service, never a client's.
  const headers = new Headers(request.headers);
  headers.delete(IDENTITY_HEADER);
  if (isApi(pathname)) headers.set(IDENTITY_HEADER, result.token);
  const response = NextResponse.next({ request: { headers } });
  for (const cookie of result.setCookie ?? []) {
    response.headers.append("set-cookie", cookie);
  }
  return response;
}

export const config = {
  matcher: [
    "/((?!_next/static|_next/image|api/auth/|api/account|internal/|login|setup|reset/|share$|privacy|terms|sw\\.js|icons/|app\\.webmanifest|favicon\\.ico).*)",
  ],
};
