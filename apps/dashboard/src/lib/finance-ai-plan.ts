import { financeClient } from "@/lib/api/finance-client";
import type { components } from "@/lib/api/generated/finance";
import type { BudgetMonth } from "@/lib/finance-budget";
import {
  type Money,
  amountInput,
  formatMoney,
  minorDigits,
  parseAmount,
} from "@/lib/finance-money";
import { monthLabel, shiftMonth } from "@/lib/finance-summary";
import { load } from "@/lib/finance-workspace";
import { dateTimeFormat } from "@/lib/time-zone";

// The AI budget plan on the Budget tab (P13-09): the owner asks the AI to
// plan a month, the processor's job plans it, and the owner applies the
// lines they want or dismisses it. Finance keeps the plan; these are its
// pure rules (texts, the apply request) and the fetch helpers. Everything
// but the fetch helpers is pure and unit-tested.

type Schemas = components["schemas"];

export type AiBudgetPlan = Schemas["AiBudgetPlan"];
export type AiBudgetPlanState = Schemas["AiBudgetPlanState"];
export type AiBudgetPlanResult = Schemas["AiBudgetPlanResult"];
export type AiBudgetPlanLine = Schemas["AiBudgetPlanLine"];
export type AiBudgetSuggestion = Schemas["AiBudgetSuggestion"];
export type ApplyAiBudgetPlanBody = Schemas["ApplyAiBudgetPlanRequest"];

/** How many days into a month a plan is for that month by default. */
export const EARLY_DAYS = 7;

/** How often the card asks whether a plan it waits for is done. */
export const PLAN_POLL_MS = 5_000;

// ---- Loading ----------------------------------------------------------------

/** A month's latest plans, newest first. */
export function fetchAiPlans(month: string) {
  return load(
    () =>
      financeClient.GET("/v1/ai-budget-plans", {
        params: { query: { month, limit: 5 } },
      }),
    "The AI budget plan could not be loaded.",
  );
}

// ---- Months -----------------------------------------------------------------

/**
 * The month a plan is for when the owner picks none, as Finance decides:
 * this month during its first 7 days, else the next month. `today` is the
 * owner's YYYY-MM-DD.
 */
export function defaultPlanMonth(today: string) {
  const month = today.slice(0, 7);
  return Number(today.slice(8, 10)) <= EARLY_DAYS
    ? month
    : shiftMonth(month, 1);
}

/** The months the request dialog offers: this month and the next two. */
export function planMonthChoices(today: string) {
  const month = today.slice(0, 7);
  return [0, 1, 2].map((offset) => shiftMonth(month, offset));
}

// ---- State ------------------------------------------------------------------

/**
 * The plan the card shows: the month's newest, unless it was dismissed.
 * `items` come newest first.
 */
export function visiblePlan(items: AiBudgetPlan[]) {
  const [newest] = items;
  return newest && newest.state !== "dismissed" ? newest : undefined;
}

/** Whether the AI still has to plan it (requested or running). */
export function isActive(plan: AiBudgetPlan | undefined) {
  return plan?.state === "requested" || plan?.state === "running";
}

const RETRY_TIME: Intl.DateTimeFormatOptions = {
  hour: "2-digit",
  minute: "2-digit",
};

/** What a plan the AI has not made yet waits for, in a sentence. */
export function progressText(plan: AiBudgetPlan, timeZone: string) {
  if (plan.state === "running")
    return "The AI is planning… usually under a minute";
  if (plan.retryAt) {
    const at = dateTimeFormat(timeZone, RETRY_TIME).format(
      new Date(plan.retryAt),
    );
    return plan.error
      ? `Trying again at ${at}: ${plan.error}`
      : `Trying again at ${at}`;
  }
  return "Waiting for the AI…";
}

/** What the owner applied: "Applied 3 of 4 categories". */
export function appliedText(plan: AiBudgetPlan) {
  const applied = plan.applied?.lines.length ?? 0;
  const lines = plan.result?.lines.length ?? applied;
  return `Applied ${applied} of ${lines} ${lines === 1 ? "category" : "categories"}`;
}

// ---- Texts ------------------------------------------------------------------

/** A suggestion's saving: "≈ €12.00/month". */
export function savingText(money: Money, locale: string) {
  return `≈ ${formatMoney(money, locale)}/month`;
}

/** A line's trend: "↑ 60%", "↓ 12%", "steady", or "" without one. */
export function trendText(line: AiBudgetPlanLine) {
  if (!line.trend) return "";
  if (line.trend === "flat") return "steady";
  const arrow = line.trend === "up" ? "↑" : "↓";
  return line.trendPercent === null
    ? arrow
    : `${arrow} ${Math.abs(line.trendPercent)}%`;
}

