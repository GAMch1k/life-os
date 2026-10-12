import assert from "node:assert/strict";
import test from "node:test";

import { budgetPlansFacts, type ProcessorStatus } from "./runtime-status";

type BudgetPlans = NonNullable<ProcessorStatus["budgetPlans"]>;

const value = (facts: { label: string; value: string }[], label: string) =>
  facts.find((fact) => fact.label === label)?.value;

const budgetPlans = (overrides: Partial<BudgetPlans> = {}): BudgetPlans => ({
  enabled: true,
  monthly: true,
  running: false,
  nextCheckAt: "2026-10-12T09:06:00Z",
  nextMonthlyAt: "2026-11-01T05:00:00Z",
  waiting: 0,
  lastCheckAt: "2026-10-12T09:05:30Z",
  lastRun: {
    planId: "p1",
    month: "2026-11",
    trigger: "request",
    startedAt: "2026-10-12T09:04:00Z",
    finishedAt: "2026-10-12T09:05:00Z",
    attempt: 1,
    outcome: "ready",
    categories: 10,
    suggestions: 3,
  },
  ...overrides,
});

test("the AI budget plan job says what it planned and when it plans next", () => {
  const facts = budgetPlansFacts(budgetPlans(), "Europe/Berlin");
  assert.equal(
    value(facts, "AI budget plans"),
    "November 2026 ready (3 ways to spend less), 12 Oct 2026, 11:05 CEST",
  );
  assert.equal(
    value(facts, "Next AI budget plan"),
    "monthly on 1 Nov 2026, 06:00 CET",
  );
  const run = budgetPlans().lastRun!;
  assert.equal(
    value(
      budgetPlansFacts(
        budgetPlans({
          lastRun: { ...run, outcome: "failed", error: "unusable answers" },
        }),
      ),
      "AI budget plans",
    ),
    "November 2026 failed: unusable answers, 12 Oct 2026, 09:05 UTC",
  );
  assert.equal(
    value(
      budgetPlansFacts(
        budgetPlans({
          lastRun: { ...run, outcome: "retry", error: "usage limit" },
        }),
      ),
      "AI budget plans",
    ),
    "November 2026 will be tried again: usage limit, 12 Oct 2026, 09:05 UTC",
  );
  assert.equal(
    value(
      budgetPlansFacts(
        budgetPlans({
          lastRun: { ...run, outcome: "dropped", suggestions: 1 },
        }),
      ),
      "AI budget plans",
    ),
    "November 2026 dropped (dismissed meanwhile), 12 Oct 2026, 09:05 UTC",
  );
  assert.equal(
    value(budgetPlansFacts(budgetPlans({ running: true })), "AI budget plans"),
    "planning now",
  );
  assert.equal(
    value(budgetPlansFacts(budgetPlans({ lastRun: null })), "AI budget plans"),
    "nothing planned since the processor started",
  );
  assert.equal(
    value(
      budgetPlansFacts(
        budgetPlans({ monthly: false, nextMonthlyAt: null, waiting: 2 }),
      ),
      "Next AI budget plan",
    ),
    "only when asked for, 2 waiting",
  );
});

test("a switched-off job says so, and an older processor shows nothing", () => {
  assert.deepEqual(
    budgetPlansFacts(
      budgetPlans({ enabled: false, nextCheckAt: null, nextMonthlyAt: null }),
    ),
    [{ label: "AI budget plans", value: "off (LIFE_OS_AI_BUDGET_PLANS)" }],
  );
  assert.deepEqual(budgetPlansFacts(undefined), []);
});
