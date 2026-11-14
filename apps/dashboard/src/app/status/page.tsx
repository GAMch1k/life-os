import { headers } from "next/headers";

import {
  MemberStatusView,
  RuntimeStatusView,
} from "@/components/runtime-status";
import { statusGate, statusView } from "@/lib/admin-surfaces";
import { isMemberStatus } from "@/lib/member-status";
import { authRuntime } from "@/lib/auth/runtime";
import { getRuntimeStatus } from "@/lib/runtime-status";

export const dynamic = "force-dynamic";

/**
 * Status: every check with its details for the server's admins (and legacy
 * access, as the owner); one line for members (src/lib/admin-surfaces.ts).
 */
export default async function StatusPage() {
  const requestHeaders = new Headers(await headers());
  const [gate, status] = await Promise.all([
    statusGate(authRuntime(), requestHeaders),
    getRuntimeStatus(),
  ]);
  const view = statusView(gate, status);
  return isMemberStatus(view) ? (
    <MemberStatusView status={view} />
  ) : (
    <RuntimeStatusView status={view} />
  );
}
