import { CaptureCard } from "@/components/capture-card";
import { HomeAttentionSurface } from "@/components/home-attention";
import { StatusAlert } from "@/components/runtime-status";
import { getHomeAttention } from "@/lib/home-attention";
import { getInboxBacklog } from "@/lib/inbox-summary";
import { getRuntimeStatus } from "@/lib/runtime-status";
import { userTimeZone } from "@/lib/user-time-zone";

export const dynamic = "force-dynamic";

// Capture first: the composer (text and voice) is the first thing on the
// page on every screen size. Runtime problems and summaries follow it.
export default async function Home() {
  const [attention, backlog, status, timeZone] = await Promise.all([
    getHomeAttention(),
    getInboxBacklog(),
    getRuntimeStatus(),
    userTimeZone(),
  ]);
  return (
    <div className="mx-auto max-w-5xl space-y-3">
      <h1 className="sr-only">Overview</h1>
      <CaptureCard
        backlog={backlog}
        focusOnDesktop
        renderedAt={new Date().toISOString()}
        timeZone={timeZone}
      />
      <StatusAlert status={status} />
      <HomeAttentionSurface
        attention={attention}
        humanReviewCount={backlog?.humanReviewCount}
        timeZone={timeZone}
      />
    </div>
  );
}
