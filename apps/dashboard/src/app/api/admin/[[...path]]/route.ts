import { adminHandler } from "@/lib/auth/admin";
import { authRuntime } from "@/lib/auth/runtime";

// The admin API: the security log of every account (src/lib/auth/admin.ts).
// It checks the request itself: a signed-in owner or admin with two-factor
// sign-in; legacy access is not enough.
export const dynamic = "force-dynamic";

export function GET(request: Request) {
  return adminHandler(authRuntime(), request);
}
