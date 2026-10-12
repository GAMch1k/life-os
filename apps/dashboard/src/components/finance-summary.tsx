"use client";

import { ChevronLeft, ChevronRight } from "lucide-react";
import type { ReactNode } from "react";

import { Amount, BalanceBreakdown } from "@/components/finance-amount";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { type Money } from "@/lib/finance-money";
import { monthLabel, shiftMonth, summaryRows } from "@/lib/finance-summary";
import type { Category, Loaded, MonthlySummary } from "@/lib/finance-workspace";
import { cn } from "@/lib/utils";

/**
 * A month's booked money in one currency: income, spending, net, and the
 * accounts' balance now (pending money included, split under it), then
 * every category with transactions in the month as a bar, largest spending
 * first. Transfers between own accounts are listed but not counted. A
 * category opens its transactions.
 */
export function FinanceSummary({
  categories,
  currencies,
  currency,
  loading,
  month,
  summary,
  thisMonth,
  onCurrencyChange,
  onMonthChange,
  onShowCategory,
}: {
  categories: Category[];
  /** The accounts' currencies; a picker shows when there is more than one. */
  currencies: string[];
  /** The chosen currency, or "" for the Cash account's. */
  currency: string;
  loading: boolean;
  /** YYYY-MM. */
  month: string;
  summary: Loaded<MonthlySummary>;
  /** The owner's current month (YYYY-MM). */
  thisMonth: string;
  onCurrencyChange: (currency: string) => void;
  onMonthChange: (month: string) => void;
  /** Shows the month's transactions of a category (null: uncategorized). */
  onShowCategory: (categoryId: string | null) => void;
}) {
  const label = monthLabel(month);
  return (
    <section aria-labelledby="finance-summary-title" className="grid gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Button
          aria-label="Previous month"
          onClick={() => onMonthChange(shiftMonth(month, -1))}
          size="icon-sm"
          variant="outline"
        >
          <ChevronLeft aria-hidden="true" />
        </Button>
        <h2
          className="min-w-36 text-center text-sm font-semibold"
          id="finance-summary-title"
        >
          {label}
        </h2>
        <Button
          aria-label="Next month"
          onClick={() => onMonthChange(shiftMonth(month, 1))}
          size="icon-sm"
          variant="outline"
        >
          <ChevronRight aria-hidden="true" />
        </Button>
        {month !== thisMonth ? (
          <Button
            onClick={() => onMonthChange(thisMonth)}
            size="sm"
            variant="ghost"
          >
            This month
          </Button>
        ) : null}
        {currencies.length > 1 ? (
          <NativeSelect
            aria-label="Currency"
            className="ml-auto"
            onChange={(event) => onCurrencyChange(event.target.value)}
            size="sm"
            value={currency || currencies[0]}
          >
            {currencies.map((code) => (
              <NativeSelectOption key={code} value={code}>
                {code}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        ) : null}
      </div>

      {summary.error !== undefined ? (
        <p className="text-destructive text-sm" role="alert">
          {summary.error}
        </p>
      ) : (
        <div
          aria-busy={loading}
          className={cn(
            "grid gap-3 transition-opacity",
            loading && "opacity-60",
          )}
          data-testid="finance-summary"
        >
          <div className="grid grid-cols-2 gap-2 sm:gap-3 lg:grid-cols-4">
            <Metric
              detail={
                <BalanceBreakdown
                  booked={summary.data.totalBookedBalance}
                  className="mt-0.5"
                  pending={summary.data.totalPending}
                />
              }
              label="Balance now"
              money={summary.data.totalBalance}
            />
            <Metric label="Income" money={summary.data.income} />
            <Metric label="Spending" money={summary.data.spending} />
            <Metric
              className={
                summary.data.net.amount >= 0
                  ? "text-success"
                  : "text-destructive"
              }
              label="Net"
              money={summary.data.net}
            />
          </div>
          <Card size="sm">
            <CardHeader>
              <CardTitle>By category</CardTitle>
            </CardHeader>
            <CardContent>
              <CategoryBreakdown
                categories={categories}
                currency={summary.data.currency}
                monthName={label}
                onShowCategory={onShowCategory}
                summary={summary.data}
              />
            </CardContent>
          </Card>
        </div>
      )}
    </section>
  );
}

function CategoryBreakdown({
  categories,
  currency,
  monthName,
  summary,
  onShowCategory,
}: {
  categories: Category[];
  currency: string;
  monthName: string;
  summary: MonthlySummary;
  onShowCategory: (categoryId: string | null) => void;
}) {
  const rows = summaryRows(summary.byCategory, categories);
  if (!rows.length) {
    return (
      <p className="text-muted-foreground">
        No booked transactions in {monthName}.
      </p>
    );
  }
  return (
    <ul className="grid gap-2.5">
      {rows.map((row) => (
        <li
          className="grid gap-1"
          data-testid="finance-summary-row"
          key={row.key}
        >
          <div className="flex min-w-0 items-baseline gap-2">
            <button
              className="focus-visible:ring-ring/50 flex min-w-0 items-center gap-1.5 rounded-sm text-left outline-none hover:underline focus-visible:ring-3"
              onClick={() => onShowCategory(row.categoryId)}
              title={`Show ${row.name}'s transactions in ${monthName}`}
              type="button"
            >
              <span
                aria-hidden="true"
                className="bg-muted-foreground/40 size-2 shrink-0 rounded-full"
                style={row.color ? { backgroundColor: row.color } : undefined}
              />
              <span className="truncate font-medium">{row.name}</span>
            </button>
            {row.kind === "transfer" ? (
              <Badge
                title="Money moved between your own accounts"
                variant="outline"
              >
                Transfer, not counted
              </Badge>
            ) : null}
            <span className="text-muted-foreground shrink-0 text-xs tabular-nums">
              {row.transactionCount}{" "}
              {row.transactionCount === 1 ? "transaction" : "transactions"}
            </span>
            <span className="ml-auto shrink-0 font-medium">
              <Amount
                money={{
                  amount: row.direction === "out" ? -row.amount : row.amount,
                  currency,
                }}
                signed
              />
            </span>
          </div>
          {/* One series, one color: the bar is the amount against the
              largest row's; the color dot beside the name is the identity. */}
          <div
            aria-hidden="true"
            className="bg-muted h-1.5 overflow-hidden rounded-full"
          >
            <div
              className="bg-primary/70 h-full rounded-full"
              style={{
                width: `${Math.max(row.share * 100, row.amount ? 1 : 0)}%`,
              }}
            />
          </div>
        </li>
      ))}
    </ul>
  );
}

function Metric({
  className,
  detail,
  label,
  money,
}: {
  className?: string;
  /** A quiet line under the amount. */
  detail?: ReactNode;
  label: string;
  money: Money;
}) {
  return (
    <Card size="sm">
      <CardContent>
        <p className="text-muted-foreground text-xs font-medium">{label}</p>
        <Amount
          className={cn(
            "mt-0.5 block text-lg font-semibold tracking-tight wrap-anywhere sm:text-xl",
            className,
          )}
          money={money}
        />
        {detail}
      </CardContent>
    </Card>
  );
}
