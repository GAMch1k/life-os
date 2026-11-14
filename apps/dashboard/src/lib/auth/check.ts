import type { Pool } from "pg";

import {
  FIRST_SPACE_ID,
  FIRST_USER_ID,
  INSECURE_SESSION_COOKIE,
  type ServerRole,
  SESSION_COOKIE,
} from "./constants";
import type { AuthEnv } from "./env";
import {
  type IdentityClaims,
  type Signer,
  type SpaceRole,
  TokenCache,
  type Via,
} from "./identity";
import { checkKey, isKeyShaped, type KeyKind, type KeyUseLog } from "./keys";
import {
  findGrant,
  isJwtShaped,
  mcpChallenge,
  type McpTokenVerifier,
} from "./oauth";
import {
  anonymousWriteAllowed,
  cookieWriteAllowed,
  requestHost,
} from "./origin";
import { serverRole } from "./roles";
import {
  mcpModuleScopes,
  routeOf,
  type RouteKind,
  scopesAllow,
} from "./scopes";
import type { LegacyUseLog } from "./security-log";
import type { LifeOsAuth } from "./server";
import { legacyAccess } from "./store";

/**
 * The auth check (docs/architecture/multi-user.md#the-gateway): who makes a
 * request, from its credential, and the identity token for the services.
 * The gateway asks it for every service route (/internal/auth/check), the
 * Dashboard's proxy (src/proxy.ts) for its own pages and routes.
 *
 *  - Authorization: Bearer (P21-06): a device key (lo_dev_), a personal
 *    API token (lo_tok_, only on the routes its scopes name), or an MCP
 *    client's OAuth access token (a JWT, on /api/mcp/ only). A request with
 *    one is judged by it alone: an invalid one is 401, never the cookie's
 *    account and never legacy access. Other schemes (Basic) are not Life
 *    OS credentials and are ignored, as before.
 *  - The session cookie: the session is read from the database on every
 *    request (Better Auth's cookie cache is off), so a sign-out or a ban
 *    counts at once. A write must come from Life OS's own origin.
 *  - No credential: legacy access, while it is on, is the first account,
 *    as Life OS was before accounts; while it is off, 401.
 */

export type Principal = {
  userId: string;
  role: ServerRole;
  via: Via;
  /** The session's id; absent for legacy access. */
  sessionId?: string;
  /** When the session was created, for sensitive actions (12 hours). */
  sessionCreatedAt?: Date;
  /** The session signed in with a second factor (the token's mfa). */
  mfa?: boolean;
  /** The device key's or token's id, or the MCP grant's (the consent). */
  credentialId?: string;
  /** A key's kind: phone, watch, or token. */
  keyKind?: KeyKind;
  /** A token's or MCP grant's scopes (absent: all the user's rights). */
  scopes?: string[];
  /** A token marked as a development tool: its name. */
  devTool?: string;
  /** An MCP client's id. */
  clientId?: string;
};

export type CheckResult =
  | {
      status: 204;
      token: string;
      principal: Principal;
      /** A renewed session cookie (with refresh), for the response. */
      setCookie?: string[];
    }
  | {
      status: 401;
      code: "unauthenticated";
      /** For MCP: the WWW-Authenticate challenge (RFC 9728). */
      challenge?: string;
    }
  | { status: 403; code: "forbidden_origin" | "insufficient_scope" };

export type CheckRuntime = {
  env: AuthEnv;
  pool: Pool;
  /** Better Auth for HTTPS and localhost. */
  auth: LifeOsAuth;
  /** Better Auth for the plain-HTTP hosts the owner allowed, if any. */
  insecureAuth?: LifeOsAuth;
  signer: Signer;
  tokens: TokenCache;
  legacy: LegacyCache;
  /** Counts the requests made under legacy access, for the security log. */
  legacyUses?: LegacyUseLog;
  /** Notes the device keys' and tokens' last use. */
  keyUses?: KeyUseLog;
  /** Verifies MCP clients' OAuth access tokens. */
  mcpTokens?: McpTokenVerifier;
  /**
   * Starts deleting the push subscriptions of revoked device keys now
   * (device_key_cleanup), rather than at the next minute's run.
   */
  keyCleanup?: () => Promise<void>;
};

/** Legacy access, cached for a few seconds: the check reads it often. */
export class LegacyCache {
  private value: boolean | undefined;
  private readAt = 0;

  constructor(
    private readonly pool: Pool,
    private readonly fallback: boolean,
    private readonly ttlMs = 5000,
  ) {}

  async get(now = Date.now()): Promise<boolean> {
    if (this.value === undefined || now - this.readAt > this.ttlMs) {
      this.value = await legacyAccess(this.pool, this.fallback);
      this.readAt = now;
    }
    return this.value;
  }

  invalidate() {
    this.value = undefined;
  }
}

/**
 * The spaces a user is in. Until spaces exist (P21-07), the first account
 * owns its personal space, by its fixed id, and no one else has one.
 */
