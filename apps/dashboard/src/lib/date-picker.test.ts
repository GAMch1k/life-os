import assert from "node:assert/strict";
import { test } from "node:test";

import {
  dateToDay,
  dayToDate,
  PICKER_YEARS,
  pickerLabel,
  pickerRange,
} from "./date-picker";

/** Runs `check` with the process (the "browser") in another timezone. */
function inZone(timeZone: string, check: () => void) {
  const original = process.env.TZ;
  process.env.TZ = timeZone;
  try {
    check();
  } finally {
    if (original === undefined) delete process.env.TZ;
    else process.env.TZ = original;
  }
}

test("a picked day is the day stored, whatever the browser's timezone", () => {
  for (const zone of [
    "UTC",
    "Europe/Berlin",
    "America/Los_Angeles",
    "Pacific/Kiritimati",
    "Pacific/Pago_Pago",
  ]) {
    inZone(zone, () => {
      for (const day of [
        "2026-10-21",
        "2026-10-25",
        "2027-03-28",
        "2028-02-29",
      ]) {
        const date = dayToDate(day);
        assert.ok(date, `${day} in ${zone}`);
        assert.equal(date.getHours(), 0, `${day} is local midnight in ${zone}`);
        assert.equal(dateToDay(date), day, `${day} round trip in ${zone}`);
      }
    });
  }
});

test("only real days become dates", () => {
  assert.equal(dayToDate(""), undefined);
  assert.equal(dayToDate(undefined), undefined);
  assert.equal(dayToDate("2026-02-30"), undefined);
  assert.equal(dayToDate("2026-13-01"), undefined);
  assert.equal(dayToDate("21.10.2026"), undefined);
  assert.equal(dayToDate("2026-10-21T09:30"), undefined);
});

test("the button shows the day in the browser's locale", () => {
  // ICU versions differ in punctuation, so only the parts are pinned.
  assert.match(pickerLabel("2026-10-21", "en-GB"), /^Wed,? 21 Oct 2026$/);
  assert.match(pickerLabel("2026-10-21", "en-US"), /^Wed,? Oct 21, 2026$/);
  assert.match(
    pickerLabel("2026-10-21", "de-DE"),
    /^Mi\.?,? 21\. Okt\.? 2026$/,
  );
  // The label never slips a day, even where midnight UTC is the day before.
  inZone("America/Los_Angeles", () =>
    assert.match(pickerLabel("2026-10-21", "en-GB"), /^Wed,? 21 Oct 2026$/),
  );
  assert.equal(pickerLabel("not a day", "en-GB"), "not a day");
});

test("the calendar reaches years either side of today, the value, and stops at min and max", () => {
  assert.deepEqual(pickerRange({ today: "2026-10-10" }), {
    start: `${2026 - PICKER_YEARS}-01-01`,
    end: `${2026 + PICKER_YEARS}-12-31`,
  });
  assert.deepEqual(pickerRange({ today: "2026-10-10", value: "1900-05-01" }), {
    start: `${1900 - PICKER_YEARS}-01-01`,
    end: `${2026 + PICKER_YEARS}-12-31`,
  });
  assert.deepEqual(
    pickerRange({ today: "2026-10-10", min: "2026-01-01", max: "2026-10-10" }),
    { start: "2026-01-01", end: "2026-10-10" },
  );
  // An empty or broken bound is no bound.
  assert.deepEqual(pickerRange({ today: "2026-10-10", min: "", max: "x" }), {
    start: `${2026 - PICKER_YEARS}-01-01`,
    end: `${2026 + PICKER_YEARS}-12-31`,
  });
});
