import { inboxClient } from "@/lib/api/inbox-client";
import type { components } from "@/lib/api/generated/inbox";
import { DATE_LOCALE, dayKey } from "@/lib/time-zone";

// Inbox state as the Dashboard shows it. Every capture is in exactly one
// bucket: pending automatic work (raw or processing, including scheduled
// retries), Human Review, failed (automatic processing gave up), or done
// (resolved or discarded). Counts come from the Inbox backlog API, never from
// the length of a capped list.

export type InboxItem = components["schemas"]["InboxItem"];
export type InboxBacklog = components["schemas"]["InboxBacklog"];

/** A failed capture with the reason its latest processing attempt gave. */
export type FailedInboxItem = InboxItem & { errorSummary?: string };

/** Rows fetched per /inbox section. */
export const INBOX_SECTION_LIMIT = 50;
/** Recently processed captures shown on /inbox. */
export const PROCESSED_LIMIT = 10;
/** Failed captures whose latest error is looked up. */
export const FAILED_REASON_LIMIT = 10;

/** `null` means that part of Inbox could not be read. */
export type InboxOverview = {
  backlog: InboxBacklog | null;
  pending: InboxItem[] | null;
  failed: FailedInboxItem[] | null;
  processed: InboxItem[] | null;
};

export type InboxItemStatus =
  | { kind: "received" }
  | { kind: "retrying" }
  | { kind: "retry"; at: string; reason?: string }
  | { kind: "processing" }
  | { kind: "review" }
  | { kind: "failed" }
  | { kind: "processed" }
  | { kind: "discarded" };

type StatusFields = Pick<InboxItem, "state" | "nextRetryAt" | "retryReason">;

export function isPendingState(state: InboxItem["state"]) {
  return state === "raw" || state === "processing";
}

/**
 * Whether a followed capture has just left automatic processing: it now
 * waits in Human Review, was processed, failed, or was discarded. What the
 * page loaded about Inbox (and what processing changed) is stale from then on.
 */
export function leftProcessing(
  before: Pick<InboxItem, "state">,
  after: Pick<InboxItem, "state"> | undefined,
) {
  return (
    after !== undefined &&
    isPendingState(before.state) &&
    !isPendingState(after.state)
  );
}

/** Where a capture is right now, from its durable Inbox state. */
export function inboxItemStatus(
  item: StatusFields,
  now: Date,
): InboxItemStatus {
  switch (item.state) {
    case "raw":
      if (item.nextRetryAt && Date.parse(item.nextRetryAt) > now.getTime()) {
        return {
          kind: "retry",
          at: item.nextRetryAt,
          ...(item.retryReason ? { reason: item.retryReason } : {}),
        };
      }
      // A retry was scheduled (and its time has come) or is due immediately.
      return item.nextRetryAt || item.retryReason
        ? { kind: "retrying" }
        : { kind: "received" };
    case "processing":
      return { kind: "processing" };
    case "human_review":
      return { kind: "review" };
    case "failed":
      return { kind: "failed" };
    case "resolved":
      return { kind: "processed" };
    case "discarded":
      return { kind: "discarded" };
  }
}

export function retryReasonLabel(reason: string) {
  if (reason === "usage_limited") return "usage limit";
  if (reason === "backoff") return "backoff";
  return reason.replaceAll("_", " ");
}

const timeFormats = new Map<string, Intl.DateTimeFormat>();

function formatter(timeZone: string, kind: "time" | "date" | "year") {
  const key = `${timeZone}|${kind}`;
  let format = timeFormats.get(key);
  if (!format) {
    format = new Intl.DateTimeFormat(DATE_LOCALE, {
      timeZone,
      hour: "2-digit",
      minute: "2-digit",
      ...(kind === "time" ? {} : { day: "numeric", month: "short" }),
      ...(kind === "year" ? { year: "numeric" } : {}),
    });
    timeFormats.set(key, format);
  }
  return format;
}

