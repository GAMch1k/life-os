import assert from "node:assert/strict";
import test from "node:test";

import {
  EVENT_TYPES,
  actorLabel,
  eventLabel,
  subjectLabel,
} from "./activity-log";

test("the AI's categories read with how many it gave and left", () => {
  assert.equal(
    eventLabel({
      type: "finance.ai_categories.applied",
      data: { categorized: 31, left: 9, skipped: 0 },
    }),
    "AI categorized transactions: 31 (9 left without a category)",
  );
  assert.equal(
    eventLabel({
      type: "finance.ai_categories.applied",
      data: { categorized: 1, left: 0 },
    }),
    "AI categorized transactions: 1",
  );
  assert.equal(
    eventLabel({ type: "finance.ai_categories.applied", data: {} }),
    "AI categorized transactions",
  );
  assert.ok(EVENT_TYPES.includes("finance.ai_categories.applied"));
  assert.equal(subjectLabel("finance_ai_categories"), "AI categories");
  assert.equal(
    actorLabel({ kind: "agent", name: "codex-finance-categories" }),
    "AI categories (Codex)",
  );
});
