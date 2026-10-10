import assert from "node:assert/strict";
import test from "node:test";

import type { Task } from "./tasks-summary";
import {
  dayLabel,
  daysWithTasks,
  dueDay,
  dueLabel,
  fromDateTimeInput,
  groupTasks,
  isDateOnly,
  toDateTimeInput,
  weekOf,
} from "./tasks-workspace";

const BERLIN = "Europe/Berlin";
const task = (id: string, dueAt?: string, completed = false): Task => ({
  id,
  title: `Task ${id}`,
  completed,
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-01T00:00:00Z",
  ...(dueAt ? { dueAt } : {}),
});
const ids = (tasks: Task[]) => tasks.map((entry) => entry.id);

test("due days are the owner's calendar day, not UTC or the browser's", () => {
  assert.equal(dueDay("2026-10-09T23:41:00Z", BERLIN), "2026-10-10");
  assert.equal(dueDay("2026-10-09T23:41:00Z", "UTC"), "2026-10-09");
  assert.equal(dueDay(null, BERLIN), null);
  assert.equal(dueDay(undefined, BERLIN), null);
});

test("groups every Task into exactly one section by the owner's day", () => {
  const tasks = [
    task("overdue", "2026-10-08T10:00:00Z"),
    task("today", "2026-10-10T10:00:00Z"),
    // 00:30 on 11 October in Berlin, still 10 October in UTC.
    task("late", "2026-10-10T22:30:00Z"),
    task("upcoming", "2026-10-14T10:00:00Z"),
    task("undated"),
    task("done", "2026-10-10T10:00:00Z", true),
    task("done-undated", undefined, true),
  ];
  const berlin = groupTasks(tasks, {
    today: "2026-10-10",
    selected: "2026-10-10",
    timeZone: BERLIN,
  });
  assert.deepEqual(ids(berlin.overdue), ["overdue"]);
  assert.deepEqual(ids(berlin.today), ["today"]);
  assert.deepEqual(ids(berlin.upcoming), ["late", "upcoming"]);
  assert.deepEqual(ids(berlin.unscheduled), ["undated"]);
  assert.deepEqual(ids(berlin.completed), ["done", "done-undated"]);
  assert.deepEqual(berlin.selected, []);

  const utc = groupTasks(tasks, {
    today: "2026-10-10",
    selected: "2026-10-10",
    timeZone: "UTC",
  });
  assert.deepEqual(ids(utc.today), ["today", "late"]);
});

test("a focused day other than today takes its Tasks out of the other sections", () => {
  const tasks = [
    task("past", "2026-10-08T10:00:00Z"),
    task("focused", "2026-10-14T10:00:00Z"),
    task("later", "2026-10-15T10:00:00Z"),
    task("undated"),
  ];
  const future = groupTasks(tasks, {
    today: "2026-10-10",
    selected: "2026-10-14",
    timeZone: BERLIN,
  });
  assert.deepEqual(ids(future.selected), ["focused"]);
  assert.deepEqual(ids(future.upcoming), ["later"]);
  assert.deepEqual(ids(future.overdue), ["past"]);
  assert.deepEqual(ids(future.unscheduled), ["undated"]);

  const past = groupTasks(tasks, {
    today: "2026-10-10",
    selected: "2026-10-08",
    timeZone: BERLIN,
  });
  assert.deepEqual(ids(past.selected), ["past"]);
  assert.deepEqual(past.overdue, []);
  assert.deepEqual(ids(past.unscheduled), ["undated"]);
});

test("only open dated Tasks mark days in the date picker", () => {
  const days = daysWithTasks(
    [
      task("a", "2026-10-10T22:30:00Z"),
      task("b"),
      task("c", "2026-10-12T10:00:00Z", true),
    ],
    BERLIN,
  );
  assert.deepEqual([...days], ["2026-10-11"]);
});

