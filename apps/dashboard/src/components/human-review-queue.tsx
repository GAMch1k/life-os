"use client";

import { useState, useTransition } from "react";
import { Check, Trash2, TriangleAlert } from "lucide-react";

import { InboxSection, SectionUnavailable } from "@/components/inbox-summary";
import { Button } from "@/components/ui/button";
import { inboxClient } from "@/lib/api/inbox-client";
import type { HumanReviewItem } from "@/lib/human-review";
import { formatInboxTime, shownOfTotal } from "@/lib/inbox-summary";

type Candidate = { id?: string; title?: string; notes?: string };
type Review = { errorSummary?: string; candidates?: Candidate[] };

export function HumanReviewQueue({
  initialItems,
  renderedAt,
  timeZone,
  total: initialTotal,
}: {
  /** `null` when Inbox could not be read. */
  initialItems: HumanReviewItem[] | null;
  renderedAt: string;
  timeZone: string;
  /** The true number waiting, from the Inbox backlog. */
  total?: number;
}) {
  const [items, setItems] = useState(initialItems);
  const [total, setTotal] = useState(
    initialItems
      ? Math.max(initialTotal ?? 0, initialItems.length)
      : initialTotal,
  );
  const [error, setError] = useState<string>();
  const [pending, startTransition] = useTransition();
  const now = new Date(renderedAt);
  function reviewFor(item: HumanReviewItem): Review {
    return (item.processingMetadata?.humanReview as Review | undefined) ?? {};
  }
  function remove(itemId: string) {
    setItems(
      (current) => current?.filter((item) => item.id !== itemId) ?? null,
    );
    setTotal((current) => (current ? current - 1 : current));
  }
  function choose(itemId: string, candidateId: string) {
    setError(undefined);
    startTransition(async () => {
      const { data, error: requestError } = await inboxClient.POST(
        "/v1/inbox-items/{itemId}/human-review/resolve",
        { params: { path: { itemId } }, body: { candidateId } },
      );
      if (requestError || !data) {
        setError("Could not submit that review decision. Please try again.");
        return;
      }
      remove(itemId);
    });
  }
  // Discarding keeps the capture in Inbox history but takes it out of review
  // and processing; it uses the same Inbox operation as anywhere else.
  function discard(itemId: string) {
    setError(undefined);
    startTransition(async () => {
      const { data, error: requestError } = await inboxClient.POST(
        "/v1/inbox-items/{itemId}/discard",
        { params: { path: { itemId } } },
      );
      if (requestError || !data) {
        setError("Could not discard that capture. Please try again.");
        return;
      }
      remove(itemId);
    });
  }
  if (items && !items.length && !total) return null;
  return (
    <InboxSection
      count={total}
      icon={<TriangleAlert aria-hidden="true" className="size-4" />}
      id="human-review"
      note={items ? shownOfTotal(items.length, total) : undefined}
      title="Human Review"
    >
      {items ? (
        <div className="space-y-2">
          {items.map((item) => {
            const review = reviewFor(item);
            const candidates = (review.candidates ?? []).filter(
              (candidate) => candidate.id,
            );
            return (
              <section className="rounded-lg border p-3" key={item.id}>
                <div className="flex items-start gap-3">
                  <p className="min-w-0 flex-1 font-medium wrap-anywhere whitespace-pre-line">
                    {item.content}
                  </p>
                  <time
                    className="text-muted-foreground shrink-0 text-xs tabular-nums"
                    dateTime={item.createdAt}
                  >
                    {formatInboxTime(item.createdAt, timeZone, now)}
                  </time>
                </div>
                <p className="text-muted-foreground mt-1">
                  {review.errorSummary ?? "Choose the intended Task."}
                </p>
                {candidates.length === 0 ? (
                  <p className="text-muted-foreground mt-1">
                    Life OS found nothing it can safely do with this capture.
                    Discard it, or capture it again with more detail.
                  </p>
                ) : null}
                <div className="mt-2 flex flex-wrap gap-2">
                  {candidates.map((candidate) => (
                    <Button
                      className="h-auto min-h-8 max-w-full py-1 text-left whitespace-normal"
                      disabled={pending}
                      key={candidate.id}
                      onClick={() => choose(item.id, candidate.id!)}
                      variant="outline"
                    >
                      <Check /> {candidate.title ?? candidate.id}
                    </Button>
                  ))}
                  <Button
                    aria-label={`Discard capture: ${item.content}`}
                    disabled={pending}
                    onClick={() => discard(item.id)}
                    variant="ghost"
                  >
                    <Trash2 /> Discard
                  </Button>
                </div>
              </section>
            );
          })}
        </div>
      ) : (
        <SectionUnavailable />
      )}
      {error ? <p className="text-destructive text-sm">{error}</p> : null}
    </InboxSection>
  );
}
