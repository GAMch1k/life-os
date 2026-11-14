import { type CheckRuntime, checkRequest, sessionSource } from "./check";
import { IDENTITY_HEADER } from "./constants";
import { devicesHandler } from "./devices-api";

/**
 * The Dashboard's auth endpoints as plain fetch handlers, which the Next.js
 * routes mount and the auth flow suite calls directly.
 */

/** Better Auth at /api/auth/*: for the request's host, the secure one or,
 * on a plain-HTTP host the owner allowed, the insecure one. The phone
 * apps' pairing endpoints (/api/auth/devices/…) are Life OS's own. */
export function authHandler(rt: CheckRuntime, request: Request) {
  const { pathname } = new URL(request.url);
  if (pathname.startsWith("/api/auth/devices/")) {
    return devicesHandler(rt, request);
  }
  return sessionSource(rt, request.headers).auth.handler(request);
}

/**
 * The gateway's auth_request target, /internal/auth/check. It answers only
 * 204 (with the identity token), 401, or 403, as nginx needs; anything that
 * goes wrong is a 500, which the gateway turns into 503 auth_unavailable.
 * A 401 for MCP carries the WWW-Authenticate challenge that points an MCP
 * client to OAuth (the gateway passes it on), and every refusal its code
 * in X-Life-OS-Auth-Error.
 */
export async function checkHandler(
  rt: CheckRuntime,
  request: Request,
): Promise<Response> {
  const method = request.headers.get("x-original-method") ?? request.method;
  const uri = request.headers.get("x-original-uri") ?? "/";
  try {
    const result = await checkRequest(rt, method, request.headers, { uri });
    if (result.status === 204) {
      return new Response(null, {
        status: 204,
        headers: {
          [IDENTITY_HEADER]: result.token,
          "cache-control": "no-store",
        },
      });
    }
    const headers: Record<string, string> = {
      "x-life-os-auth-error": result.code,
      "cache-control": "no-store",
    };
    if (result.status === 401 && result.challenge) {
      headers["www-authenticate"] = result.challenge;
    }
    return new Response(null, { status: result.status, headers });
  } catch (error) {
    console.error(
      "auth check failed",
      error instanceof Error ? error.message : error,
    );
    return new Response(null, { status: 500 });
  }
}
