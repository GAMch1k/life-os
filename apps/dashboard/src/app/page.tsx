import { headers } from "next/headers";

import { CaptureCard } from "@/components/capture-card";
import { HomeAttentionSurface } from "@/components/home-attention";
import { MemberStatusAlert, StatusAlert } from "@/components/runtime-status";
import { statusGate, statusView } from "@/lib/admin-surfaces";
import { isMemberStatus } from "@/lib/member-status";
import { authRuntime } from "@/lib/auth/runtime";
import { getHomeAttention } from "@/lib/home-attention";
import { getInboxBacklog } from "@/lib/inbox-summary";
import { getRuntimeStatus } from "@/lib/runtime-status";
import { dayKey } from "@/lib/time-zone";
import { userTimeZone } from "@/lib/user-time-zone";

export const dynamic = "force-dynamic";

// Capture first: the composer (text and voice) is the first thing on the
// page on every screen size. Runtime problems and summaries follow it.
export default async function Home() {
  const requestHeaders = new Headers(await headers());
  const [attention, backlog, runtime, gate, timeZone] = await Promise.all([
    getHomeAttention(),
    getInboxBacklog(),
    getRuntimeStatus(),
    statusGate(authRuntime(), requestHeaders),
    userTimeZone(),
  ]);
  // Members see Status's one line (src/lib/admin-surfaces.ts).
  const status = statusView(gate, runtime);
  return (
    <div className="mx-auto max-w-5xl space-y-3">
      <h1 className="sr-only">Overview</h1>
      <CaptureCard
        backlog={backlog}
        focusOnDesktop
        renderedAt={new Date().toISOString()}
        timeZone={timeZone}
      />
      {isMemberStatus(status) ? (
        <MemberStatusAlert status={status} />
      ) : (
        <StatusAlert status={status} />
      )}
      <HomeAttentionSurface
        attention={attention}
        humanReviewCount={backlog?.humanReviewCount}
        timeZone={timeZone}
        today={dayKey(new Date(), timeZone)}
      />
    </div>
  );
}
