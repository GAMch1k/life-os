import assert from "node:assert/strict";
import { test } from "node:test";

import {
  type BudgetLine,
  type BudgetMonth,
  type BudgetSuggestions,
  budgetBody,
  budgetDraft,
  budgetHref,
  daysLeft,
  defaultScope,
  draftTotals,
  excludedText,
  homeBudget,
  monthName,
  parseMonth,
  parseTab,
  pendingText,
  planCategories,
  planSourceText,
  progressValue,
  remainingText,
  spentOfText,
  statusTone,
  suggestionText,
  totalStatus,
  withSuggestions,
} from "./finance-budget";
import type { Category } from "./finance-workspace";

const eur = (amount: number) => ({ amount, currency: "EUR" });

function line(patch: Partial<BudgetLine> & { categoryId: string }): BudgetLine {
  return {
    categoryName: patch.categoryId,
    categoryColor: null,
    archived: false,
    planned: eur(0),
    spent: eur(0),
    pending: eur(0),
    remaining: eur(0),
    percent: null,
    status: "unplanned",
    transactionCount: 0,
    ...patch,
  };
}

function month(patch: Partial<BudgetMonth> = {}): BudgetMonth {
  return {
    month: "2026-10",
    currency: "EUR",
    plan: {
      source: "regular",
      since: "2026-10",
      expectedIncome: eur(300000),
      savingsGoal: eur(40000),
      unallocated: eur(220000),
      updatedAt: "2026-10-10T10:00:00Z",
    },
    lines: [],
    uncategorized: { spent: eur(0), pending: eur(0), transactionCount: 0 },
    totals: {
      planned: eur(40000),
      spent: eur(0),
      pending: eur(0),
      remaining: eur(40000),
      percent: 0,
    },
    income: { actual: eur(0), pending: eur(0) },
    savings: {
      movedToSavings: eur(0),
      leftover: eur(0),
      saved: eur(0),
      percent: 0,
    },
    excludedCurrencies: [],
    ...patch,
  };
}

function category(
  id: string,
  kind: Category["kind"] = "expense",
  archived = false,
): Category {
  return {
    id,
    name: id.charAt(0).toUpperCase() + id.slice(1),
    kind,
    color: null,
    systemKey: null,
    archived,
    archivedAt: null,
    createdAt: "2026-10-01T00:00:00Z",
    updatedAt: "2026-10-01T00:00:00Z",
  };
}

test("the URL picks a tab and a month, and nothing else", () => {
  assert.equal(parseTab("budget"), "budget");
  assert.equal(parseTab("categories"), "categories");
  assert.equal(parseTab("subscriptions"), "subscriptions");
  assert.equal(parseTab("Budget"), undefined);
  assert.equal(parseTab(["budget"]), undefined);
  assert.equal(parseMonth("2026-10"), "2026-10");
  assert.equal(parseMonth("2026-13"), undefined);
  assert.equal(parseMonth("1999-12"), undefined);
  assert.equal(parseMonth("2026-1"), undefined);
  assert.equal(budgetHref(), "/finance?tab=budget");
  assert.equal(budgetHref("2026-09"), "/finance?tab=budget&month=2026-09");
});

test("progress fills up to 100% and turns warning, then red", () => {
  assert.equal(progressValue(2600, 3000), 86);
  assert.equal(progressValue(4000, 3000), 100);
  assert.equal(progressValue(0, 3000), 0);
  assert.equal(progressValue(500, 0), 100);
  assert.equal(progressValue(0, 0), 0);
  assert.equal(totalStatus(2400, 3000), "near");
  assert.equal(totalStatus(2399, 3000), "under");
  assert.equal(totalStatus(3000, 3000), "near");
  assert.equal(totalStatus(3001, 3000), "over");
  assert.equal(totalStatus(10, 0), "unplanned");
  assert.match(statusTone("over"), /progress-indicator\]\]:bg-destructive$/);
  assert.match(statusTone("near"), /bg-warning$/);
  assert.match(statusTone("under"), /bg-primary$/);
});

test("amounts read as left, over, of, and pending", () => {
  assert.equal(remainingText(eur(4000), "en-GB"), "€40.00 left");
  assert.equal(remainingText(eur(-2000), "en-GB"), "€20.00 over");
  assert.equal(
    remainingText(eur(-5000), "en-GB", "unplanned"),
    "Not in the plan",
  );
  assert.equal(
    spentOfText(eur(26000), eur(30000), "en-GB"),
    "€260.00 of €300.00",
  );
  assert.equal(spentOfText(eur(5000), eur(0), "en-GB"), "€50.00");
  assert.equal(pendingText(eur(2000), "en-GB"), "incl. €20.00 pending");
  assert.equal(pendingText(eur(0), "en-GB"), "");
});

test("days left count today, only in the current month", () => {
  assert.equal(daysLeft("2026-10", "2026-10-11"), 21);
  assert.equal(daysLeft("2026-10", "2026-10-31"), 1);
  assert.equal(daysLeft("2028-02", "2028-02-01"), 29);
  assert.equal(daysLeft("2026-09", "2026-10-11"), undefined);
  assert.equal(monthName("2026-10"), "October");
});

