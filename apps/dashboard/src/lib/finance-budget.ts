import { financeClient } from "@/lib/api/finance-client";
import type { components } from "@/lib/api/generated/finance";
import {
  type Money,
  amountInput,
  formatMoney,
  minorDigits,
  parseAmount,
} from "@/lib/finance-money";
import { monthLabel, shiftMonth } from "@/lib/finance-summary";
import { type Category, load } from "@/lib/finance-workspace";
import { DATE_LOCALE, addDays } from "@/lib/time-zone";

// The monthly budget's data and its pure rules: the plan editor's draft as a
// request body, the progress texts and tones, and Home's summary. Everything
// but the fetch helpers is pure and unit-tested.

type Schemas = components["schemas"];

export type BudgetMonth = Schemas["BudgetMonth"];
export type BudgetLine = Schemas["BudgetLine"];
export type BudgetLineStatus = Schemas["BudgetLineStatus"];
export type BudgetScope = Schemas["BudgetScope"];
export type BudgetSuggestions = Schemas["BudgetSuggestions"];
export type SaveBudgetBody = Schemas["SaveBudgetPlanRequest"];

/** The Finance page's tabs; `?tab=` in the URL picks one. */
export const FINANCE_TABS = [
  "transactions",
  "summary",
  "budget",
  "categories",
] as const;
export type FinanceTab = (typeof FINANCE_TABS)[number];

/** A tab named in the URL, or undefined. */
export function parseTab(value: unknown): FinanceTab | undefined {
  return FINANCE_TABS.find((tab) => tab === value);
}

/** A YYYY-MM month named in the URL, or undefined. */
export function parseMonth(value: unknown): string | undefined {
  if (typeof value !== "string" || !/^\d{4}-(0[1-9]|1[0-2])$/.test(value)) {
    return undefined;
  }
  const year = Number(value.slice(0, 4));
  return year >= 2000 && year <= 2200 ? value : undefined;
}

// ---- Loading ----------------------------------------------------------------

/** Starting amounts for a month's plan, from the last complete months. */
export function fetchBudgetSuggestions(month: string) {
  return load(
    () =>
      financeClient.GET("/v1/budgets/{month}/suggestions", {
        params: { path: { month } },
      }),
    "Suggestions could not be loaded.",
  );
}

// ---- Progress ---------------------------------------------------------------

/** The progress bar's fill, 0 to 100 (an overspent line stays full). */
export function progressValue(spent: number, planned: number) {
  if (planned <= 0) return spent > 0 ? 100 : 0;
  return Math.min(100, Math.max(0, Math.floor((spent * 100) / planned)));
}

/**
 * The progress bar's color for a status, as classes for shadcn's Progress
 * (its indicator keeps the CLI's markup): warning from 80%, red when over.
 */
export function statusTone(status: BudgetLineStatus) {
  switch (status) {
    case "over":
      return "[&_[data-slot=progress-indicator]]:bg-destructive";
    case "near":
      return "[&_[data-slot=progress-indicator]]:bg-warning";
    case "unplanned":
      return "[&_[data-slot=progress-indicator]]:bg-muted-foreground/60";
    default:
      return "[&_[data-slot=progress-indicator]]:bg-primary";
  }
}

/** The status of a total: like a line's, over above 100%, near from 80%. */
export function totalStatus(spent: number, planned: number): BudgetLineStatus {
  if (planned <= 0) return "unplanned";
  if (spent > planned) return "over";
  return spent * 100 >= planned * 80 ? "near" : "under";
}

/** What is left of a line, or how far over it is: "€40.00 left". */
export function remainingText(
  remaining: Money,
  locale: string,
  status?: BudgetLineStatus,
) {
  if (status === "unplanned") return "Not in the plan";
  if (remaining.amount < 0) {
    return `${formatMoney({ ...remaining, amount: -remaining.amount }, locale)} over`;
  }
  return `${formatMoney(remaining, locale)} left`;
}

/** "€260.00 of €300.00", or the spending alone without a planned amount. */
export function spentOfText(spent: Money, planned: Money, locale: string) {
  if (planned.amount <= 0) return formatMoney(spent, locale);
  return `${formatMoney(spent, locale)} of ${formatMoney(planned, locale)}`;
}

