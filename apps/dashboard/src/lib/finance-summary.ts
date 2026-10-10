import { financeClient } from "@/lib/api/finance-client";
import { DATE_LOCALE, dayKey } from "@/lib/time-zone";
import { userTimeZone } from "@/lib/user-time-zone";

type MonthlySummary = NonNullable<Awaited<ReturnType<typeof loadSummary>>>;

export type FinanceSummaryState =
  | { kind: "ready"; month: string; summary: MonthlySummary }
  | { kind: "empty"; month: string }
  | { kind: "unavailable"; month: string };

/** The current month (YYYY-MM) in the owner's timezone. */
export function currentMonth(now: Date, timeZone: string) {
  return dayKey(now, timeZone).slice(0, 7);
}

/** A YYYY-MM month as text, for example "October 2026". */
export function monthLabel(month: string) {
  return new Intl.DateTimeFormat(DATE_LOCALE, {
    month: "long",
    year: "numeric",
    timeZone: "UTC",
  }).format(new Date(`${month}-01T00:00:00Z`));
}

async function loadSummary(month: string) {
  try {
    const { data, error } = await financeClient.GET("/v1/summaries/monthly", {
      params: { query: { month } },
    });

    if (error || !data) {
      return undefined;
    }

    return data;
  } catch {
    return undefined;
  }
}

export async function getFinanceSummary(): Promise<FinanceSummaryState> {
  const month = currentMonth(new Date(), await userTimeZone());
  const summary = await loadSummary(month);

  if (!summary) {
    return { kind: "unavailable", month };
  }
  if (
    summary.totalBalance.amount === 0 &&
    summary.income.amount === 0 &&
    summary.spending.amount === 0 &&
    summary.spendingByCategory.length === 0
  ) {
    return { kind: "empty", month };
  }

  return { kind: "ready", month, summary };
}
