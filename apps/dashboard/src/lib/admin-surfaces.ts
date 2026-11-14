import { adminGate, adminProblem, type AdminGate } from "@/lib/auth/admin";
import { type CheckRuntime, checkRequest } from "@/lib/auth/check";
import { recordSecurityEvent } from "@/lib/auth/security-log";
import {
  isMemberStatus,
  memberStatus,
  type MemberStatus,
} from "@/lib/member-status";
import type { RuntimeStatus } from "@/lib/runtime-status";
import { runtimeStatusResponse } from "@/lib/status-response";

/**
 * The Dashboard's surfaces that were the owner's before accounts and are
 * the server's admins' now (docs/architecture/multi-user.md#roles-and-checks):
 * Status's details and the backups. Legacy access still opens them, as
 * the owner, as before; a signed-in owner or admin needs two-factor
 * sign-in; a member gets Status's one line, and no backups. As plain fetch
 * handlers, so that the auth flow suite calls them as the routes do.
 */

/** Who sees Status's details: legacy access, or an admin with a second factor. */
export function statusGate(rt: CheckRuntime, headers: Headers) {
  // The proxy (src/proxy.ts) counted this request already.
  return checkRequest(rt, "GET", headers, { count: false }).then((result) =>
    adminGate(rt, result, { legacy: "allow" }),
  );
}

/** The view of Status a gate allows. */
export function statusView(
  gate: AdminGate,
  status: RuntimeStatus,
): RuntimeStatus | MemberStatus {
  if (gate.ok) return status;
  return memberStatus(
    status,
    gate.code === "second_factor_required" ? gate.code : undefined,
  );
}

/** GET /api/status: the full checks for admins, one line for members. */
export async function statusHandler(
  rt: CheckRuntime,
  request: Request,
  load: () => Promise<RuntimeStatus>,
): Promise<Response> {
  const gate = await statusGate(rt, request.headers);
  if (!gate.ok && gate.principal === null) return adminProblem(gate);
  const view = statusView(gate, await load());
  return isMemberStatus(view)
    ? Response.json(view, { headers: { "cache-control": "no-store" } })
    : runtimeStatusResponse(view);
}

/**
 * What /api/backups passes to the backup service: reading its status and
 * starting a backup of every database. Restore checks and anything else
 * stay on the private network.
 */
export const BACKUP_ROUTES = new Set(["GET v1/status", "POST v1/backups"]);

/**
 * /api/backups/<path>: the backups' status and Back up now, for admins.
 * forward passes the request to the backup service. Starting a backup, and
 * a refused attempt, go into the security log.
 */
export async function backupsHandler(
  rt: CheckRuntime,
  request: Request,
  path: string,
  forward: () => Promise<Response>,
): Promise<Response> {
  const route = `${request.method} ${path}`;
  if (!BACKUP_ROUTES.has(route)) {
    return Response.json(
      { error: { code: "not_found", message: "Not Found" } },
      { status: 404 },
    );
  }
  const result = await checkRequest(rt, request.method, request.headers, {
    count: false,
  });
  const gate = await adminGate(rt, result, { legacy: "allow" });
  const start = route === "POST v1/backups";
  if (!gate.ok) {
    if (start && gate.principal) {
      await recordSecurityEvent(rt.pool, {
        kind: "admin_refused",
        outcome: "refused",
        actorUserId: gate.principal.userId,
        via: gate.principal.via === "legacy" ? "legacy" : "session",
        headers: request.headers,
        detail: `backup:${gate.code}`,
      });
    }
    return adminProblem(gate);
  }
  const response = await forward();
  if (start) {
    const started = response.status === 202 || response.status === 200;
    await recordSecurityEvent(rt.pool, {
      kind: "backup_started",
      outcome: started ? "ok" : "failed",
      actorUserId: gate.principal.userId,
      via: gate.principal.via === "legacy" ? "legacy" : "session",
      headers: request.headers,
      detail: started ? null : `status_${response.status}`,
    });
  }
  return response;
}
