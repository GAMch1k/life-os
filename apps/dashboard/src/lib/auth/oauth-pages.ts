import { verifyOAuthQueryParams } from "@better-auth/oauth-provider";

import type { CheckRuntime } from "./check";
import { MCP_SCOPES } from "./constants";

/**
 * What the login and consent pages need of MCP's OAuth flow
 * (docs/services/mcp.md#signing-in-with-oauth). Better Auth's OAuth
 * provider sends a person who is not signed in to /login, and one who is to
 * /oauth/consent, each with the authorization request's parameters, signed
 * and valid for 10 minutes. The pages check the signature before they show
 * anything, and send the browser only back to the provider (or, from the
 * consent, to the redirect address the provider itself returns).
 */

type SearchParams = Record<string, string | string[] | undefined>;

/** The search parameters as a query string, repeated ones repeated. */
export function queryOf(params: SearchParams): string {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined) continue;
    for (const one of Array.isArray(value) ? value : [value]) {
      query.append(key, one);
    }
  }
  return query.toString();
}

/** The signature's own parameters, which the provider adds and checks. */
const SIGNATURE_PARAMS = ["sig", "exp", "ba_iat", "ba_pl", "ba_param"];

export type OAuthLoginDecision =
  /** Signed in as the client asks: back to the provider at once. */
  | { action: "continue"; url: string }
  /**
   * Sign in (again, when the client asks for a fresh sign-in with
   * prompt=login or a max_age the session is older than), then go on to
   * next, which no longer asks.
   */
  | { action: "sign-in"; next: string };

/**
 * What the login page does with an OAuth authorization request it was
 * sent with: go on to the provider's authorize endpoint with the request's
 * own parameters (it validates them all again), at once when this browser
 * is signed in as the client asks, or after signing in. A client's
 * prompt=login and max_age are honoured: a session that is signed in but
 * not fresh enough signs in again. null when the page has no such request,
 * or its signature is wrong or expired.
 */
export async function oauthLoginDecision(
  rt: CheckRuntime,
  params: SearchParams,
  session: { signedIn: boolean; signedInAt: number | null },
  now = Date.now(),
): Promise<OAuthLoginDecision | null> {
  if (typeof params.client_id !== "string" || typeof params.sig !== "string") {
    return null;
  }
  const query = queryOf(params);
  if (!(await verifyOAuthQueryParams(query, rt.env.secret))) return null;
  const authorize = new URLSearchParams(query);
  for (const name of SIGNATURE_PARAMS) authorize.delete(name);
  const prompts = (authorize.get("prompt") ?? "").split(" ").filter(Boolean);
  const maxAge = authorize.has("max_age")
    ? Number(authorize.get("max_age"))
    : null;
  // As the provider judges it (max_age=0: always a new sign-in), so that
  // going on never comes straight back here.
  const tooOld =
    maxAge !== null &&
    (!Number.isFinite(maxAge) ||
      maxAge <= 0 ||
      session.signedInAt === null ||
      now - session.signedInAt > maxAge * 1000);
  if (session.signedIn && !prompts.includes("login") && !tooOld) {
    return {
      action: "continue",
      url: `/api/auth/oauth2/authorize?${authorize.toString()}`,
    };
  }
  // Signed in just now, after this: the prompt and the age are answered.
  const rest = prompts.filter((value) => value !== "login");
  if (rest.length) authorize.set("prompt", rest.join(" "));
  else authorize.delete("prompt");
  authorize.delete("max_age");
  return {
    action: "sign-in",
    next: `/api/auth/oauth2/authorize?${authorize.toString()}`,
  };
}

export type ConsentRequest =
  | {
      ok: true;
      clientId: string;
      clientName: string | null;
      clientUri: string | null;
      /** Where the app gets its answer: the redirect URI's host. */
      redirectHost: string;
      /** mcp:read and mcp:write, as asked (none: the client named none). */
      scopes: string[];
      /** Whether it asked for offline_access (refresh tokens). */
      offlineAccess: boolean;
      /** The signed query, for POST /api/auth/oauth2/consent. */
      oauthQuery: string;
    }
  | { ok: false };

/** The consent page's request: signed, of a client that exists. */
export async function consentRequest(
  rt: CheckRuntime,
  params: SearchParams,
): Promise<ConsentRequest> {
  const query = queryOf(params);
  const search = new URLSearchParams(query);
  const clientId = search.get("client_id");
  if (!clientId || !(await verifyOAuthQueryParams(query, rt.env.secret))) {
    return { ok: false };
  }
  const { rows } = await rt.pool.query<{
    name: string | null;
    uri: string | null;
    disabled: boolean | null;
  }>('SELECT name, uri, disabled FROM "oauthClient" WHERE "clientId" = $1', [
    clientId,
  ]);
  if (!rows[0] || rows[0].disabled) return { ok: false };
  let redirectHost = "";
  try {
    const redirect = new URL(search.get("redirect_uri") ?? "");
    redirectHost =
      redirect.protocol === "http:" || redirect.protocol === "https:"
        ? redirect.host
        : `${redirect.protocol}//`;
  } catch {
    return { ok: false };
  }
  const asked = (search.get("scope") ?? "").split(" ");
  const scopes = MCP_SCOPES.filter((scope) => asked.includes(scope));
  return {
    ok: true,
    clientId,
    clientName: rows[0].name,
    clientUri: rows[0].uri,
    redirectHost,
    scopes,
    offlineAccess: asked.includes("offline_access"),
    oauthQuery: query,
  };
}
