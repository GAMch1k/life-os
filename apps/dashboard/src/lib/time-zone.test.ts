import assert from "node:assert/strict";
import { test } from "node:test";

import { addDays, dateTimeFormat, dayKey, startOfDay } from "./time-zone";

test("dayKey uses the configured timezone, not UTC", () => {
  // 23:41 UTC on Friday is already Saturday in Berlin (summer time).
  assert.equal(dayKey("2026-10-09T23:41:00Z", "UTC"), "2026-10-09");
  assert.equal(dayKey("2026-10-09T23:41:00Z", "Europe/Berlin"), "2026-10-10");
});

test("startOfDay is local midnight, across DST changes", () => {
  assert.equal(
    startOfDay("2026-10-10", "Europe/Berlin").toISOString(),
    "2026-10-09T22:00:00.000Z",
  );
  // Berlin leaves summer time on 2026-10-25.
  assert.equal(
    startOfDay("2026-10-25", "Europe/Berlin").toISOString(),
    "2026-10-24T22:00:00.000Z",
  );
  assert.equal(
    startOfDay("2026-10-26", "Europe/Berlin").toISOString(),
    "2026-10-25T23:00:00.000Z",
  );
  assert.equal(
    startOfDay("2026-10-10", "UTC").toISOString(),
    "2026-10-10T00:00:00.000Z",
  );
});

test("addDays crosses month boundaries", () => {
  assert.equal(addDays("2026-10-31", 1), "2026-11-01");
  assert.equal(addDays("2026-03-01", -1), "2026-02-28");
});

test("dateTimeFormat renders in the timezone with the fixed locale", () => {
  const options = { hour: "2-digit", minute: "2-digit" } as const;
  const at = new Date("2026-10-09T23:41:00Z");
  assert.equal(dateTimeFormat("Europe/Berlin", options).format(at), "01:41");
  assert.equal(dateTimeFormat("UTC", options).format(at), "23:41");
  assert.equal(
    dateTimeFormat("Europe/Berlin", options),
    dateTimeFormat("Europe/Berlin", { ...options }),
  );
});
