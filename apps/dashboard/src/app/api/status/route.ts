import { statusHandler } from "@/lib/admin-surfaces";
import { authRuntime } from "@/lib/auth/runtime";
import { getRuntimeStatus } from "@/lib/runtime-status";

// Evaluated on every request, like the Status page. The full checks for
// the server's admins (and legacy access, as the owner); one line for
// members (src/lib/admin-surfaces.ts).
export const dynamic = "force-dynamic";

export async function GET(request: Request) {
  return statusHandler(authRuntime(), request, () => getRuntimeStatus());
}
