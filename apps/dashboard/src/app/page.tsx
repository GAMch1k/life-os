import { HomeAttentionSurface } from "@/components/home-attention";
import { InboxSummary } from "@/components/inbox-summary";
import { VoiceCapture } from "@/components/voice-capture";
import { getHomeAttention } from "@/lib/home-attention";
import { getInboxSummary } from "@/lib/inbox-summary";

export const dynamic = "force-dynamic";

export default async function Home() {
  const [attention, inbox] = await Promise.all([
    getHomeAttention(),
    // The capture card degrades to its own unavailable state when Inbox is
    // unreachable instead of failing the whole page.
    getInboxSummary().catch(() => ({ kind: "unavailable" }) as const),
  ]);
  return (
    <div className="space-y-8">
      <HomeAttentionSurface attention={attention} />
      <section
        aria-labelledby="capture-heading"
        className="mx-auto max-w-5xl space-y-4"
      >
        <h2 className="text-lg font-semibold" id="capture-heading">
          Capture
        </h2>
        <div className="grid gap-4 md:grid-cols-2">
          <InboxSummary state={inbox} />
          <VoiceCapture />
        </div>
      </section>
    </div>
  );
}
