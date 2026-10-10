import assert from "node:assert/strict";
import test from "node:test";

import {
  customDays,
  customFrom,
  NO_REPEAT,
  readRepeat,
  recurrenceLabel,
  type RepeatDraft,
  repeatDraftFrom,
  repeatOptions,
  repeatsOnNote,
  sameRepeat,
  shiftWeekdays,
  unitLabel,
  untilInstant,
  weekdayOf,
} from "./calendar-recurrence";

const BERLIN = "Europe/Berlin";
// 17 September 2026 is a Thursday.
const SEP_17 = "2026-09-17";

const custom = (fields: Partial<RepeatDraft>): RepeatDraft => ({
  ...NO_REPEAT,
  preset: "custom",
  ...fields,
});

test("days of the week come from the date", () => {
  assert.equal(weekdayOf(SEP_17), "TH");
  assert.equal(weekdayOf("2026-10-12"), "MO");
  assert.equal(weekdayOf("2026-10-18"), "SU");
  assert.equal(weekdayOf(""), undefined);
  assert.deepEqual(shiftWeekdays(["MO", "WE"], 1), ["TU", "TH"]);
  assert.deepEqual(shiftWeekdays(["MO", "SU"], -1), ["SA", "SU"]);
  assert.deepEqual(shiftWeekdays(["FR"], 3), ["MO"]);
});

test("the Repeat menu follows the start date", () => {
  assert.deepEqual(
    repeatOptions(SEP_17).map((option) => option.label),
    [
      "Does not repeat",
      "Daily",
      "Weekly on Thursday",
      "Every weekday (Monday to Friday)",
      "Monthly on day 17",
      "Annually on September 17",
      "Custom…",
    ],
  );
  // Every weekday needs a start on one.
  const saturday = repeatOptions("2026-10-17");
  assert.equal(saturday[2].label, "Weekly on Saturday");
  assert.equal(
    saturday.find((option) => option.value === "weekdays")?.disabled,
    true,
  );
  assert.equal(repeatOptions("")[5].label, "Annually");
});

test("menu choices are rules that repeat forever", () => {
  const read = (preset: RepeatDraft["preset"]) =>
    readRepeat({ ...NO_REPEAT, preset }, SEP_17, BERLIN);
  assert.deepEqual(read("none"), { recurrence: null });
  assert.deepEqual(read("daily"), {
    recurrence: { frequency: "daily", interval: 1 },
  });
  assert.deepEqual(read("weekly"), {
    recurrence: { frequency: "weekly", interval: 1 },
  });
  assert.deepEqual(read("weekdays"), {
    recurrence: {
      frequency: "weekly",
      interval: 1,
      byWeekday: ["MO", "TU", "WE", "TH", "FR"],
    },
  });
  assert.deepEqual(read("monthly"), {
    recurrence: { frequency: "monthly", interval: 1 },
  });
  assert.deepEqual(read("yearly"), {
    recurrence: { frequency: "yearly", interval: 1 },
  });
});

test("a custom rule always repeats on the start's weekday and ends as chosen", () => {
  assert.deepEqual(
    readRepeat(
      custom({ frequency: "weekly", interval: "2", days: ["MO"] }),
      SEP_17,
      BERLIN,
    ),
    {
      recurrence: { frequency: "weekly", interval: 2, byWeekday: ["MO", "TH"] },
    },
  );
  // Only the start's weekday: plain weekly, no byWeekday.
  assert.deepEqual(
    readRepeat(custom({ frequency: "weekly", days: [] }), SEP_17, BERLIN),
    { recurrence: { frequency: "weekly", interval: 1 } },
  );
  assert.deepEqual(customDays(custom({ days: ["SU", "MO"] }), SEP_17), [
    "MO",
    "TH",
    "SU",
  ]);
  assert.deepEqual(
    readRepeat(
      custom({ frequency: "monthly", ends: "after", count: "10" }),
      SEP_17,
      BERLIN,
    ),
    { recurrence: { frequency: "monthly", interval: 1, count: 10 } },
  );
  // Until the end of the chosen day where it repeats (CET in December).
  assert.deepEqual(
    readRepeat(
      custom({ frequency: "daily", ends: "on", until: "2026-12-31" }),
      SEP_17,
      BERLIN,
    ),
    {
      recurrence: {
        frequency: "daily",
        interval: 1,
        until: "2026-12-31T22:59:59Z",
      },
    },
  );
  assert.equal(untilInstant("2026-10-24", BERLIN), "2026-10-24T21:59:59Z");
  for (const draft of [
    custom({ interval: "0" }),
    custom({ interval: "1.5" }),
    custom({ ends: "on", until: "" }),
    custom({ ends: "after", count: "0" }),
    custom({ ends: "after", count: "1001" }),
  ]) {
    assert.ok("error" in readRepeat(draft, SEP_17, BERLIN));
  }
});

