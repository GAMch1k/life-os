import assert from "node:assert/strict";
import { test } from "node:test";

import { currentMonth, monthLabel } from "./finance-summary";

test("currentMonth uses the owner's timezone, not UTC", () => {
  // 23:30 UTC on 31 October is already November in Berlin.
  const instant = new Date("2026-10-31T23:30:00Z");
  assert.equal(currentMonth(instant, "UTC"), "2026-10");
  assert.equal(currentMonth(instant, "Europe/Berlin"), "2026-11");
});

test("monthLabel names the month without shifting it", () => {
  assert.equal(monthLabel("2026-10"), "October 2026");
  assert.equal(monthLabel("2027-01"), "January 2027");
});