test("weeks start on Monday", () => {
  const week = weekOf("2026-10-10"); // a Saturday
  assert.equal(week[0], "2026-10-05");
  assert.equal(week[6], "2026-10-11");
  assert.equal(weekOf("2026-10-05")[0], "2026-10-05");
  assert.equal(weekOf("2026-10-11")[0], "2026-10-05"); // a Sunday
  assert.deepEqual(weekOf("2026-11-01").slice(0, 2), [
    "2026-10-26",
    "2026-10-27",
  ]);
});

test("labels are fixed English text in the owner's timezone", () => {
  const today = "2026-10-10";
  assert.equal(dayLabel("2026-10-12", today), "Mon 12 Oct");
  assert.equal(dayLabel("2026-09-14", today), "Mon 14 Sept");
  assert.equal(dayLabel("2027-01-04", today), "Mon 4 Jan 2027");
  assert.equal(dueLabel(null, today, BERLIN), "No date");
  assert.equal(
    dueLabel("2026-10-10T12:05:00Z", today, BERLIN),
    "Today · 14:05",
  );
  assert.equal(dueLabel("2026-10-10T12:05:00Z", today, "UTC"), "Today · 12:05");
  assert.equal(
    dueLabel("2026-10-10T22:30:00Z", today, BERLIN),
    "Tomorrow · 00:30",
  );
  assert.equal(
    dueLabel("2026-10-09T08:00:00Z", today, BERLIN),
    "Yesterday · 10:00",
  );
  assert.equal(
    dueLabel("2026-10-14T16:00:00Z", today, BERLIN),
    "Wed 14 Oct · 18:00",
  );
});

test("date-only due dates keep their day and show no time", () => {
  // The owner's local midnight.
  assert.equal(isDateOnly("2026-10-19T22:00:00Z", BERLIN), true);
  assert.equal(
    dueLabel("2026-10-19T22:00:00Z", "2026-10-10", BERLIN),
    "Tue 20 Oct",
  );
  // A bare date sent as UTC midnight is the same day in Berlin.
  assert.equal(isDateOnly("2026-10-20T00:00:00Z", BERLIN), true);
  assert.equal(dueDay("2026-10-20T00:00:00Z", BERLIN), "2026-10-20");
  assert.equal(
    dueLabel("2026-10-20T00:00:00Z", "2026-10-10", BERLIN),
    "Tue 20 Oct",
  );
  // West of UTC that instant is the previous evening, so it keeps its time.
  assert.equal(isDateOnly("2026-10-20T00:00:00Z", "America/New_York"), false);
  assert.equal(isDateOnly("2026-10-20T12:00:00Z", BERLIN), false);
});

test("the edit form reads and writes the owner's wall time", () => {
  assert.equal(
    toDateTimeInput("2026-10-21T07:30:00Z", BERLIN),
    "2026-10-21T09:30",
  );
  assert.equal(toDateTimeInput(null, BERLIN), "");
  assert.equal(
    fromDateTimeInput("2026-10-21T09:30", BERLIN),
    "2026-10-21T07:30:00.000Z",
  );
  assert.equal(
    fromDateTimeInput("2026-10-21T09:30", "UTC"),
    "2026-10-21T09:30:00.000Z",
  );
  assert.equal(
    fromDateTimeInput("2026-10-21T09:30", "America/New_York"),
    "2026-10-21T13:30:00.000Z",
  );
  // Berlin leaves summer time at 03:00 on 25 October.
  assert.equal(
    fromDateTimeInput("2026-10-25T10:00", BERLIN),
    "2026-10-25T09:00:00.000Z",
  );
  assert.equal(
    fromDateTimeInput("2026-10-25T01:00", BERLIN),
    "2026-10-24T23:00:00.000Z",
  );
  assert.equal(
    fromDateTimeInput("2026-10-25T23:30", BERLIN),
    "2026-10-25T22:30:00.000Z",
  );
  // It enters summer time at 02:00 on 28 March 2027.
  assert.equal(
    fromDateTimeInput("2027-03-28T10:00", BERLIN),
    "2027-03-28T08:00:00.000Z",
  );
  assert.equal(
    toDateTimeInput(fromDateTimeInput("2027-03-28T23:30", BERLIN), BERLIN),
    "2027-03-28T23:30",
  );
});