/** The pending part of a spending, as a short note; "" when none. */
export function pendingText(pending: Money, locale: string) {
  return pending.amount > 0
    ? `incl. ${formatMoney(pending, locale)} pending`
    : "";
}

/**
 * Days left in the month, today included, when it is the owner's current
 * month; undefined for any other month.
 */
export function daysLeft(month: string, today: string) {
  if (today.slice(0, 7) !== month) return undefined;
  const last = addDays(`${shiftMonth(month, 1)}-01`, -1);
  return Number(last.slice(8, 10)) - Number(today.slice(8, 10)) + 1;
}

/** Where the month's plan comes from, in a sentence. */
export function planSourceText(budget: BudgetMonth) {
  if (!budget.plan) return undefined;
  if (budget.plan.source === "month") {
    return `${monthLabel(budget.month)} has its own plan; the regular plan is unchanged.`;
  }
  return budget.plan.since === budget.month
    ? `The regular plan starts in ${monthLabel(budget.month)} and rolls forward to later months.`
    : `The regular plan, since ${monthLabel(budget.plan.since)}.`;
}

/** Transactions the budget leaves out, as a sentence; "" when none. */
export function excludedText(budget: BudgetMonth) {
  if (!budget.excludedCurrencies.length) return "";
  const parts = budget.excludedCurrencies.map(
    ({ currency, transactionCount }) =>
      `${transactionCount} ${transactionCount === 1 ? "transaction" : "transactions"} in ${currency}`,
  );
  const total = budget.excludedCurrencies.reduce(
    (sum, item) => sum + item.transactionCount,
    0,
  );
  return `${parts.join(" and ")} ${total === 1 ? "isn't" : "aren't"} counted: budgets use ${budget.currency}.`;
}

// ---- Plan editor ------------------------------------------------------------

export type BudgetDraft = {
  /** Expected income, as typed. */
  income: string;
  /** Savings goal, as typed. */
  savings: string;
  /** Amount per category id, as typed; "" leaves the category out. */
  amounts: Record<string, string>;
  scope: BudgetScope;
};

export type BudgetErrors = Partial<Record<string, string>> & {
  income?: string;
  savings?: string;
};

/**
 * The categories a plan can have: active expense categories, plus any
 * archived one the month's plan still has, by name.
 */
export function planCategories(categories: Category[], budget?: BudgetMonth) {
  const planned = new Set(
    budget?.lines
      .filter((line) => line.planned.amount > 0)
      .map((line) => line.categoryId) ?? [],
  );
  return categories
    .filter(
      (category) =>
        category.kind === "expense" &&
        (!category.archived || planned.has(category.id)),
    )
    .sort((a, b) => a.name.localeCompare(b.name));
}

/**
 * Saving for this month only keeps the regular plan; a first plan starts the
 * regular plan.
 */
export function defaultScope(budget: BudgetMonth): BudgetScope {
  return budget.plan ? "month" : "following";
}

const asInput = (minor: number, currency: string, locale: string) =>
  minor > 0 ? amountInput(minor, currency, locale) : "";

/** The editor's fields for a month's plan, empty without one. */
export function budgetDraft(budget: BudgetMonth, locale: string): BudgetDraft {
  const amounts: Record<string, string> = {};
  for (const line of budget.lines) {
    if (line.planned.amount > 0) {
      amounts[line.categoryId] = asInput(
        line.planned.amount,
        budget.currency,
        locale,
      );
    }
  }
  return {
    income: asInput(
      budget.plan?.expectedIncome.amount ?? 0,
      budget.currency,
      locale,
    ),
    savings: asInput(
      budget.plan?.savingsGoal.amount ?? 0,
      budget.currency,
      locale,
    ),
    amounts,
    scope: defaultScope(budget),
  };
}

/**
 * The suggestions in the fields still empty (all of them with
 * `overwrite`); a category without history stays as it is.
 */
export function withSuggestions(
  draft: BudgetDraft,
  suggestions: BudgetSuggestions,
  locale: string,
  overwrite = false,
): BudgetDraft {
  const amounts = { ...draft.amounts };
  for (const item of suggestions.categories) {
    if (!item.suggested) continue;
    if (overwrite || !amounts[item.categoryId]?.trim()) {
      amounts[item.categoryId] = asInput(
        item.suggested.amount,
        suggestions.currency,
        locale,
      );
    }
  }
  const income =
    suggestions.income && (overwrite || !draft.income.trim())
      ? asInput(suggestions.income.amount, suggestions.currency, locale)
      : draft.income;
  return { ...draft, income, amounts };
}

