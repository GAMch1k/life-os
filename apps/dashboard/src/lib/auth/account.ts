import { credentialsRoute } from "./account-credentials";
import { type CheckRuntime, checkRequest, type Principal } from "./check";
import type { ServerRole } from "./constants";
import { hasSecondFactor } from "./roles";
import {
  isSecurityKind,
  listSecurityEvents,
  recordSecurityEvent,
} from "./security-log";
import { firstAccountExists, setLegacyAccess } from "./store";

/**
 * Life OS's own account API, /api/account (next to Better Auth's
 * /api/auth): who is signed in, the sessions list, and turning legacy
 * access off from Settings → Security (docs/frontend/dashboard.md). Turning
 * it on is the host's alone (./setup.sh legacy-access on).
 */

export type AccountInfo = {
  /** How this request is signed in: a session, or legacy access. */
  via: Principal["via"];
  user: {
    id: string;
    username: string | null;
    name: string;
    role: ServerRole;
  } | null;
  /** TOTP turned on, or a passkey. */
  secondFactor: boolean;
  legacyAccess: boolean;
  /** Whether any account exists (else /setup is where to start). */
  accountExists: boolean;
};

export type SessionInfo = {
  id: string;
  current: boolean;
  createdAt: string;
  updatedAt: string;
  expiresAt: string;
  ipAddress: string | null;
  userAgent: string | null;
};

function json(status: number, body: unknown) {
  return Response.json(body, {
    status,
    headers: { "cache-control": "no-store" },
  });
}

function problem(status: number, code: string, message: string) {
  return json(status, { error: { code, message } });
}

export async function accountInfo(
  rt: CheckRuntime,
  principal: Principal,
): Promise<AccountInfo> {
  const [legacyAccess, accountExists] = await Promise.all([
    rt.legacy.get(),
    firstAccountExists(rt.pool),
  ]);
  if (principal.via !== "session") {
    return {
      via: principal.via,
      user: null,
      secondFactor: false,
      legacyAccess,
      accountExists,
    };
  }
  const { rows } = await rt.pool.query<{
    id: string;
    username: string | null;
    name: string;
    role: ServerRole;
  }>(
    'SELECT id, "displayUsername" AS username, name, role FROM "user" WHERE id = $1',
    [principal.userId],
  );
  return {
    via: "session",
    user: rows[0] ?? null,
    secondFactor: await hasSecondFactor(rt.pool, principal.userId),
    legacyAccess,
    accountExists,
  };
}

export async function listSessions(
  rt: CheckRuntime,
  principal: Principal,
): Promise<SessionInfo[]> {
  const { rows } = await rt.pool.query<{
    id: string;
    createdAt: Date;
    updatedAt: Date;
    expiresAt: Date;
    ipAddress: string | null;
    userAgent: string | null;
  }>(
    `SELECT id, "createdAt", "updatedAt", "expiresAt", "ipAddress", "userAgent"
     FROM session WHERE "userId" = $1 AND "expiresAt" > now()
     ORDER BY "updatedAt" DESC`,
    [principal.userId],
  );
  return rows.map((row) => ({
    id: row.id,
    current: row.id === principal.sessionId,
    createdAt: row.createdAt.toISOString(),
    updatedAt: row.updatedAt.toISOString(),
    expiresAt: row.expiresAt.toISOString(),
    ipAddress: row.ipAddress,
    userAgent: row.userAgent,
  }));
}

/** /api/account and below. Every write needs Life OS's own origin. */
export async function accountHandler(
  rt: CheckRuntime,
  request: Request,
): Promise<Response> {
  const { pathname } = new URL(request.url);
  const check = await checkRequest(rt, request.method, request.headers, {
    uri: pathname,
  });
  // A personal token is never enough to manage the account.
  if (check.status === 403 && check.code === "insufficient_scope") {
    return problem(
      401,
      "sign_in_required",
      "Sign in with your account to do this.",
    );
  }
  if (check.status === 403) {
    return problem(
      403,
      "forbidden_origin",
      "This request did not come from Life OS.",
    );
  }
  if (check.status === 401) {
    return problem(401, "unauthenticated", "Sign in first.");
  }
  const principal = check.principal;

  if (pathname === "/api/account" && request.method === "GET") {
    return json(200, await accountInfo(rt, principal));
  }

  // Everything else is the account's own: legacy access, a device key, or
  // a token is not enough.
  if (principal.via !== "session") {
    return problem(
      401,
      "sign_in_required",
      "Sign in with your account to do this.",
    );
  }

  const credentials = await credentialsRoute(rt, request, principal, pathname);
  if (credentials) return credentials;

  if (pathname === "/api/account/sessions" && request.method === "GET") {
    return json(200, { sessions: await listSessions(rt, principal) });
  }

  // The security log's entries that concern this account
  // (docs/architecture/multi-user.md#logs).
  if (pathname === "/api/account/security-log" && request.method === "GET") {
    const params = new URL(request.url).searchParams;
    const kind = params.get("kind");
    return json(
      200,
      await listSecurityEvents(rt.pool, {
        userId: principal.userId,
        viewer: principal.userId,
        kind: isSecurityKind(kind) ? kind : undefined,
        cursor: params.get("cursor") ?? undefined,
        limit: Number(params.get("limit")) || undefined,
      }),
    );
  }

  const revoke = pathname.match(/^\/api\/account\/sessions\/([0-9a-f-]{36})$/);
  if (revoke && request.method === "DELETE") {
    const { rowCount } = await rt.pool.query(
      'DELETE FROM session WHERE id = $1 AND "userId" = $2',
      [revoke[1], principal.userId],
    );
    rt.tokens.forget(`session:${revoke[1]}`);
    if (!rowCount)
      return problem(404, "not_found", "There is no such session.");
    await recordSecurityEvent(rt.pool, {
      kind: "sessions_revoked",
      actorUserId: principal.userId,
      subjectUserId: principal.userId,
      via: "session",
      headers: request.headers,
      detail: "one",
    });
    return new Response(null, { status: 204 });
  }

  if (pathname === "/api/account/legacy-access" && request.method === "POST") {
    if (principal.role !== "owner") {
      return problem(
        403,
        "owner_only",
        "Only the server owner changes legacy access.",
      );
    }
    let enabled: unknown;
    try {
      enabled = ((await request.json()) as { enabled?: unknown }).enabled;
    } catch {
      enabled = undefined;
    }
    if (typeof enabled !== "boolean") {
      return problem(400, "bad_request", 'Send {"enabled": false}.');
    }
    // Turning it on turns sign-in off for the whole server: only from the
    // host (./setup.sh legacy-access on), never with a browser's session,
    // which might be an old or a stolen one.
    if (enabled) {
      await recordSecurityEvent(rt.pool, {
        kind: "legacy_access_changed",
        outcome: "refused",
        actorUserId: principal.userId,
        subjectUserId: principal.userId,
        via: "session",
        headers: request.headers,
        detail: "on:host_only",
      });
      return problem(
        403,
        "host_only",
        "Turn legacy access on from the server: ./setup.sh legacy-access on.",
      );
    }
    await setLegacyAccess(rt.pool, false, principal.userId);
    rt.legacy.invalidate();
    rt.tokens.forget("legacy");
    await recordSecurityEvent(rt.pool, {
      kind: "legacy_access_changed",
      actorUserId: principal.userId,
      subjectUserId: principal.userId,
      via: "session",
      headers: request.headers,
      detail: "off",
    });
    return json(200, { legacyAccess: false });
  }

  return problem(404, "not_found", "Not found");
}
