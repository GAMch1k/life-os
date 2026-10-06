import { financeClient } from "@/lib/api/finance-client";

export type FinanceSummaryState =
  | {
      kind: "ready";
      summary: NonNullable<Awaited<ReturnType<typeof loadSummary>>>;
    }
  | { kind: "empty" }
  | { kind: "unavailable" };

async function loadSummary(month: string) {
  const { data, error } = await financeClient.GET("/v1/summaries/monthly", {
    params: { query: { month } },
  });

  if (error || !data) {
    return undefined;
  }

  return data;
}

export async function getFinanceSummary(): Promise<FinanceSummaryState> {
  const month = new Date().toISOString().slice(0, 7);
  const summary = await loadSummary(month);

  if (!summary) {
    return { kind: "unavailable" };
  }
  if (
    summary.totalBalance.amount === 0 &&
    summary.income.amount === 0 &&
    summary.spending.amount === 0 &&
    summary.spendingByCategory.length === 0
  ) {
    return { kind: "empty" };
  }

  return { kind: "ready", summary };
}
