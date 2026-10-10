import type { components } from "@/lib/api/generated/finance";
import { addDays, DATE_LOCALE, dayKey } from "@/lib/time-zone";

type CategoryTotal = components["schemas"]["CategoryTotal"];
type Category = components["schemas"]["Category"];

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

/** The month `months` after a YYYY-MM month (negative goes back). */
export function shiftMonth(month: string, months: number) {
  const date = new Date(`${month}-01T00:00:00Z`);
  date.setUTCMonth(date.getUTCMonth() + months);
  return date.toISOString().slice(0, 7);
}

/** A month's first and last day (YYYY-MM-DD), for a date filter. */
export function monthRange(month: string) {
  return {
    from: `${month}-01`,
    to: addDays(`${shiftMonth(month, 1)}-01`, -1),
  };
}

/** One line of the summary's by-category breakdown. */
export type SummaryRow = {
  /** The category id, or "uncategorized". */
  key: string;
  categoryId: string | null;
  name: string;
  kind: CategoryTotal["categoryKind"];
  color: string | null;
  /** Spending when it is the larger side, else income. */
  direction: "in" | "out";
  /** The larger side's size in minor units (positive). */
  amount: number;
  /** The amount against the largest row's, 0 to 1, for the bar. */
  share: number;
  transactionCount: number;
};

/**
 * The by-category totals as rows with bars: each row shows its larger side
 * (spending, or income for an income category), and its bar is that amount
 * against the largest row's. The API's order (largest spending first) is
 * kept; uncategorized transactions get their own row.
 */
export function summaryRows(
  byCategory: CategoryTotal[],
  categories: Category[] = [],
): SummaryRow[] {
  const colors = new Map(categories.map((item) => [item.id, item.color]));
  const rows = byCategory.map((total) => {
    const out = total.spending.amount >= total.income.amount;
    return {
      key: total.categoryId ?? "uncategorized",
      categoryId: total.categoryId,
      name: total.categoryName ?? "Uncategorized",
      kind: total.categoryKind,
      color: (total.categoryId && colors.get(total.categoryId)) || null,
      direction: out ? ("out" as const) : ("in" as const),
      amount: Math.abs(out ? total.spending.amount : total.income.amount),
      share: 0,
      transactionCount: total.transactionCount,
    };
  });
  const largest = Math.max(0, ...rows.map((row) => row.amount));
  return rows.map((row) => ({
    ...row,
    share: largest ? row.amount / largest : 0,
  }));
}
