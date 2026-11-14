import { getRuntimeStatus } from "@/lib/runtime-status";
import { runtimeStatusResponse } from "@/lib/status-response";

// The full runtime status for the monitor, which reads it every minute over
// the private network (MONITOR_STATUS_URL). Like every /internal/ route,
// the gateway answers it with 404 from outside; until services sign their
// requests (P21-07), being on the private network is what lets the monitor
// in. Browsers read /api/status, behind the sign-in.
export const dynamic = "force-dynamic";

export async function GET() {
  return runtimeStatusResponse(await getRuntimeStatus());
}