export function spacesOf(userId: string): {
  sp: Record<string, SpaceRole>;
  psp?: string;
} {
  if (userId === FIRST_USER_ID) {
    return { sp: { [FIRST_SPACE_ID]: "o" }, psp: FIRST_SPACE_ID };
  }
  return { sp: {} };
}

function cookieValue(header: string | null, name: string): string | null {
  if (!header) return null;
  for (const part of header.split(";")) {
    const index = part.indexOf("=");
    if (index > 0 && part.slice(0, index).trim() === name) {
      return part.slice(index + 1).trim();
    }
  }
  return null;
}

/**
 * The bearer token of a request: null without an Authorization header or
 * with another scheme (Basic, which a proxy in front may use); "" for a
 * Bearer without a token, which is refused like any invalid one.
 */
export function bearerToken(headers: Headers): string | null {
  const value = headers.get("authorization");
  if (value === null) return null;
  const match = /^\s*bearer(?:\s+(.*))?$/i.exec(value);
  if (!match) return null;
  return (match[1] ?? "").trim();
}

/** The Better Auth instance and cookie name for the request's host. */
export function sessionSource(rt: CheckRuntime, headers: Headers) {
  if (
    rt.insecureAuth &&
    rt.env.insecureLoginHosts.includes(requestHost(headers))
  ) {
    return { auth: rt.insecureAuth, cookie: INSECURE_SESSION_COOKIE };
  }
  return { auth: rt.auth, cookie: SESSION_COOKIE };
}

type Session = {
  session: {
    id: string;
    createdAt: Date | string;
    secondFactor?: boolean | null;
  };
  user: {
    id: string;
    role?: string | null;
    banned?: boolean | null;
    banExpires?: Date | string | null;
    authzVersion?: number | null;
  };
};

function isBanned(user: Session["user"]) {
  if (!user.banned) return false;
  return !user.banExpires || new Date(user.banExpires).getTime() > Date.now();
}

export function legacyClaims(): IdentityClaims {
  return {
    sub: FIRST_USER_ID,
    sid: "legacy",
    via: "legacy",
    role: "owner",
    ...spacesOf(FIRST_USER_ID),
  };
}

/**
 * The path of the request the check is about: the gateway's check passes
 * X-Original-URI (which nginx sets, never the client), the Dashboard its
 * own path. Never read from a client's header here: a token must not name
 * another route than the one it calls.
 */
function requestUri(uri?: string) {
  return uri ?? "/";
}

function unauthenticated(rt: CheckRuntime, route: RouteKind, invalid: boolean) {
  return {
    status: 401 as const,
    code: "unauthenticated" as const,
    ...(route.kind === "mcp"
      ? {
          challenge: mcpChallenge(
            rt.env.publicUrl,
            invalid ? "invalid_token" : undefined,
          ),
        }
      : {}),
  };
}

/**
 * A device key or personal token: valid, of an account that is not
 * disabled, and for a token, scoped for the route. A device key is the
 * person on every route but MCP's.
 */
async function checkApiKey(
  rt: CheckRuntime,
  method: string,
  route: RouteKind,
  presented: string,
): Promise<CheckResult> {
  const result = await checkKey(rt.pool, presented);
  if (!result.ok) return unauthenticated(rt, route, true);
  const key = result.key;
  const via: Via = key.kind === "token" ? "token" : "device";
  if (route.kind === "mcp") {
    return { status: 403, code: "insufficient_scope" };
  }
  if (via === "token") {
    const scopes = key.scopes ?? [];
    if (route.kind !== "module" || !scopesAllow(scopes, route.module, method)) {
      return { status: 403, code: "insufficient_scope" };
    }
  }
  rt.keyUses?.touch(key.id);
  const role = key.user.role;
  const claims: IdentityClaims = {
    sub: key.userId,
    sid: key.id,
    via,
    role,
    ...spacesOf(key.userId),
    ...(via === "token" ? { scp: key.scopes ?? [] } : {}),
    ...(via === "token" && key.devTool ? { dev: devName(key.name) } : {}),
  };
  const token = rt.tokens.get(
    `key:${key.id}`,
    `${key.user.authzVersion}:${role}`,
    () => rt.signer.sign(claims),
  );
  return {
    status: 204,
    token,
    principal: {
      userId: key.userId,
      role,
      via,
      credentialId: key.id,
      keyKind: key.kind,
      ...(via === "token" ? { scopes: key.scopes ?? [] } : {}),
      ...(claims.dev ? { devTool: claims.dev } : {}),
    },
  };
}

/** A development tool's actor name: dev:<name>, from the token's name. */
export function devName(name: string): string {
  const cleaned = name
    .replace(/[^A-Za-z0-9._\s-]/g, "")
    .trim()
    .replace(/[\s-]+/g, "-")
    .replace(/^[-.]+|[-.]+$/g, "")
    .slice(0, 64);
  return cleaned || "token";
}

/**
 * An MCP client's OAuth access token, on /api/mcp/ only: the JWT, then the
 * grant (the consent), so that a revoked one is refused at once.
 */
