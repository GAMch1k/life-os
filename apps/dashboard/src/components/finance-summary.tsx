import { Landmark } from "lucide-react";

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { FinanceImport } from "@/components/finance-import";
import type { FinanceSummaryState } from "@/lib/finance-summary";

function money(amount: number, currency: string) {
  return new Intl.NumberFormat("en", {
    style: "currency",
    currency,
    maximumFractionDigits: 2,
  }).format(amount / 100);
}

export function FinanceSummary({ state }: { state: FinanceSummaryState }) {
  if (state.kind === "unavailable") {
    return (
      <Card id="finance">
        <CardHeader>
          <CardTitle>Finance</CardTitle>
          <CardDescription>
            Finance is temporarily unavailable. Your data has not been replaced
            with placeholders.
          </CardDescription>
        </CardHeader>
      </Card>
    );
  }

  if (state.kind === "empty") {
    return <EmptyFinance />;
  }

  const { summary } = state;
  const netTone =
    summary.net.amount >= 0 ? "text-emerald-600" : "text-rose-600";

  return (
    <Card id="finance">
      <CardHeader>
        <div className="flex items-center gap-2">
          <Landmark aria-hidden="true" className="size-4" />
          <CardTitle>Finance</CardTitle>
        </div>
        <CardDescription>
          Current balance and this month&apos;s cash flow.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <section className="bg-muted/50 rounded-lg p-4 sm:col-span-2">
          <p className="text-muted-foreground text-xs font-medium">
            Current balance
          </p>
          <p className="mt-1 text-3xl font-semibold tracking-tight">
            {money(summary.totalBalance.amount, summary.totalBalance.currency)}
          </p>
        </section>
        <Metric
          label="Income"
          money={money(summary.income.amount, summary.income.currency)}
        />
        <Metric
          label="Expenses"
          money={money(summary.spending.amount, summary.spending.currency)}
        />
        <section className="rounded-lg border p-4 sm:col-span-2 lg:col-span-4">
          <p className="text-muted-foreground text-xs font-medium">
            Monthly net
          </p>
          <p className={`mt-1 text-2xl font-semibold ${netTone}`}>
            {money(summary.net.amount, summary.net.currency)}
          </p>
        </section>
        <section className="sm:col-span-2 lg:col-span-4">
          <p className="text-muted-foreground mb-2 text-xs font-medium">
            Spending by category
          </p>
          {summary.spendingByCategory.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              No categorized spending this month.
            </p>
          ) : (
            <ul className="divide-y rounded-lg border">
              {summary.spendingByCategory.map((category) => (
                <li
                  className="flex items-center justify-between gap-4 px-4 py-3 text-sm"
                  key={category.categoryId}
                >
                  <span>{category.categoryName}</span>
                  <span className="font-medium">
                    {money(category.amount.amount, category.amount.currency)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </section>
        <FinanceImport />
      </CardContent>
    </Card>
  );
}

function EmptyFinance() {
  return (
    <Card id="finance">
      <CardHeader>
        <CardTitle>Finance</CardTitle>
        <CardDescription>
          Import transactions to start seeing this month&apos;s cash flow.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <FinanceImport />
      </CardContent>
    </Card>
  );
}

function Metric({ label, money }: { label: string; money: string }) {
  return (
    <section className="rounded-lg border p-4">
      <p className="text-muted-foreground text-xs font-medium">{label}</p>
      <p className="mt-1 text-xl font-semibold">{money}</p>
    </section>
  );
}
