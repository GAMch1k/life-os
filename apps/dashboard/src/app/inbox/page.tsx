import { CaptureCard } from "@/components/capture-card";
import { FailedCaptures } from "@/components/failed-captures";
import { HumanReviewQueue } from "@/components/human-review-queue";
import {
  InboxCounts,
  PendingSection,
  ProcessedSection,
} from "@/components/inbox-summary";
import { getHumanReviewItems } from "@/lib/human-review";
import { getInboxOverview } from "@/lib/inbox-summary";
import { userTimeZone } from "@/lib/user-time-zone";

export const dynamic = "force-dynamic";

// Every capture appears once, in the section for its state: Human Review
// (decisions), Pending (automatic work, including scheduled retries), Failed
// (automatic processing gave up), and Recently processed.
export default async function InboxPage() {
  const [overview, humanReview, timeZone] = await Promise.all([
    getInboxOverview(),
    getHumanReviewItems(),
    userTimeZone(),
  ]);
  const { backlog, failed, pending, processed } = overview;
  const renderedAt = new Date().toISOString();
  const now = new Date(renderedAt);
  const unavailable =
    !backlog && !humanReview && !pending && !failed && !processed;
  return (
    <div className="mx-auto max-w-5xl space-y-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <h1 className="text-xl font-semibold tracking-tight">Inbox</h1>
        {unavailable ? null : <InboxCounts backlog={backlog} />}
      </div>
      <CaptureCard renderedAt={renderedAt} timeZone={timeZone} />
      {unavailable ? (
        <p className="text-muted-foreground text-sm">
          Inbox is unavailable right now.
        </p>
      ) : (
        <>
          <HumanReviewQueue
            initialItems={humanReview}
            renderedAt={renderedAt}
            timeZone={timeZone}
            total={backlog?.humanReviewCount}
          />
          <PendingSection
            backlog={backlog}
            items={pending}
            now={now}
            timeZone={timeZone}
          />
          <FailedCaptures
            initialItems={failed}
            renderedAt={renderedAt}
            timeZone={timeZone}
            total={backlog?.failedCount}
          />
          <ProcessedSection items={processed} now={now} timeZone={timeZone} />
        </>
      )}
    </div>
  );
}