async function checkMcpToken(
  rt: CheckRuntime,
  route: RouteKind,
  presented: string,
): Promise<CheckResult> {
  if (route.kind !== "mcp" || !rt.mcpTokens) {
    return unauthenticated(rt, route, true);
  }
  const access = await rt.mcpTokens.verify(presented);
  if (!access) return unauthenticated(rt, route, true);
  const grant = await findGrant(rt.pool, access);
  if (!grant || grant.user.banned) return unauthenticated(rt, route, true);
  const granted = access.scopes.filter((scope) => grant.scopes.includes(scope));
  const scp = mcpModuleScopes(granted);
  if (scp.length === 0) return { status: 403, code: "insufficient_scope" };
  const role = grant.user.role;
  const claims: IdentityClaims = {
    sub: access.userId,
    sid: grant.id,
    via: "mcp",
    role,
    ...spacesOf(access.userId),
    scp,
  };
  const token = rt.tokens.get(
    `mcp:${grant.id}:${scp.join(",")}`,
    `${grant.user.authzVersion}:${role}`,
    () => rt.signer.sign(claims),
  );
  return {
    status: 204,
    token,
    principal: {
      userId: access.userId,
      role,
      via: "mcp",
      credentialId: grant.id,
      scopes: scp,
      clientId: access.clientId,
    },
  };
}

/**
 * Checks a request: its method and headers (for the gateway, the original
 * request's, which it sends as X-Original-Method and X-Original-URI).
 */
export async function checkRequest(
  rt: CheckRuntime,
  method: string,
  headers: Headers,
  options: {
    /**
     * Renew the session (once a day) and return its new cookie: the
     * Dashboard's pages do, the gateway's check does not (nginx keeps no
     * cookie from it), so it only reads.
     */
    refresh?: boolean;
    /**
     * Count a request under legacy access in the security log (the
     * default). A page or route checking again a request the proxy already
     * counted passes false.
     */
    count?: boolean;
    /**
     * The request's path and query: X-Original-URI for the gateway's
     * check, the Dashboard's own path for its routes. Without it, the
     * request is on none of the services' routes (a token is refused).
     */
    uri?: string;
  } = {},
): Promise<CheckResult> {
  const route = routeOf(requestUri(options.uri));

  // Authorization: Bearer is the credential when present; it carries no
  // ambient authority, so the CSRF rule does not apply to it.
  const bearer = bearerToken(headers);
  if (bearer !== null) {
    if (isKeyShaped(bearer)) return checkApiKey(rt, method, route, bearer);
    if (isJwtShaped(bearer)) return checkMcpToken(rt, route, bearer);
    return unauthenticated(rt, route, true);
  }

  const { auth, cookie } = sessionSource(rt, headers);
  const hasCookie = cookieValue(headers.get("cookie"), cookie) !== null;

  if (hasCookie) {
    // Only the session cookie goes to Better Auth: nothing else of the
    // request can sign anyone in.
    const sessionHeaders = new Headers();
    sessionHeaders.set(
      "cookie",
      `${cookie}=${cookieValue(headers.get("cookie"), cookie)}`,
    );
    const { headers: responseHeaders, response } = await auth.api.getSession({
      headers: sessionHeaders,
      query: { disableRefresh: !options.refresh },
      returnHeaders: true,
    });
    const session = response as Session | null;
    const setCookie = responseHeaders?.getSetCookie() ?? [];
    if (!session || isBanned(session.user)) {
      return unauthenticated(rt, route, false);
    }
    if (!cookieWriteAllowed(method, headers)) {
      return { status: 403, code: "forbidden_origin" };
    }
    const role = serverRole(session.user.role);
    const mfa = session.session.secondFactor === true;
    const claims: IdentityClaims = {
      sub: session.user.id,
      sid: session.session.id,
      via: "session",
      role,
      ...spacesOf(session.user.id),
      ...(mfa ? { mfa: true as const } : {}),
    };
    const token = rt.tokens.get(
      `session:${session.session.id}`,
      `${session.user.authzVersion ?? 0}:${role}:${mfa}`,
      () => rt.signer.sign(claims),
    );
    return {
      status: 204,
      token,
      principal: {
        userId: session.user.id,
        role,
        via: "session",
        sessionId: session.session.id,
        sessionCreatedAt: new Date(session.session.createdAt),
        mfa,
      },
      ...(setCookie.length ? { setCookie } : {}),
    };
  }

  if (!(await rt.legacy.get())) {
    return unauthenticated(rt, route, false);
  }
  if (!anonymousWriteAllowed(method, headers)) {
    return { status: 403, code: "forbidden_origin" };
  }
  // The security log keeps legacy access's use, counted per address and
  // device a day (docs/architecture/multi-user.md#legacy-access).
  if (options.count !== false) rt.legacyUses?.note(headers);
  const token = rt.tokens.get("legacy", "legacy", () =>
    rt.signer.sign(legacyClaims()),
  );
  return {
    status: 204,
    token,
    principal: { userId: FIRST_USER_ID, role: "owner", via: "legacy" },
  };
}
