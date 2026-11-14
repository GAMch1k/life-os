import type { Pool, PoolClient } from "pg";

import {
  SERVER_ROLES,
  type ServerRole,
  SESSION_FRESH_SECONDS,
} from "./constants";

/**
 * Server roles and the checks admin surfaces use
 * (docs/architecture/multi-user.md#roles-and-checks). P21-03 gives every
 * account a role; P21-04's admin pages call requireAdmin.
 */

type Queryable = Pick<Pool | PoolClient, "query">;

export function serverRole(value: unknown): ServerRole {
  return SERVER_ROLES.includes(value as ServerRole)
    ? (value as ServerRole)
    : "member";
}

export function isAdminRole(role: unknown) {
  return role === "owner" || role === "admin";
}

/** Whether a session is younger than 12 hours: sensitive actions need it. */
export function isFresh(
  session: { createdAt: Date | string | number },
  now = Date.now(),
) {
  const created = new Date(session.createdAt).getTime();
  return now - created < SESSION_FRESH_SECONDS * 1000;
}

/** TOTP turned on, or at least one passkey. */
export async function hasSecondFactor(
  db: Queryable,
  userId: string,
): Promise<boolean> {
  const { rows } = await db.query<{ ok: boolean }>(
    `SELECT coalesce((SELECT "twoFactorEnabled" FROM "user" WHERE id = $1), false)
         OR EXISTS (SELECT 1 FROM passkey WHERE "userId" = $1) AS ok`,
    [userId],
  );
  return rows[0]?.ok === true;
}

export type AdminCheck =
  | { ok: true }
  | {
      ok: false;
      status: 401 | 403;
      code:
        | "unauthenticated"
        | "admin_only"
        | "second_factor_required"
        | "session_not_fresh";
    };

/**
 * The gate of every admin surface: a signed-in owner or admin (never legacy
 * access) with TOTP or a passkey; with sensitive, also a session younger
 * than 12 hours.
 */
export async function checkAdmin(
  db: Queryable,
  session: {
    user: { id: string; role?: string | null };
    session: { createdAt: Date | string | number };
  } | null,
  options: { sensitive?: boolean } = {},
): Promise<AdminCheck> {
  if (!session) return { ok: false, status: 401, code: "unauthenticated" };
  if (!isAdminRole(session.user.role)) {
    return { ok: false, status: 403, code: "admin_only" };
  }
  if (!(await hasSecondFactor(db, session.user.id))) {
    return { ok: false, status: 403, code: "second_factor_required" };
  }
  if (options.sensitive && !isFresh(session.session)) {
    return { ok: false, status: 403, code: "session_not_fresh" };
  }
  return { ok: true };
}
