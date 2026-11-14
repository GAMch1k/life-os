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
import {
  anonymousWriteAllowed,
  cookieWriteAllowed,
  requestHost,
} from "./origin";
import { serverRole } from "./roles";
import type { LegacyUseLog } from "./security-log";
import type { LifeOsAuth } from "./server";
import { legacyAccess } from "./store";

/**
 * The auth check (docs/architecture/multi-user.md#the-gateway): who makes a
 * request, from its credential, and the identity token for the services.
 * The gateway asks it for every service route (/internal/auth/check), the
 * Dashboard's proxy (src/proxy.ts) for its own pages and routes.
 *
 *  - The session cookie: the session is read from the database on every
 *    request (Better Auth's cookie cache is off), so a sign-out or a ban
 *    counts at once. A write must come from Life OS's own origin.
 *  - No credential: legacy access, while it is on, is the first account,
 *    as Life OS was before accounts; while it is off, 401.
 *  - Authorization headers are not credentials until P21-06 (device keys,
 *    tokens, MCP) and are ignored, as before.
 */

export type Principal = {
  userId: string;
  role: ServerRole;
  via: Via;
  /** The session's id; absent for legacy access. */
  sessionId?: string;
  /** When the session was created, for sensitive actions (12 hours). */
  sessionCreatedAt?: Date;
};

export type CheckResult =
  | {
      status: 204;
      token: string;
      principal: Principal;
      /** A renewed session cookie (with refresh), for the response. */
      setCookie?: string[];
    }
  | { status: 401; code: "unauthenticated" }
  | { status: 403; code: "forbidden_origin" };

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
  session: { id: string; createdAt: Date | string };
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
 * Checks a request: its method and headers (for the gateway, the original
 * request's, which it sends as X-Original-Method).
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
  } = {},
): Promise<CheckResult> {
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
      return { status: 401, code: "unauthenticated" };
    }
    if (!cookieWriteAllowed(method, headers)) {
      return { status: 403, code: "forbidden_origin" };
    }
    const role = serverRole(session.user.role);
    const claims: IdentityClaims = {
      sub: session.user.id,
      sid: session.session.id,
      via: "session",
      role,
      ...spacesOf(session.user.id),
    };
    const token = rt.tokens.get(
      `session:${session.session.id}`,
      `${session.user.authzVersion ?? 0}:${role}`,
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
      },
      ...(setCookie.length ? { setCookie } : {}),
    };
  }

  if (!(await rt.legacy.get())) {
    return { status: 401, code: "unauthenticated" };
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
