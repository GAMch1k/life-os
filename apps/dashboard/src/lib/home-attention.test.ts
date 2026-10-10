import assert from "node:assert/strict";
import test from "node:test";

import {
  REVIEW_LIMIT,
  calendarDayHref,
  eventSourceName,
  eventStart,
  nextUpcomingEvent,
  ownerToday,
  toHomeAttention,
  unavailableOnFailure,
  type CalendarSource,
} from "./home-attention";
import type { CalendarEvent } from "./calendar-summary";
import type { InboxItem } from "./inbox-summary";

const now = new Date("2026-10-09T15:00:00Z");
const task = (id: string, dueAt?: string) => ({
  id,
  title: `Task ${id}`,
  completed: false,
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-01T00:00:00Z",
  ...(dueAt ? { dueAt } : {}),
});
/** Only the id matters to Home's review preview logic. */
const reviewItem = (id: string) => ({ id }) as unknown as InboxItem;
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

test("Home counts carry a + only when the service has more rows", () => {
  const due = "2026-10-09T18:00:00Z";
  const capped = toHomeAttention(
    {
      // The Inbox list does not page: a full page means there may be more.
      review: list(
        Array.from({ length: REVIEW_LIMIT }, (_, index) =>
          reviewItem(`review-${index}`),
        ),
      ),
      urgent: list([task("today", due)], "next-urgent"),
      open: list([task("a")], "next-undated"),
      events: list([]),
    },
    now,
  );
  assert.equal(capped.humanReview?.more, true);
  assert.equal(capped.urgent?.more, true);
  assert.equal(capped.unscheduled?.more, true);

  const complete = toHomeAttention(
    {
      review: list([reviewItem("review")]),
      urgent: list([task("today", due)]),
      open: list([task("a"), task("b")]),
      events: list([]),
    },
    now,
  );
  assert.equal(complete.humanReview?.more, false);
  assert.equal(complete.urgent?.more, false);
  assert.equal(complete.unscheduled?.more, false);
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

const calendarEvent = (
  id: string,
  fields: Partial<CalendarEvent> = {},
): CalendarEvent => ({
  id,
  title: `Event ${id}`,
  allDay: false,
  status: "active",
  source: { kind: "manual" },
  reminders: [],
  useDefaultReminders: true,
  defaultReminders: [],
  effectiveReminders: [],
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-01T00:00:00Z",
  ...fields,
});
const timedEvent = (id: string, startAt: string, minutes = 60) =>
  calendarEvent(id, {
    startAt,
    endAt: new Date(Date.parse(startAt) + minutes * 60_000).toISOString(),
  });
const empty = { review: list([]), urgent: list([]), open: list([]) };

test("the next event is the first active one that has not started yet", () => {
  // 17:00 in Berlin on Friday 9 October.
  const items = [
    // Today's all-day event and a meeting under way have already started.
    calendarEvent("today-all-day", {
      allDay: true,
      startDate: "2026-10-09",
      endDate: "2026-10-09",
    }),
    timedEvent("under-way", "2026-10-09T14:30:00Z"),
    // A cancelled event is not on the calendar any more.
    calendarEvent("cancelled", {
      ...timedEvent("cancelled", "2026-10-09T15:30:00Z"),
      status: "cancelled",
    }),
    // An occurrence of a recurring event comes next.
    calendarEvent("weekly", {
      ...timedEvent("weekly", "2026-10-09T16:00:00Z"),
      recurrence: { frequency: "weekly", interval: 1 },
      occurrence: { seriesId: "weekly", recurrenceId: "2026-10-09T16:00:00Z" },
    }),
    timedEvent("later", "2026-10-09T18:00:00Z"),
  ];
  const attention = toHomeAttention(
    { ...empty, events: list(items) },
    now,
    "Europe/Berlin",
  );
  assert.equal(attention.nextEvent?.kind, "event");
  if (attention.nextEvent?.kind !== "event") return;
  assert.equal(attention.nextEvent.event.id, "weekly");
  assert.equal(attention.nextEvent.href, "/calendar?date=2026-10-09");
  assert.equal(attention.nextEvent.sourceName, undefined);

  // Only events under way or cancelled: nothing is next.
  assert.deepEqual(
    toHomeAttention(
      { ...empty, events: list(items.slice(0, 3)) },
      now,
      "Europe/Berlin",
    ).nextEvent,
    { kind: "none" },
  );
});

test("an all-day event starts at the owner's midnight", () => {
  const tomorrow = calendarEvent("pickup", {
    allDay: true,
    startDate: "2026-10-10",
    endDate: "2026-10-10",
  });
  // 23:30 UTC on Friday is already Saturday 01:30 in Berlin: under way.
  const late = new Date("2026-10-09T23:30:00Z");
  assert.equal(nextUpcomingEvent([tomorrow], late, "Europe/Berlin"), undefined);
  assert.equal(nextUpcomingEvent([tomorrow], late, "UTC")?.id, "pickup");
  assert.equal(eventStart(tomorrow, "UTC"), Date.parse("2026-10-10T00:00:00Z"));
  assert.equal(calendarDayHref(tomorrow, "UTC"), "/calendar?date=2026-10-10");
});

test("an imported next event carries its source's name", () => {
  const imported = calendarEvent("restmuell", {
    allDay: true,
    startDate: "2026-10-12",
    endDate: "2026-10-12",
    source: { kind: "ics", sourceId: "source-zaw", externalUid: "uid-1" },
  });
  const zaw = { id: "source-zaw", name: "ZAW" } as CalendarSource;
  const attention = toHomeAttention(
    { ...empty, events: list([imported]), sources: list([zaw]) },
    now,
    "Europe/Berlin",
  );
  assert.equal(
    attention.nextEvent?.kind === "event" && attention.nextEvent.sourceName,
    "ZAW",
  );
  // Without the source list (Calendar sources unreadable), the kind names it.
  assert.equal(eventSourceName(imported, undefined), "ICS feed");
  // Times link to the owner's day: 22:30 UTC is the next day in Berlin.
  const late = timedEvent("late", "2026-10-12T22:30:00Z");
  assert.equal(
    calendarDayHref(late, "Europe/Berlin"),
    "/calendar?date=2026-10-13",
  );
});
