import { type CheckRuntime, checkRequest, sessionSource } from "./check";
import { IDENTITY_HEADER } from "./constants";

/**
 * The Dashboard's auth endpoints as plain fetch handlers, which the Next.js
 * routes mount and the auth flow suite calls directly.
 */

/** Better Auth at /api/auth/*: for the request's host, the secure one or,
 * on a plain-HTTP host the owner allowed, the insecure one. */
export function authHandler(rt: CheckRuntime, request: Request) {
  return sessionSource(rt, request.headers).auth.handler(request);
}

/**
 * The gateway's auth_request target, /internal/auth/check. It answers only
 * 204 (with the identity token), 401, or 403, as nginx needs; anything that
 * goes wrong is a 500, which the gateway turns into 503 auth_unavailable.
 */
export async function checkHandler(
  rt: CheckRuntime,
  request: Request,
): Promise<Response> {
  const method = request.headers.get("x-original-method") ?? request.method;
  try {
    const result = await checkRequest(rt, method, request.headers);
    if (result.status === 204) {
      return new Response(null, {
        status: 204,
        headers: {
          [IDENTITY_HEADER]: result.token,
          "cache-control": "no-store",
        },
      });
    }
    return new Response(null, {
      status: result.status,
      headers: {
        "x-life-os-auth-error": result.code,
        "cache-control": "no-store",
      },
    });
  } catch (error) {
    console.error(
      "auth check failed",
      error instanceof Error ? error.message : error,
    );
    return new Response(null, { status: 500 });
  }
}
