import {
  type CheckResult,
  type CheckRuntime,
  checkRequest,
  type Principal,
} from "./check";
import { checkAdmin } from "./roles";
import {
  isSecurityKind,
  listSecurityEvents,
  securityLogUsers,
} from "./security-log";

/**
 * The gate of the admin surfaces (docs/architecture/multi-user.md#roles-and-checks):
 * a signed-in owner or admin with two-factor sign-in (TOTP or a passkey),
 * and for sensitive actions a sign-in from the last 12 hours.
 *
 * Legacy access covers what Life OS did before accounts: the surfaces that
 * existed then (Status's details, the backups, Back up now) let it in as
 * the owner, as before; the ones that are new with accounts (the security
 * log of every account, diagnostics) need a real sign-in.
 */

export type AdminRefusalCode =
  | "unauthenticated"
  | "forbidden_origin"
  | "sign_in_required"
  | "admin_only"
  | "second_factor_required"
  | "session_not_fresh";

export const ADMIN_MESSAGES: Record<AdminRefusalCode, string> = {
  unauthenticated: "Sign in first.",
  forbidden_origin: "This request did not come from Life OS.",
  sign_in_required: "Sign in with your account to do this.",
  admin_only: "Only the server's admins can do this.",
  second_factor_required:
    "Admin pages need two-factor sign-in: turn on an authenticator app or add a passkey in Settings → Security.",
  session_not_fresh: "Sign in again to do this.",
};

export type AdminGate =
  | { ok: true; principal: Principal }
  | {
      ok: false;
      status: 401 | 403;
      code: AdminRefusalCode;
      /** Who asked, when the request was signed in at all. */
      principal: Principal | null;
    };

export type AdminGateOptions = {
  /** Legacy access counts as the owner (surfaces from before accounts). */
  legacy: "allow" | "refuse";
  /** A sensitive action: the sign-in must be younger than 12 hours. */
  sensitive?: boolean;
};

export async function adminGate(
  rt: CheckRuntime,
  result: CheckResult,
  options: AdminGateOptions,
): Promise<AdminGate> {
  if (result.status === 401) {
    return { ok: false, status: 401, code: "unauthenticated", principal: null };
  }
  if (result.status === 403) {
    return {
      ok: false,
      status: 403,
      code: "forbidden_origin",
      principal: null,
    };
  }
  const principal = result.principal;
  if (principal.via === "legacy") {
    return options.legacy === "allow"
      ? { ok: true, principal }
      : { ok: false, status: 401, code: "sign_in_required", principal };
  }
  if (principal.via !== "session") {
    return { ok: false, status: 403, code: "admin_only", principal };
  }
  const check = await checkAdmin(
    rt.pool,
    {
      user: { id: principal.userId, role: principal.role },
      session: { createdAt: principal.sessionCreatedAt ?? 0 },
    },
    { sensitive: options.sensitive },
  );
  return check.ok
    ? { ok: true, principal }
    : { ok: false, status: check.status, code: check.code, principal };
}

/** A refusal as the services' errors look. */
export function adminProblem(gate: Extract<AdminGate, { ok: false }>) {
  return Response.json(
    { error: { code: gate.code, message: ADMIN_MESSAGES[gate.code] } },
    { status: gate.status, headers: { "cache-control": "no-store" } },
  );
}

function json(status: number, body: unknown) {
  return Response.json(body, {
    status,
    headers: { "cache-control": "no-store" },
  });
}

function instant(value: string | null) {
  if (!value) return undefined;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? undefined : date;
}

/**
 * The admin API, /api/admin and below (it checks the request itself, like
 * /api/account): the security log of every account, with filters, for a
 * signed-in admin with two-factor sign-in. Users, invites, and limits join
 * it with later tickets (P21-12, P21-13).
 */
export async function adminHandler(
  rt: CheckRuntime,
  request: Request,
): Promise<Response> {
  const url = new URL(request.url);
  const gate = await adminGate(
    rt,
    await checkRequest(rt, request.method, request.headers),
    { legacy: "refuse" },
  );
  if (!gate.ok) return adminProblem(gate);

  if (url.pathname === "/api/admin/security-log" && request.method === "GET") {
    const params = url.searchParams;
    const kind = params.get("kind");
    const user = params.get("user");
    const [page, users] = await Promise.all([
      listSecurityEvents(rt.pool, {
        userId: user ?? undefined,
        kind: isSecurityKind(kind) ? kind : undefined,
        from: instant(params.get("from")),
        to: instant(params.get("to")),
        cursor: params.get("cursor") ?? undefined,
        limit: Number(params.get("limit")) || undefined,
      }),
      securityLogUsers(rt.pool),
    ]);
    return json(200, { ...page, users });
  }
  return json(404, { error: { code: "not_found", message: "Not found" } });
}