test("the plan's source and the left-out currencies are explained", () => {
  assert.equal(
    planSourceText(month()),
    "The regular plan starts in October 2026 and rolls forward to later months.",
  );
  assert.equal(
    planSourceText(month({ month: "2026-12" })),
    "The regular plan, since October 2026.",
  );
  const own = month({ month: "2026-12" });
  own.plan = { ...own.plan!, source: "month", since: "2026-12" };
  assert.equal(
    planSourceText(own),
    "December 2026 has its own plan; the regular plan is unchanged.",
  );
  assert.equal(planSourceText(month({ plan: null })), undefined);
  assert.equal(excludedText(month()), "");
  assert.equal(
    excludedText(
      month({ excludedCurrencies: [{ currency: "USD", transactionCount: 1 }] }),
    ),
    "1 transaction in USD isn't counted: budgets use EUR.",
  );
  assert.equal(
    excludedText(
      month({
        excludedCurrencies: [
          { currency: "CHF", transactionCount: 1 },
          { currency: "USD", transactionCount: 2 },
        ],
      }),
    ),
    "1 transaction in CHF and 2 transactions in USD aren't counted: budgets use EUR.",
  );
});

test("the editor offers active expense categories and the plan's archived ones", () => {
  const budget = month({
    lines: [
      line({ categoryId: "old", planned: eur(1000), status: "under" }),
      line({ categoryId: "shopping", spent: eur(500) }),
    ],
  });
  const choices = planCategories(
    [
      category("rent"),
      category("old", "expense", true),
      category("gone", "expense", true),
      category("salary", "income"),
      category("savings", "transfer"),
      category("groceries"),
    ],
    budget,
  );
  assert.deepEqual(
    choices.map((item) => item.id),
    ["groceries", "old", "rent"],
  );
});

test("a draft starts from the plan, and a first plan from the suggestions", () => {
  const budget = month({
    lines: [
      line({ categoryId: "groceries", planned: eur(30000), status: "under" }),
      line({ categoryId: "shopping", spent: eur(500) }),
    ],
  });
  assert.equal(defaultScope(budget), "month");
  assert.equal(defaultScope(month({ plan: null })), "following");
  const draft = budgetDraft(budget, "en-GB");
  assert.deepEqual(draft, {
    income: "3000.00",
    savings: "400.00",
    amounts: { groceries: "300.00" },
    scope: "month",
  });
  assert.equal(budgetDraft(budget, "de-DE").income, "3000,00");

  const suggestions: BudgetSuggestions = {
    currency: "EUR",
    months: ["2026-07", "2026-08", "2026-09"],
    income: eur(310000),
    saved: eur(50000),
    categories: [
      { categoryId: "groceries", suggested: eur(32000), monthsWithSpending: 3 },
      { categoryId: "rent", suggested: eur(120000), monthsWithSpending: 3 },
      { categoryId: "fun", suggested: null, monthsWithSpending: 0 },
    ],
  };
  const filled = withSuggestions(draft, suggestions, "en-GB");
  assert.deepEqual(filled.amounts, { groceries: "300.00", rent: "1200.00" });
  assert.equal(filled.income, "3000.00");
  const replaced = withSuggestions(draft, suggestions, "en-GB", true);
  assert.deepEqual(replaced.amounts, { groceries: "320.00", rent: "1200.00" });
  assert.equal(replaced.income, "3100.00");
  assert.equal(
    suggestionText(suggestions.categories[0], 3, "en-GB"),
    "Suggested €320.00 (median of 3 months)",
  );
  assert.equal(
    suggestionText(suggestions.categories[2], 3, "en-GB"),
    "No history yet",
  );
  assert.equal(suggestionText(undefined, 0, "en-GB"), "No history yet");
});

test("a draft becomes the request, or says which amounts are wrong", () => {
  const draft = {
    income: "3.000,00",
    savings: "",
    amounts: { groceries: "300", rent: "", fun: "0", gone: "12" },
    scope: "following" as const,
  };
  const { body, errors } = budgetBody(draft, "EUR", [
    "groceries",
    "rent",
    "fun",
  ]);
  assert.deepEqual(errors, {});
  assert.deepEqual(body, {
    scope: "following",
    expectedIncomeMinor: 300000,
    savingsGoalMinor: 0,
    lines: [{ categoryId: "groceries", amountMinor: 30000 }],
  });
  const bad = budgetBody(
    { ...draft, savings: "-5", amounts: { groceries: "12.3456" } },
    "EUR",
    ["groceries"],
  );
  assert.equal(bad.body, undefined);
  assert.ok(bad.errors.savings);
  assert.ok(bad.errors.groceries);
  assert.deepEqual(draftTotals(draft, "EUR"), {
    planned: 31200,
    income: 300000,
    savings: 0,
    unallocated: 268800,
  });
});

test("Home lists what is over budget and what comes closest", () => {
  assert.deepEqual(homeBudget(month({ plan: null })), {
    kind: "none",
    month: "2026-10",
  });
  const budget = month({
    lines: [
      line({
        categoryId: "dining",
        planned: eur(10000),
        spent: eur(12000),
        remaining: eur(-2000),
        percent: 120,
        status: "over",
      }),
      line({
        categoryId: "groceries",
        planned: eur(30000),
        spent: eur(26000),
        remaining: eur(4000),
        percent: 86,
        status: "near",
      }),
      line({
        categoryId: "fun",
        planned: eur(5000),
        spent: eur(9000),
        remaining: eur(-4000),
        percent: 180,
        status: "over",
      }),
      line({
        categoryId: "rent",
        planned: eur(100000),
        spent: eur(0),
        remaining: eur(100000),
        percent: 0,
        status: "under",
      }),
      line({ categoryId: "shopping", spent: eur(99999) }),
    ],
    uncategorized: { spent: eur(3500), pending: eur(0), transactionCount: 1 },
  });
  const home = homeBudget(budget);
  assert.equal(home.kind, "plan");
  if (home.kind !== "plan") return;
  assert.deepEqual(
    home.over.map((item) => item.categoryId),
    ["fun", "dining"],
  );
  assert.deepEqual(
    home.closest.map((item) => item.categoryId),
    ["fun", "dining", "groceries"],
  );
  assert.equal(home.uncategorized, 3500);
  assert.equal(home.planned, 40000);
});