test("a stored rule fills the control: a menu choice when it is one", () => {
  const from = (recurrence: Parameters<typeof repeatDraftFrom>[0]) =>
    repeatDraftFrom(recurrence, SEP_17, BERLIN);
  assert.equal(from(null).preset, "none");
  assert.equal(from({ frequency: "daily", interval: 1 }).preset, "daily");
  assert.equal(from({ frequency: "weekly", interval: 1 }).preset, "weekly");
  assert.equal(
    from({ frequency: "weekly", interval: 1, byWeekday: ["TH"] }).preset,
    "weekly",
  );
  assert.equal(
    from({
      frequency: "weekly",
      interval: 1,
      byWeekday: ["MO", "TU", "WE", "TH", "FR"],
    }).preset,
    "weekdays",
  );
  assert.equal(from({ frequency: "yearly", interval: 1 }).preset, "yearly");
  assert.deepEqual(
    from({
      frequency: "weekly",
      interval: 2,
      byWeekday: ["TH", "MO"],
      until: "2026-12-31T22:59:59Z",
    }),
    {
      preset: "custom",
      frequency: "weekly",
      interval: "2",
      days: ["MO"],
      ends: "on",
      until: "2026-12-31",
      count: "10",
    },
  );
  assert.deepEqual(from({ frequency: "monthly", interval: 1, count: 5 }), {
    preset: "custom",
    frequency: "monthly",
    interval: "1",
    days: [],
    ends: "after",
    until: "",
    count: "5",
  });
  // Round trip: what the control reads back is the stored rule.
  const stored = {
    frequency: "weekly" as const,
    interval: 3,
    byWeekday: ["MO" as const, "TH" as const],
    count: 12,
  };
  assert.deepEqual(readRepeat(from(stored), SEP_17, BERLIN), {
    recurrence: stored,
  });
});

test("Custom starts from the choice made so far", () => {
  // Weekly on the start's day, which needs no choosing.
  const weekly = customFrom(NO_REPEAT);
  assert.deepEqual(weekly, {
    ...NO_REPEAT,
    preset: "custom",
    frequency: "weekly",
    days: [],
  });
  assert.deepEqual(customDays(weekly, SEP_17), ["TH"]);
  assert.deepEqual(customDays(weekly, "2026-10-12"), ["MO"]);
  assert.equal(
    customFrom({ ...NO_REPEAT, preset: "yearly" }).frequency,
    "yearly",
  );
  assert.deepEqual(customFrom({ ...NO_REPEAT, preset: "weekdays" }).days, [
    "MO",
    "TU",
    "WE",
    "TH",
    "FR",
  ]);
  assert.equal(unitLabel("weekly", "1"), "week");
  assert.equal(unitLabel("monthly", "3"), "months");
  assert.equal(
    repeatsOnNote("monthly", "2026-10-31"),
    "On day 31 of the month; months without one are skipped.",
  );
  assert.equal(repeatsOnNote("yearly", SEP_17), "On September 17.");
  assert.equal(
    repeatsOnNote("yearly", "2028-02-29"),
    "On February 29, in leap years.",
  );
});

test("a changed Repeat is told apart from an unchanged one", () => {
  const weekly = { ...NO_REPEAT, preset: "weekly" as const };
  assert.ok(sameRepeat(weekly, { ...weekly, count: "3" }));
  assert.ok(!sameRepeat(weekly, NO_REPEAT));
  const days = custom({ frequency: "weekly", days: ["WE", "MO"] });
  assert.ok(
    sameRepeat(days, custom({ frequency: "weekly", days: ["MO", "WE"] })),
  );
  assert.ok(!sameRepeat(days, custom({ frequency: "weekly", days: ["MO"] })));
  assert.ok(!sameRepeat(days, { ...days, ends: "after" }));
  // What an unused end holds doesn't matter.
  assert.ok(sameRepeat(days, { ...days, until: "2027-01-01" }));
});

test("rules read as plain words", () => {
  const label = (
    recurrence: Parameters<typeof recurrenceLabel>[0],
    start = "2026-10-12",
  ) => recurrenceLabel(recurrence, start, BERLIN);
  assert.equal(label({ frequency: "daily", interval: 1 }), "Daily");
  assert.equal(label({ frequency: "daily", interval: 3 }), "Every 3 days");
  assert.equal(label({ frequency: "weekly", interval: 1 }), "Weekly on Monday");
  assert.equal(
    label({ frequency: "weekly", interval: 1, byWeekday: ["MO", "WE"] }),
    "Weekly on Monday and Wednesday",
  );
  assert.equal(
    label({ frequency: "weekly", interval: 1, byWeekday: ["MO", "WE", "FR"] }),
    "Weekly on Monday, Wednesday, and Friday",
  );
  assert.equal(
    label({
      frequency: "weekly",
      interval: 1,
      byWeekday: ["MO", "TU", "WE", "TH", "FR"],
    }),
    "Every weekday",
  );
  assert.equal(
    label(
      {
        frequency: "weekly",
        interval: 2,
        until: "2026-12-31T22:59:59Z",
      },
      "2026-10-13",
    ),
    "Every 2 weeks on Tuesday, until 31 Dec 2026",
  );
  assert.equal(
    label({ frequency: "monthly", interval: 1, count: 10 }, "2026-10-15"),
    "Monthly on day 15, 10 times",
  );
  assert.equal(
    label({ frequency: "monthly", interval: 2, count: 1 }, "2026-10-15"),
    "Every 2 months on day 15, once",
  );
  assert.equal(
    label({ frequency: "yearly", interval: 1 }, SEP_17),
    "Annually on September 17",
  );
  assert.equal(
    label({ frequency: "yearly", interval: 2 }, SEP_17),
    "Every 2 years on September 17",
  );
});
