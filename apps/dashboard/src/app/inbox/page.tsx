import { InboxSummary } from "@/components/inbox-summary";
import { HumanReviewQueue } from "@/components/human-review-queue";
import { getHumanReviewItems } from "@/lib/human-review";
import { getInboxSummary } from "@/lib/inbox-summary";
export const dynamic = "force-dynamic";
export default async function InboxPage() {
  const [inbox, humanReview] = await Promise.all([
    getInboxSummary(),
    getHumanReviewItems(),
  ]);
  return (
    <div className="space-y-6">
      <HumanReviewQueue initialItems={humanReview} />
      <InboxSummary state={inbox} />
    </div>
  );
}
