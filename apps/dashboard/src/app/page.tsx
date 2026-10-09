import { HomeAttentionSurface } from "@/components/home-attention";
import { FinanceSummary } from "@/components/finance-summary";
import { InboxSummary } from "@/components/inbox-summary";
import { VoiceCapture } from "@/components/voice-capture";
import { getFinanceSummary } from "@/lib/finance-summary";
import { getHomeAttention } from "@/lib/home-attention";
import { getInboxSummary } from "@/lib/inbox-summary";

export const dynamic = "force-dynamic";

export default async function Home() {
  const [attention, finance, inbox] = await Promise.all([
    getHomeAttention(),
    getFinanceSummary(),
    getInboxSummary(),
  ]);
  return (
    <div className="space-y-8">
      <HomeAttentionSurface attention={attention} />
      <section className="mx-auto grid max-w-5xl gap-4 md:grid-cols-2">
        <InboxSummary state={inbox} />
        <VoiceCapture />
      </section>
      <div className="mx-auto max-w-5xl">
        <FinanceSummary state={finance} />
      </div>
    </div>
  );
}
