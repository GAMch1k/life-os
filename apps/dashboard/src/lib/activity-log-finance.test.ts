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

test("an AI budget plan reads with the month it plans", () => {
  for (const [type, label] of [
    ["finance.ai_budget_plan.requested", "AI budget plan requested"],
    ["finance.ai_budget_plan.created", "AI budget plan ready"],
    ["finance.ai_budget_plan.failed", "AI budget plan failed"],
    ["finance.ai_budget_plan.applied", "AI budget plan applied"],
    ["finance.ai_budget_plan.dismissed", "AI budget plan dismissed"],
  ]) {
    assert.ok(EVENT_TYPES.includes(type));
    assert.equal(
      eventLabel({ type, data: { month: "2026-11", planned: 119000 } }),
      `${label}: November 2026`,
    );
    assert.equal(eventLabel({ type, data: {} }), label);
  }
  assert.equal(
    eventLabel({
      type: "finance.ai_budget_plan.created",
      data: { month: "next month" },
    }),
    "AI budget plan ready",
  );
  assert.equal(subjectLabel("finance_ai_budget_plan"), "AI budget plan");
  assert.equal(
    actorLabel({ kind: "agent", name: "codex-budget-plan" }),
    "AI budget plan (Codex)",
  );
});