/** "14:05" today, "8 Oct, 14:05" this year, "8 Oct 2025, 14:05" otherwise. */
export function formatInboxTime(instant: string, timeZone: string, now: Date) {
  const day = dayKey(instant, timeZone);
  const today = dayKey(now, timeZone);
  const kind =
    day === today
      ? "time"
      : day.slice(0, 4) === today.slice(0, 4)
        ? "date"
        : "year";
  return formatter(timeZone, kind).format(new Date(instant));
}

/** The short status text shown next to a capture. */
export function inboxStatusLabel(
  status: InboxItemStatus,
  timeZone: string,
  now: Date,
) {
  switch (status.kind) {
    case "received":
      return "Received";
    case "retrying":
      return "Retrying";
    case "retry":
      return `Retry ${formatInboxTime(status.at, timeZone, now)}`;
    case "processing":
      return "Processing";
    case "review":
      return "Needs review";
    case "failed":
      return "Failed";
    case "processed":
      return "Processed";
    case "discarded":
      return "Discarded";
  }
}

/** Raw and processing captures as one list, newest first. */
export function mergePending(
  raw: InboxItem[],
  processing: InboxItem[],
  limit = INBOX_SECTION_LIMIT,
) {
  return [...raw, ...processing]
    .sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))
    .slice(0, limit);
}

/**
 * The most recently processed captures. Inbox lists newest captures first, so
 * a wider page is re-sorted by when each one was resolved.
 */
export function recentlyProcessed(items: InboxItem[], limit = PROCESSED_LIMIT) {
  const resolvedAt = (item: InboxItem) =>
    Date.parse(item.resolvedAt ?? item.updatedAt);
  return [...items]
    .sort((a, b) => resolvedAt(b) - resolvedAt(a))
    .slice(0, limit);
}

/** "Showing 50 of 120" when a section holds fewer rows than really exist. */
export function shownOfTotal(shown: number, total: number | undefined) {
  return total !== undefined && total > shown
    ? `Showing ${shown} of ${total}`
    : undefined;
}

/** The newest attempt error; attempts are listed newest first. */
export function latestErrorSummary(
  attempts: { errorSummary?: string | null }[],
) {
  return (
    attempts.find((attempt) => attempt.errorSummary)?.errorSummary ?? undefined
  );
}

export async function getInboxBacklog(): Promise<InboxBacklog | null> {
  try {
    const { data, error } = await inboxClient.GET("/v1/backlog");
    return error || !data ? null : data;
  } catch {
    return null;
  }
}

async function listState(
  state: InboxItem["state"],
  limit = INBOX_SECTION_LIMIT,
): Promise<InboxItem[] | null> {
  try {
    const { data, error } = await inboxClient.GET("/v1/inbox-items", {
      params: { query: { state, limit } },
    });
    return error || !data ? null : data.items;
  } catch {
    return null;
  }
}

async function withErrorSummary(item: InboxItem): Promise<FailedInboxItem> {
  try {
    const { data } = await inboxClient.GET(
      "/v1/inbox-items/{itemId}/processing-attempts",
      { params: { path: { itemId: item.id } } },
    );
    const errorSummary = data ? latestErrorSummary(data.items) : undefined;
    return errorSummary ? { ...item, errorSummary } : item;
  } catch {
    return item;
  }
}

/** Everything /inbox shows besides Human Review. */
export async function getInboxOverview(): Promise<InboxOverview> {
  const [backlog, raw, processing, failed, processed] = await Promise.all([
    getInboxBacklog(),
    listState("raw"),
    listState("processing"),
    listState("failed"),
    listState("resolved"),
  ]);
  return {
    backlog,
    pending: raw && processing ? mergePending(raw, processing) : null,
    failed: failed
      ? await Promise.all(
          failed.map((item, index) =>
            index < FAILED_REASON_LIMIT ? withErrorSummary(item) : item,
          ),
        )
      : null,
    processed: processed ? recentlyProcessed(processed) : null,
  };
}
