import { getRuntimeStatus } from "@/lib/runtime-status";
import { runtimeStatusResponse } from "@/lib/status-response";

// Evaluated on every request, like the Status page.
export const dynamic = "force-dynamic";

export async function GET() {
  return runtimeStatusResponse(await getRuntimeStatus());
}
