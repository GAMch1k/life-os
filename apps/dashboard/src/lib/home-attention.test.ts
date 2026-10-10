import assert from "node:assert/strict";
import test from "node:test";

import {
  ownerToday,
  toHomeAttention,
  unavailableOnFailure,
} from "./home-attention";

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

test("Home's today is the owner's day, not the UTC day", () => {
  // 23:30 UTC on Friday is 01:30 on Saturday in Berlin (summer time).
  const nearMidnight = new Date("2026-10-09T23:30:00Z");
  const bounds = ownerToday(nearMidnight, "Europe/Berlin");
  assert.equal(bounds.start.toISOString(), "2026-10-09T22:00:00.000Z");
  assert.equal(bounds.end.toISOString(), "2026-10-10T21:59:59.999Z");

  const urgent = list([
    // Friday 23:00 in Berlin: already yesterday for the owner.
    task("friday-night", "2026-10-09T21:00:00Z"),
    // Saturday 10:00 in Berlin.
    task("saturday", "2026-10-10T08:00:00Z"),
  ]);
  const results = {
    review: list([]),
    urgent,
    open: list([]),
    events: list([]),
  };
  const berlin = toHomeAttention(results, nearMidnight, "Europe/Berlin");
  assert.deepEqual(
    berlin.urgent?.overdue.map((item) => item.id),
    ["friday-night"],
  );
  assert.deepEqual(
    berlin.urgent?.today.map((item) => item.id),
    ["saturday"],
  );
  // The same instant in UTC is still Friday, so nothing is overdue yet.
  const utc = toHomeAttention(results, nearMidnight, "UTC");
  assert.deepEqual(utc.urgent?.overdue, []);
});
