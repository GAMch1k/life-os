import assert from "node:assert/strict";
import test from "node:test";

import {
  formatInboxTime,
  type InboxItem,
  inboxItemStatus,
  inboxStatusLabel,
  isPendingState,
  latestErrorSummary,
  leftProcessing,
  mergePending,
  recentlyProcessed,
  shownOfTotal,
} from "./inbox-summary";

const now = new Date("2026-10-09T15:00:00Z");
const item = (
  id: string,
  state: InboxItem["state"],
  extra: Partial<InboxItem> = {},
): InboxItem => ({
  id,
  content: `Capture ${id}`,
  correlationId: id,
  source: "manual",
  state,
  processingMetadata: {},
  createdAt: "2026-10-09T10:00:00Z",
  updatedAt: "2026-10-09T10:00:00Z",
  ...extra,
});

test("each Inbox state maps to exactly one status; review is not pending", () => {
  assert.deepEqual(inboxItemStatus(item("a", "raw"), now), {
    kind: "received",
  });
  assert.deepEqual(inboxItemStatus(item("b", "processing"), now), {
    kind: "processing",
  });
  assert.deepEqual(inboxItemStatus(item("c", "human_review"), now), {
    kind: "review",
  });
  assert.deepEqual(inboxItemStatus(item("d", "failed"), now), {
    kind: "failed",
  });
  assert.deepEqual(inboxItemStatus(item("e", "resolved"), now), {
    kind: "processed",
  });
  assert.deepEqual(inboxItemStatus(item("f", "discarded"), now), {
    kind: "discarded",
  });
  assert.equal(isPendingState("raw"), true);
  assert.equal(isPendingState("processing"), true);
  for (const state of [
    "human_review",
    "failed",
    "resolved",
    "discarded",
  ] as const) {
    assert.equal(isPendingState(state), false, state);
  }
});

test("a raw capture with a future retry shows when and why it retries", () => {
  const scheduled = item("a", "raw", {
    nextRetryAt: "2026-10-09T16:30:00Z",
    retryReason: "usage_limited",
  });
  const status = inboxItemStatus(scheduled, now);
  assert.deepEqual(status, {
    kind: "retry",
    at: "2026-10-09T16:30:00Z",
    reason: "usage_limited",
  });
  assert.equal(inboxStatusLabel(status, "Europe/Berlin", now), "Retry 18:30");
  // Once the retry time has passed it is simply being retried.
  assert.deepEqual(
    inboxItemStatus({ ...scheduled, nextRetryAt: "2026-10-09T14:00:00Z" }, now),
    { kind: "retrying" },
  );
  assert.deepEqual(
    inboxItemStatus(item("b", "raw", { retryReason: "backoff" }), now),
    { kind: "retrying" },
  );
});

test("a followed capture leaves processing once it settles, not before", () => {
  // Settling into any final or review state makes the page's data stale.
  for (const state of [
    "human_review",
    "failed",
    "resolved",
    "discarded",
  ] as const) {
    assert.equal(leftProcessing(item("a", "raw"), item("a", state)), true);
    assert.equal(
      leftProcessing(item("a", "processing"), item("a", state)),
      true,
    );
  }
  // Still pending, not read this time, or already settled before.
  assert.equal(
    leftProcessing(item("a", "raw"), item("a", "processing")),
    false,
  );
  assert.equal(leftProcessing(item("a", "processing"), undefined), false);
  assert.equal(
    leftProcessing(item("a", "human_review"), item("a", "discarded")),
    false,
  );
});

test("Human Review is labelled as needing review, never as pending", () => {
  assert.equal(
    inboxStatusLabel(
      inboxItemStatus(item("a", "human_review"), now),
      "UTC",
      now,
    ),
    "Needs review",
  );
});

test("times use the owner's timezone and add the date only when needed", () => {
  assert.equal(formatInboxTime("2026-10-09T08:05:00Z", "UTC", now), "08:05");
  assert.equal(
    formatInboxTime("2026-10-09T08:05:00Z", "Europe/Berlin", now),
    "10:05",
  );
  // 23:30 UTC on the 8th is already the 9th in Berlin, but not in New York.
  assert.equal(
    formatInboxTime("2026-10-08T23:30:00Z", "Europe/Berlin", now),
    "01:30",
  );
  assert.equal(
    formatInboxTime("2026-10-08T23:30:00Z", "America/New_York", now),
    "8 Oct, 19:30",
  );
  assert.equal(
    formatInboxTime("2025-12-31T09:00:00Z", "UTC", now),
    "31 Dec 2025, 09:00",
  );
});

test("pending merges raw and processing captures newest first, capped", () => {
  const merged = mergePending(
    [
      item("raw-old", "raw", { createdAt: "2026-10-09T08:00:00Z" }),
      item("raw-new", "raw", { createdAt: "2026-10-09T12:00:00Z" }),
    ],
    [item("processing", "processing", { createdAt: "2026-10-09T10:00:00Z" })],
    2,
  );
  assert.deepEqual(
    merged.map((entry) => entry.id),
    ["raw-new", "processing"],
  );
});

test("recently processed is ordered by when each capture was resolved", () => {
  const processed = recentlyProcessed(
    [
      item("new-capture", "resolved", {
        createdAt: "2026-10-09T12:00:00Z",
        resolvedAt: "2026-10-09T12:01:00Z",
      }),
      item("old-capture", "resolved", {
        createdAt: "2026-10-01T12:00:00Z",
        resolvedAt: "2026-10-09T14:00:00Z",
      }),
      item("no-resolved-at", "resolved", { updatedAt: "2026-10-09T13:00:00Z" }),
    ],
    2,
  );
  assert.deepEqual(
    processed.map((entry) => entry.id),
    ["old-capture", "no-resolved-at"],
  );
});

test("a capped section says how many really exist instead of hiding it", () => {
  assert.equal(shownOfTotal(50, 120), "Showing 50 of 120");
  assert.equal(shownOfTotal(3, 3), undefined);
  assert.equal(shownOfTotal(3, undefined), undefined);
});

test("a failed capture shows its newest attempt error", () => {
  assert.equal(
    latestErrorSummary([
      { errorSummary: null },
      { errorSummary: "Codex timed out" },
      { errorSummary: "older error" },
    ]),
    "Codex timed out",
  );
  assert.equal(latestErrorSummary([{ errorSummary: null }]), undefined);
});