function readAmount(text: string, currency: string) {
  if (!text.trim()) return 0;
  const minor = parseAmount(text, currency);
  return minor === undefined || minor < 0 ? undefined : minor;
}

function amountError(currency: string) {
  const digits = minorDigits(currency);
  return digits
    ? `Enter an amount like 250.00, with at most ${digits} decimals, or leave it empty.`
    : "Enter a whole amount, like 250, or leave it empty.";
}

/** The plan's totals as typed, for the editor's live line. */
export function draftTotals(draft: BudgetDraft, currency: string) {
  let planned = 0;
  for (const text of Object.values(draft.amounts)) {
    planned += readAmount(text, currency) ?? 0;
  }
  const income = readAmount(draft.income, currency) ?? 0;
  const savings = readAmount(draft.savings, currency) ?? 0;
  return { planned, income, savings, unallocated: income - planned - savings };
}

/** The request that saves the draft, or the fields' errors. */
export function budgetBody(
  draft: BudgetDraft,
  currency: string,
  categoryIds: string[],
): { body?: SaveBudgetBody; errors: BudgetErrors } {
  const errors: BudgetErrors = {};
  const income = readAmount(draft.income, currency);
  const savings = readAmount(draft.savings, currency);
  if (income === undefined) errors.income = amountError(currency);
  if (savings === undefined) errors.savings = amountError(currency);
  const lines: SaveBudgetBody["lines"] = [];
  for (const categoryId of categoryIds) {
    const amount = readAmount(draft.amounts[categoryId] ?? "", currency);
    if (amount === undefined) errors[categoryId] = amountError(currency);
    else if (amount > 0) lines.push({ categoryId, amountMinor: amount });
  }
  if (Object.keys(errors).length) return { errors };
  return {
    body: {
      scope: draft.scope,
      expectedIncomeMinor: income ?? 0,
      savingsGoalMinor: savings ?? 0,
      lines,
    },
    errors,
  };
}

/** A suggestion's hint under a category's field. */
export function suggestionText(
  suggestion: BudgetSuggestions["categories"][number] | undefined,
  months: number,
  locale: string,
) {
  if (!suggestion?.suggested) return "No history yet";
  const span = months === 1 ? "1 month" : `${months} months`;
  return `Suggested ${formatMoney(suggestion.suggested, locale)} (median of ${span})`;
}

// ---- Home -------------------------------------------------------------------

/** How many categories Home lists under the month's progress. */
export const HOME_BUDGET_LINES = 3;

export type HomeBudget =
  | { kind: "none"; month: string }
  | {
      kind: "plan";
      month: string;
      currency: string;
      spent: number;
      planned: number;
      /** Categories over budget, most over first. */
      over: BudgetLine[];
      /** The planned categories closest to their amount, at most three. */
      closest: BudgetLine[];
      uncategorized: number;
    };

/** Home's view of the month's budget. */
export function homeBudget(budget: BudgetMonth): HomeBudget {
  if (!budget.plan) return { kind: "none", month: budget.month };
  const planned = budget.lines.filter((line) => line.planned.amount > 0);
  const share = (line: BudgetLine) => line.spent.amount / line.planned.amount;
  const over = planned
    .filter((line) => line.status === "over")
    .sort((a, b) => a.remaining.amount - b.remaining.amount);
  const closest = [...planned]
    .sort((a, b) => share(b) - share(a))
    .slice(0, HOME_BUDGET_LINES);
  return {
    kind: "plan",
    month: budget.month,
    currency: budget.currency,
    spent: budget.totals.spent.amount,
    planned: budget.totals.planned.amount,
    over,
    closest,
    uncategorized: budget.uncategorized.spent.amount,
  };
}

/** A YYYY-MM month's name alone: "October". */
export function monthName(month: string) {
  return new Intl.DateTimeFormat(DATE_LOCALE, {
    month: "long",
    timeZone: "UTC",
  }).format(new Date(`${month}-01T00:00:00Z`));
}

/** The Budget tab of a month, for links. */
export function budgetHref(month?: string) {
  return month ? `/finance?tab=budget&month=${month}` : "/finance?tab=budget";
}
