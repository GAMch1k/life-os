import { inboxClient } from "@/lib/api/inbox-client";
import type { components } from "@/lib/api/generated/inbox";

export type HumanReviewItem = components["schemas"]["InboxItem"];

/** Captures shown in the /inbox review queue; the true count is the backlog's. */
export const HUMAN_REVIEW_LIMIT = 50;

/**
 * How many captures wait in review: the backlog's true count, but never fewer
 * than are listed. Unknown when the backlog is.
 */
export function reviewTotal(
  items: readonly unknown[] | null,
  total: number | undefined,
) {
  return items ? Math.max(total ?? 0, items.length) : total;
}

/**
 * What a Human Review edit adds to its new capture's `sourceMetadata` (text,
 * or voice, which Voice passes on to the Inbox item): the capture it replaces.
 */
export function replacementMetadata(itemId: string) {
  return { replacesItemId: itemId };
}

/** What the queue says once an edit replaced a capture. */
export function replacementNotice(kind: "text" | "voice") {
  return kind === "text"
    ? "Captured your edit; the original was discarded."
    : "Recording sent; it becomes a new capture once transcribed. The original was discarded.";
}

/** `null` when Inbox could not be read, so it is not mistaken for "none". */
export async function getHumanReviewItems(): Promise<HumanReviewItem[] | null> {
  try {
    const { data, error } = await inboxClient.GET("/v1/inbox-items", {
      params: { query: { state: "human_review", limit: HUMAN_REVIEW_LIMIT } },
    });
    return error || !data ? null : data.items;
  } catch {
    return null;
  }
}
