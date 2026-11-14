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

/**
 * After signing in: where an OAuth authorization request the login page
 * was sent with goes on, the provider's authorize endpoint with the
 * request's own parameters (it validates them all again). null when the
 * page has no such request, or its signature is wrong or expired.
 */
export async function oauthLoginContinuation(
  rt: CheckRuntime,
  params: SearchParams,
): Promise<string | null> {
  if (typeof params.client_id !== "string" || typeof params.sig !== "string") {
    return null;
  }
  const query = queryOf(params);
  if (!(await verifyOAuthQueryParams(query, rt.env.secret))) return null;
  const authorize = new URLSearchParams(query);
  for (const name of SIGNATURE_PARAMS) authorize.delete(name);
  // Signed in just now: a prompt to sign in is answered.
  const prompt = (authorize.get("prompt") ?? "")
    .split(" ")
    .filter((value) => value && value !== "login");
  if (prompt.length) authorize.set("prompt", prompt.join(" "));
  else authorize.delete("prompt");
  authorize.delete("max_age");
  return `/api/auth/oauth2/authorize?${authorize.toString()}`;
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
