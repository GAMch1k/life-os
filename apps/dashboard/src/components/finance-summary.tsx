import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { CSV_COLUMNS, FinanceImport } from "@/components/finance-import";
import { type FinanceSummaryState, monthLabel } from "@/lib/finance-summary";
import { cn } from "@/lib/utils";

function money(amount: number, currency: string) {
  return new Intl.NumberFormat("en", {
    style: "currency",
    currency,
    maximumFractionDigits: 2,
  }).format(amount / 100);
}

export function FinanceSummary({ state }: { state: FinanceSummaryState }) {
  return (
    <div className="mx-auto max-w-5xl space-y-3" id="finance">
      <div className="relative flex flex-wrap items-center justify-between gap-x-3 gap-y-2">
        <h1 className="flex items-baseline gap-2 text-lg font-semibold tracking-tight">
          Finance
          <span className="text-muted-foreground text-sm font-normal">
            {monthLabel(state.month)}
          </span>
        </h1>
        {state.kind === "unavailable" ? null : <FinanceImport />}
      </div>
      {state.kind === "unavailable" ? (
        <p className="text-muted-foreground text-sm">
          Finance is unavailable right now.
        </p>
      ) : state.kind === "empty" ? (
        <>
          <p className="text-muted-foreground text-sm">
            No transactions this month yet. Import a CSV to see your cash flow.
          </p>
          <CsvHint />
        </>
      ) : (
        <ReadyFinance summary={state.summary} />
      )}
    </div>
  );
}

function ReadyFinance({
  summary,
}: {
  summary: Extract<FinanceSummaryState, { kind: "ready" }>["summary"];
}) {
  return (
    <>
      <div className="grid grid-cols-2 gap-2 sm:gap-3 lg:grid-cols-4">
        <Metric
          label="Balance"
          value={money(
            summary.totalBalance.amount,
            summary.totalBalance.currency,
          )}
        />
        <Metric
          label="Income"
          value={money(summary.income.amount, summary.income.currency)}
        />
        <Metric
          label="Expenses"
          value={money(summary.spending.amount, summary.spending.currency)}
        />
        <Metric
          className={
            summary.net.amount >= 0 ? "text-success" : "text-destructive"
          }
          label="Net this month"
          value={money(summary.net.amount, summary.net.currency)}
        />
      </div>
      <Card size="sm">
        <CardHeader>
          <CardTitle>Spending by category</CardTitle>
        </CardHeader>
        <CardContent>
          {summary.spendingByCategory.length === 0 ? (
            <p className="text-muted-foreground">
              No categorized spending this month.
            </p>
          ) : (
            <ul className="divide-y">
              {summary.spendingByCategory.map((category) => (
                <li
                  className="flex items-baseline justify-between gap-3 py-1.5 first:pt-0 last:pb-0"
                  key={category.categoryId}
                >
                  <span className="min-w-0 wrap-anywhere">
                    {category.categoryName}
                  </span>
                  <span className="shrink-0 font-medium tabular-nums">
                    {money(category.amount.amount, category.amount.currency)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
      <CsvHint />
    </>
  );
}

function CsvHint() {
  return (
    <p className="text-muted-foreground text-xs wrap-anywhere">
      CSV columns: {CSV_COLUMNS}.
    </p>
  );
}

function Metric({
  className,
  label,
  value,
}: {
  className?: string;
  label: string;
  value: string;
}) {
  return (
    <Card size="sm">
      <CardContent>
        <p className="text-muted-foreground text-xs font-medium">{label}</p>
        <p
          className={cn(
            "mt-0.5 text-lg font-semibold tracking-tight wrap-anywhere tabular-nums sm:text-xl",
            className,
          )}
        >
          {value}
        </p>
      </CardContent>
    </Card>
  );
}
