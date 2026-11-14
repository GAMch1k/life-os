import { type CheckRuntime, checkRequest, type Principal } from "./check";
import type { ServerRole } from "./constants";
import { hasSecondFactor } from "./roles";
import { accountCount, firstAccountExists, setLegacyAccess } from "./store";

/**
 * Life OS's own account API, /api/account (next to Better Auth's
 * /api/auth): who is signed in, the sessions list, and the legacy access
 * switch of Settings → Security (docs/frontend/dashboard.md).
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
  const check = await checkRequest(rt, request.method, request.headers);
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

  // Everything else is the account's own: legacy access is not enough.
  if (principal.via !== "session") {
    return problem(
      401,
      "sign_in_required",
      "Sign in with your account to do this.",
    );
  }

  if (pathname === "/api/account/sessions" && request.method === "GET") {
    return json(200, { sessions: await listSessions(rt, principal) });
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
      return problem(
        400,
        "bad_request",
        'Send {"enabled": true} or {"enabled": false}.',
      );
    }
    const result = await setLegacyAccess(rt.pool, enabled, principal.userId);
    if (result === "accounts") {
      return problem(
        409,
        "more_than_one_account",
        "Legacy access stays off while more than one account exists.",
      );
    }
    rt.legacy.invalidate();
    rt.tokens.forget("legacy");
    return json(200, {
      legacyAccess: enabled,
      accounts: await accountCount(rt.pool),
    });
  }

  return problem(404, "not_found", "Not found");
}
