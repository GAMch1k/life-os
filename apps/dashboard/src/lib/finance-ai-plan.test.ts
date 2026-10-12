import assert from "node:assert/strict";
import { test } from "node:test";

import {
  type AiBudgetPlan,
  type AiBudgetPlanLine,
  applyBody,
  appliedText,
  defaultPlanMonth,
  evidenceText,
  goalText,
  isActive,
  planDraft,
  planMonthChoices,
  planTitle,
  progressText,
  roughText,
  savingText,
  trendText,
  triggerText,
  visiblePlan,
} from "./finance-ai-plan";
import type { BudgetMonth } from "./finance-budget";

const eur = (amount: number) => ({ amount, currency: "EUR" });

const line = (overrides: Partial<AiBudgetPlanLine> = {}): AiBudgetPlanLine => ({
  categoryId: "c-eating",
  categoryName: "Eating out",
  proposed: eur(9000),
  reason: "Eating out grew; bring it back down.",
  current: eur(0),
  median: eur(6000),
  lastMonth: eur(16000),
  trend: "up",
  trendPercent: 167,
  ...overrides,
});

const plan = (overrides: Partial<AiBudgetPlan> = {}): AiBudgetPlan => ({
  id: "p1",
  month: "2026-11",
  currency: "EUR",
  trigger: "request",
  salary: null,
  state: "ready",
  savingsGoal: null,
  requestedBy: { kind: "user" },
  attempts: 1,
  claimedAt: "2026-10-12T09:00:00Z",
  retryAt: null,
  error: null,
  finishedAt: "2026-10-12T09:01:00Z",
  result: {
    headline: "Money is tight: eat out less.",
    rough: false,
    fullMonths: 2,
    lines: [
      line(),
      line({
        categoryId: "c-groceries",
        categoryName: "Groceries",
        proposed: eur(30000),
        current: eur(35000),
        trend: "flat",
        trendPercent: 7,
      }),
    ],
    suggestions: [],
    projectedTotal: eur(39000),
    incomeEstimate: eur(205000),
    projectedSavings: eur(166000),
    savingsGoal: null,
    goalGap: null,
  },
  applied: null,
  dismissedAt: null,
  createdAt: "2026-10-12T09:00:00Z",
  updatedAt: "2026-10-12T09:01:00Z",
  ...overrides,
});

test("a plan is for this month in its first week, else for the next", () => {
  assert.equal(defaultPlanMonth("2026-10-01"), "2026-10");
  assert.equal(defaultPlanMonth("2026-10-07"), "2026-10");
  assert.equal(defaultPlanMonth("2026-10-08"), "2026-11");
  assert.equal(defaultPlanMonth("2026-12-31"), "2027-01");
  assert.deepEqual(planMonthChoices("2026-11-20"), [
    "2026-11",
    "2026-12",
    "2027-01",
  ]);
});

test("the card shows the month's newest plan unless it was dismissed", () => {
  assert.equal(visiblePlan([]), undefined);
  const newest = plan({ id: "new", state: "running" });
  assert.equal(visiblePlan([newest, plan({ id: "old" })])?.id, "new");
  assert.equal(
    visiblePlan([plan({ state: "dismissed" }), plan({ id: "old" })]),
    undefined,
  );
  assert.equal(isActive(plan({ state: "requested" })), true);
  assert.equal(isActive(plan({ state: "running" })), true);
  assert.equal(isActive(plan()), false);
  assert.equal(isActive(undefined), false);
  assert.equal(planTitle(plan()), "AI budget plan for November 2026");
});

test("progress says what a plan waits for", () => {
  const zone = "Europe/Berlin";
  assert.equal(
    progressText(plan({ state: "requested" }), zone),
    "Waiting for the AI…",
  );
  assert.equal(
    progressText(plan({ state: "running" }), zone),
    "The AI is planning… usually under a minute",
  );
  assert.equal(
    progressText(
      plan({
        state: "requested",
        retryAt: "2026-10-12T12:05:00Z",
        error: "ERROR: You've hit your usage limit",
      }),
      zone,
    ),
    "Trying again at 14:05: ERROR: You've hit your usage limit",
  );
});

