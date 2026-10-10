"use client";

import { CircleX, Trash2 } from "lucide-react";
import { useState, useTransition } from "react";

import {
  InboxItemRow,
  InboxSection,
  SectionUnavailable,
} from "@/components/inbox-summary";
import { Button } from "@/components/ui/button";
import { inboxClient } from "@/lib/api/inbox-client";
import { type FailedInboxItem, shownOfTotal } from "@/lib/inbox-summary";

/**
 * Captures automatic processing gave up on. They are no longer retried; the
 * owner can discard them (the same Inbox operation as in Human Review).
 */
export function FailedCaptures({
  initialItems,
  renderedAt,
  timeZone,
  total: initialTotal,
}: {
  initialItems: FailedInboxItem[] | null;
  renderedAt: string;
  timeZone: string;
  total?: number;
}) {
  const [items, setItems] = useState(initialItems);
  const [total, setTotal] = useState(initialTotal ?? initialItems?.length);
  // A router.refresh(), after a capture on this page settled, brings the
  // current list; it replaces what this section holds.
  const [loaded, setLoaded] = useState(initialItems);
  if (loaded !== initialItems) {
    setLoaded(initialItems);
    setItems(initialItems);
    setTotal(initialTotal ?? initialItems?.length);
  }
  const [error, setError] = useState<string>();
  const [pending, startTransition] = useTransition();
  const now = new Date(renderedAt);

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
      setItems(
        (current) => current?.filter((item) => item.id !== itemId) ?? null,
      );
      setTotal((current) => (current ? current - 1 : current));
    });
  }

  if (items && !items.length && !total) return null;
  return (
    <InboxSection
      count={total}
      icon={<CircleX aria-hidden="true" className="text-destructive size-4" />}
      id="failed"
      note={items ? shownOfTotal(items.length, total) : undefined}
      title="Failed"
    >
      {items ? (
        <ul className="divide-y">
          {items.map((item) => (
            <InboxItemRow
              action={
                <Button
                  aria-label={`Discard capture: ${item.content}`}
                  disabled={pending}
                  onClick={() => discard(item.id)}
                  size="xs"
                  variant="ghost"
                >
                  <Trash2 /> Discard
                </Button>
              }
              at={item.updatedAt}
              item={item}
              key={item.id}
              now={now}
              timeZone={timeZone}
            >
              {item.errorSummary ? (
                <p className="text-muted-foreground mt-0.5 line-clamp-2 text-xs wrap-anywhere">
                  {item.errorSummary}
                </p>
              ) : null}
            </InboxItemRow>
          ))}
        </ul>
      ) : (
        <SectionUnavailable />
      )}
      {error ? <p className="text-destructive text-sm">{error}</p> : null}
    </InboxSection>
  );
}
