import assert from "node:assert/strict";
import test from "node:test";

import {
  EVENT_TYPES,
  actorLabel,
  bankSyncChanges,
  eventLabel,
  sourceLabel,
  subjectLabel,
} from "./activity-log";

test("bank events read with their bank, syncs with what they changed", () => {
  assert.equal(
    eventLabel({
      type: "banking.connected",
      data: { institution: "Deutsche Bank", accounts: 1 },
    }),
    "Bank connected: Deutsche Bank",
  );
  assert.equal(
    eventLabel({
      type: "banking.sync.completed",
      data: { institution: "Deutsche Bank", created: 3, updated: 1 },
    }),
    "Bank synced: Deutsche Bank — 3 new, 1 updated",
  );
  assert.equal(
    eventLabel({ type: "banking.consent.expired", data: {} }),
    "Bank access expired",
  );
  assert.equal(
    bankSyncChanges({ created: 0, removedPending: 2 }),
    "2 pending removed",
  );
  assert.equal(bankSyncChanges({}), "no changes");
  for (const type of [
    "banking.connected",
    "banking.disconnected",
    "banking.sync.completed",
    "banking.sync.failed",
    "banking.consent.expiring",
    "banking.consent.expired",
  ]) {
    assert.ok(EVENT_TYPES.includes(type), type);
  }
  assert.equal(sourceLabel("banking"), "Bank connection");
  assert.equal(subjectLabel("bank_connection"), "Bank connection");
  assert.equal(actorLabel({ kind: "system", name: "banking" }), "Bank sync");
});