test("texts give the savings, the evidence, the trend, and the goal", () => {
  assert.equal(savingText(eur(1200), "en-GB"), "≈ €12.00/month");
  assert.equal(trendText(line()), "↑ 167%");
  assert.equal(trendText(line({ trend: "down", trendPercent: -12 })), "↓ 12%");
  assert.equal(trendText(line({ trend: "flat" })), "steady");
  assert.equal(trendText(line({ trend: null, trendPercent: null })), "");
  assert.equal(
    evidenceText(line(), "en-GB"),
    "Usually €60.00 · last month €160.00 · ↑ 167%",
  );
  assert.equal(
    evidenceText(
      line({ median: null, lastMonth: eur(0), trend: null }),
      "en-GB",
    ),
    "last month €0.00",
  );
  const result = plan().result!;
  assert.equal(roughText(result), "");
  assert.equal(
    roughText({ ...result, rough: true, fullMonths: 1 }),
    "Rough plan: only 1 full month of history.",
  );
  assert.equal(
    roughText({ ...result, rough: true, fullMonths: 0 }),
    "Rough plan: only 0 full months of history.",
  );
  assert.equal(goalText(result, "en-GB"), "Saves €1,660.00 a month");
  assert.equal(
    goalText(
      { ...result, projectedSavings: eur(86000), savingsGoal: eur(50000) },
      "en-GB",
    ),
    "Saves €860.00 a month, €360.00 more than the €500.00 goal",
  );
  assert.equal(
    goalText(
      { ...result, projectedSavings: eur(10000), savingsGoal: eur(50000) },
      "en-GB",
    ),
    "Saves €100.00 a month, €400.00 short of the €500.00 goal",
  );
  assert.equal(
    goalText({ ...result, projectedSavings: eur(-4000) }, "en-GB"),
    "Spends €40.00 more than the income",
  );
});

const budget = (expectedIncome?: number): BudgetMonth =>
  ({
    month: "2026-11",
    currency: "EUR",
    plan:
      expectedIncome === undefined
        ? null
        : {
            source: "regular",
            since: "2026-10",
            expectedIncome: eur(expectedIncome),
            savingsGoal: eur(0),
            unallocated: eur(0),
            updatedAt: "2026-10-01T00:00:00Z",
          },
  }) as BudgetMonth;

test("applying sends the ticked lines, the goal, and the income when unset", () => {
  const ready = plan({ savingsGoal: eur(50000) });
  const draft = planDraft(ready, "de-DE");
  assert.deepEqual(draft, {
    "c-eating": { checked: true, amount: "90,00" },
    "c-groceries": { checked: true, amount: "300,00" },
  });
  draft["c-groceries"].checked = false;
  draft["c-eating"].amount = "80,50";
  assert.deepEqual(applyBody(draft, ready, budget(), false), {
    body: {
      lines: [{ categoryId: "c-eating", amountMinor: 8050 }],
      scope: "month",
      savingsGoalMinor: 50000,
      expectedIncomeMinor: 205000,
    },
    errors: {},
  });
  // The owner's own expected income stays; no goal was asked for.
  assert.deepEqual(
    applyBody(planDraft(plan(), "en-GB"), plan(), budget(200000), true).body,
    {
      lines: [
        { categoryId: "c-eating", amountMinor: 9000 },
        { categoryId: "c-groceries", amountMinor: 30000 },
      ],
      scope: "following",
    },
  );
  // An empty amount is 0, which leaves the category out of the budget.
  const empty = planDraft(plan(), "en-GB");
  empty["c-eating"].amount = " ";
  assert.equal(
    applyBody(empty, plan(), budget(0), false).body?.lines[0].amountMinor,
    0,
  );
});

test("applying refuses bad amounts and nothing ticked", () => {
  const draft = planDraft(plan(), "en-GB");
  draft["c-eating"].amount = "lots";
  draft["c-groceries"].amount = "-5";
  assert.deepEqual(applyBody(draft, plan(), budget(), false), {
    errors: {
      "c-eating": "Enter an amount like 250.00, with at most 2 decimals.",
      "c-groceries": "Enter an amount like 250.00, with at most 2 decimals.",
    },
  });
  const none = planDraft(plan(), "en-GB");
  none["c-eating"].checked = false;
  none["c-groceries"].checked = false;
  assert.deepEqual(applyBody(none, plan(), budget(), false), {
    errors: { form: "Tick at least one category to apply." },
  });
});

test("an applied plan says how much of it was applied", () => {
  const applied = plan({
    state: "partly_applied",
    applied: {
      at: "2026-10-12T10:00:00Z",
      scope: "month",
      lines: [{ categoryId: "c-eating", amount: eur(9000) }],
      expectedIncome: null,
      savingsGoal: null,
    },
  });
  assert.equal(appliedText(applied), "Applied 1 of 2 categories");
});

test("a plan the salary asked for says when it arrived", () => {
  assert.equal(triggerText(plan()), "");
  assert.equal(triggerText(plan({ trigger: "schedule" })), "");
  assert.equal(
    triggerText(
      plan({
        trigger: "salary",
        salary: { bookingDate: "2026-10-28", transactionId: "t1" },
      }),
    ),
    "Planned when your salary arrived on 28 Oct",
  );
});