/**
 * Finance's evidence for a line: "Usually €60.00 · last month €160.00 ·
 * ↑ 167%", leaving out what is missing.
 */
export function evidenceText(line: AiBudgetPlanLine, locale: string) {
  return [
    line.median ? `Usually ${formatMoney(line.median, locale)}` : "",
    line.lastMonth ? `last month ${formatMoney(line.lastMonth, locale)}` : "",
    trendText(line),
  ]
    .filter(Boolean)
    .join(" · ");
}

/** The note on a plan made from little history; "" for others. */
export function roughText(result: AiBudgetPlanResult) {
  if (!result.rough) return "";
  const months =
    result.fullMonths === 1
      ? "1 full month"
      : `${result.fullMonths} full months`;
  return `Rough plan: only ${months} of history.`;
}

/** What the plan saves a month, against the goal when there is one. */
export function goalText(result: AiBudgetPlanResult, locale: string) {
  const saved = result.projectedSavings;
  if (saved.amount < 0) {
    return `Spends ${formatMoney({ ...saved, amount: -saved.amount }, locale)} more than the income`;
  }
  const text = `Saves ${formatMoney(saved, locale)} a month`;
  const goal = result.savingsGoal;
  if (!goal || goal.amount <= 0) return text;
  const difference = saved.amount - goal.amount;
  if (difference >= 0) {
    return `${text}, ${formatMoney({ ...goal, amount: difference }, locale)} more than the ${formatMoney(goal, locale)} goal`;
  }
  return `${text}, ${formatMoney({ ...goal, amount: -difference }, locale)} short of the ${formatMoney(goal, locale)} goal`;
}

/** The card's title: "AI budget plan for November 2026". */
export function planTitle(plan: AiBudgetPlan) {
  return `AI budget plan for ${monthLabel(plan.month)}`;
}

// ---- Applying ---------------------------------------------------------------

/** Per category: whether the owner applies it, and the amount as typed. */
export type PlanDraft = Record<string, { checked: boolean; amount: string }>;

export type PlanDraftErrors = Partial<Record<string, string>> & {
  form?: string;
};

/** The table's fields: every line ticked, at the AI's amount. */
export function planDraft(plan: AiBudgetPlan, locale: string): PlanDraft {
  const draft: PlanDraft = {};
  for (const line of plan.result?.lines ?? []) {
    draft[line.categoryId] = {
      checked: true,
      amount: amountInput(line.proposed.amount, line.proposed.currency, locale),
    };
  }
  return draft;
}

function amountError(currency: string) {
  const digits = minorDigits(currency);
  return digits
    ? `Enter an amount like 250.00, with at most ${digits} decimals.`
    : "Enter a whole amount, like 250.";
}

/**
 * The request that applies the ticked lines, or the fields' errors. The
 * plan's goal goes with it when the owner asked with one; its income
 * estimate only when the month's budget has no expected income yet, so
 * the owner's own figure stays. `regular` makes it the regular plan from
 * the month on; otherwise only the month gets it.
 */
export function applyBody(
  draft: PlanDraft,
  plan: AiBudgetPlan,
  budget: BudgetMonth | undefined,
  regular: boolean,
): { body?: ApplyAiBudgetPlanBody; errors: PlanDraftErrors } {
  const errors: PlanDraftErrors = {};
  const lines: ApplyAiBudgetPlanBody["lines"] = [];
  for (const line of plan.result?.lines ?? []) {
    const field = draft[line.categoryId];
    if (!field?.checked) continue;
    const text = field.amount.trim();
    const amount = text ? parseAmount(text, plan.currency) : 0;
    if (amount === undefined || amount < 0) {
      errors[line.categoryId] = amountError(plan.currency);
    } else {
      lines.push({ categoryId: line.categoryId, amountMinor: amount });
    }
  }
  if (!lines.length && !Object.keys(errors).length) {
    errors.form = "Tick at least one category to apply.";
  }
  if (Object.keys(errors).length) return { errors };
  const body: ApplyAiBudgetPlanBody = {
    lines,
    scope: regular ? "following" : "month",
  };
  if (plan.savingsGoal) body.savingsGoalMinor = plan.savingsGoal.amount;
  const income = budget?.plan?.expectedIncome.amount ?? 0;
  if (plan.result && income === 0) {
    body.expectedIncomeMinor = plan.result.incomeEstimate.amount;
  }
  return { body, errors };
}
