import assert from "node:assert/strict";
import test from "node:test";

import { DEVICE_PAIRING_AVAILABLE } from "./constants";
import { showsLegacyReminder } from "./legacy-reminder";

const on = { legacyAccess: true, accountExists: true };

test("devices can be paired (P21-06): the reminder is on", () => {
  assert.equal(DEVICE_PAIRING_AVAILABLE, true);
  assert.equal(showsLegacyReminder(on, false), true);
  // The switch still hides it, as before P21-06.
  assert.equal(showsLegacyReminder(on, false, false), false);
});

test("once devices can be paired, the reminder shows daily while legacy access is on", () => {
  assert.equal(showsLegacyReminder(on, false, true), true);
  assert.equal(showsLegacyReminder(on, true, true), false);
  assert.equal(
    showsLegacyReminder(
      { legacyAccess: false, accountExists: true },
      false,
      true,
    ),
    false,
  );
  assert.equal(
    showsLegacyReminder(
      { legacyAccess: true, accountExists: false },
      false,
      true,
    ),
    false,
  );
  assert.equal(showsLegacyReminder(undefined, false, true), false);
});
