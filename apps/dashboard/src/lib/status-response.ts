import type { RuntimeStatus } from "@/lib/runtime-status";

/**
 * The runtime status as `GET /api/status` returns it: the checks the Status
 * page renders, as JSON, never cached. The monitor service polls it and
 * notifies the owner when a check breaks or recovers, so the Status rules
 * live in one place (docs/services/monitor.md).
 */
export function runtimeStatusResponse(status: RuntimeStatus): Response {
  return Response.json(status, { headers: { "cache-control": "no-store" } });
}
