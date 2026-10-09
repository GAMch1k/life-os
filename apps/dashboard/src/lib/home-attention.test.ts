import assert from "node:assert/strict";
import test from "node:test";

import { toHomeAttention, unavailableOnFailure } from "./home-attention";

const now = new Date("2026-10-09T15:00:00Z");
const task = (id: string, dueAt?: string) => ({
  id,
  title: `Task ${id}`,
  completed: false,
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-01T00:00:00Z",
  ...(dueAt ? { dueAt } : {}),
});
const list = <T>(items: T[], nextCursor?: string) => ({
  data: { items, nextCursor },
});

test("Home splits overdue from due-today Tasks and keeps previews capped", () => {
  const attention = toHomeAttention(
    {
      review: list([], "cursor"),
      urgent: list([
        task("late", "2026-10-08T09:00:00Z"),
        task("today", "2026-10-09T18:00:00Z"),
      ]),
      open: list([
        task("today", "2026-10-09T18:00:00Z"),
        ...["a", "b", "c", "d"].map((id) => task(id)),
      ]),
      events: list([]),
    },
    now,
  );
  assert.deepEqual(
    attention.urgent?.overdue.map((item) => item.id),
    ["late"],
  );
  assert.deepEqual(
    attention.urgent?.today.map((item) => item.id),
    ["today"],
  );
  assert.equal(attention.unscheduled?.items.length, 3);
  assert.equal(attention.unscheduled?.more, true);
  assert.equal(attention.humanReview?.more, true);
});

test("Home distinguishes an empty service from an unavailable one", () => {
  const empty = toHomeAttention(
    { review: list([]), urgent: list([]), open: list([]), events: list([]) },
    now,
  );
  assert.deepEqual(empty.nextEvent, { kind: "none" });
  assert.deepEqual(empty.humanReview, { items: [], more: false });

  const unavailable = toHomeAttention(
    { review: {}, urgent: {}, open: {}, events: {} },
    now,
  );
  assert.equal(unavailable.nextEvent, null);
  assert.equal(unavailable.humanReview, null);
  assert.equal(unavailable.urgent, null);
  assert.equal(unavailable.unscheduled, null);
});

test("Home treats an unreachable service as unavailable", async () => {
  const result = await unavailableOnFailure<never>(
    Promise.reject(new TypeError("fetch failed")),
  );
  assert.equal(
    toHomeAttention(
      { review: result, urgent: result, open: result, events: result },
      now,
    ).nextEvent,
    null,
  );
});
