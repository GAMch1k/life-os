"use client";

import {
  ChevronLeft,
  ChevronRight,
  Pencil,
  PiggyBank,
  RotateCcw,
  Sparkles,
} from "lucide-react";
import type { ReactNode } from "react";

import { Amount } from "@/components/finance-amount";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import {
  type BudgetLine,
  type BudgetMonth,
  daysLeft,
  excludedText,
  pendingText,
  planSourceText,
  progressValue,
  remainingText,
  spentOfText,
  statusTone,
  totalStatus,
} from "@/lib/finance-budget";
import { type Money, formatMoney } from "@/lib/finance-money";
import { monthLabel, shiftMonth } from "@/lib/finance-summary";
import type { Loaded } from "@/lib/finance-workspace";
import { useBrowserLocale } from "@/lib/use-browser-locale";
import { cn } from "@/lib/utils";

/**
 * A month's budget: what was spent against the plan, per category and in
 * total, live during the month (booked and pending money out in the budget
 * currency), with the income, the savings against their goal, and
 * uncategorized spending as its own line. A category opens its
 * transactions of the month; the plan is edited in a dialog. The AI can
 * propose a plan too ("Plan my budget"), shown above the month as `aiPlan`
 * until the owner applies or dismisses it.
 */
export function FinanceBudget({
  actionError,
  aiPlan,
  budget,
  loading,
  month,
  pending,
  thisMonth,
  today,
  onEdit,
  onMonthChange,
  onPlanWithAI,
  onReset,
  onShowCategory,
}: {
  /** Why the last plan change failed, if it did. */
  actionError?: string;
  /** The AI's plan for the month (AiPlanCard), if any. */
  aiPlan?: ReactNode;
  budget: Loaded<BudgetMonth>;
  loading: boolean;
  /** YYYY-MM. */
  month: string;
  /** A plan change is being saved. */
  pending: boolean;
  /** The owner's current month (YYYY-MM). */
  thisMonth: string;
  /** The owner's today (YYYY-MM-DD). */
  today: string;
  onEdit: (budget: BudgetMonth) => void;
  onMonthChange: (month: string) => void;
  /** Asks the AI to plan a month. */
  onPlanWithAI?: () => void;
  /** Returns a month with its own plan to the regular plan. */
  onReset: (month: string) => void;
  /** Shows the month's transactions of a category (null: uncategorized). */
  onShowCategory: (categoryId: string | null) => void;
}) {
  const label = monthLabel(month);
  const data = budget.data;
  return (
    <section aria-labelledby="finance-budget-title" className="grid gap-3">
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
          id="finance-budget-title"
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
        {data ? (
          <div className="ml-auto flex flex-wrap gap-2">
            {data.plan?.source === "month" ? (
              <Button
                disabled={pending}
                onClick={() => onReset(month)}
                size="sm"
                variant="outline"
              >
                <RotateCcw aria-hidden="true" />
                Use the regular plan
              </Button>
            ) : null}
            {onPlanWithAI ? (
              <Button
                disabled={pending}
                onClick={onPlanWithAI}
                size="sm"
                variant="outline"
              >
                <Sparkles aria-hidden="true" />
                Plan my budget
              </Button>
            ) : null}
            <Button disabled={pending} onClick={() => onEdit(data)} size="sm">
              <Pencil aria-hidden="true" />
              {data.plan ? "Edit plan" : `Plan ${label}`}
            </Button>
          </div>
        ) : null}
      </div>

      {actionError ? (
        <p className="text-destructive text-sm" role="alert">
          {actionError}
        </p>
      ) : null}
      {aiPlan}
      {budget.error !== undefined ? (
        <p className="text-destructive text-sm" role="alert">
          {budget.error}
        </p>
      ) : (
        <div
          aria-busy={loading}
          className={cn(
            "grid gap-3 transition-opacity",
            loading && "opacity-60",
          )}
          data-testid="finance-budget"
        >
          {budget.data.plan ? (
            <Overview budget={budget.data} today={today} />
          ) : (
            <Card size="sm">
              <CardHeader>
                <CardTitle>No budget for {label} yet</CardTitle>
                <CardDescription>
                  Plan the month: your expected income, an amount for each
                  category, and a savings goal. Starting amounts come from your
                  spending in the last three months.
                </CardDescription>
              </CardHeader>
            </Card>
          )}
          <Lines
            budget={budget.data}
            monthName={label}
            onShowCategory={onShowCategory}
          />
          <Notes budget={budget.data} />
        </div>
      )}
    </section>
  );
}

function Overview({ budget, today }: { budget: BudgetMonth; today: string }) {
  const locale = useBrowserLocale();
  const { totals, income, savings, plan } = budget;
  const left = daysLeft(budget.month, today);
  const goal = plan?.savingsGoal ?? { amount: 0, currency: budget.currency };
  return (
    <div className="grid grid-cols-2 gap-2 sm:gap-3 lg:grid-cols-4">
      <Metric
        label="Spent"
        money={totals.spent}
        note={[
          `of ${formatMoney(totals.planned, locale)} planned`,
          pendingText(totals.pending, locale),
        ]
          .filter(Boolean)
          .join(", ")}
        progress={{
          label: "Spent against the plan",
          value: progressValue(totals.spent.amount, totals.planned.amount),
          tone: statusTone(
            totalStatus(totals.spent.amount, totals.planned.amount),
          ),
        }}
      />
      <Metric
        className={totals.remaining.amount < 0 ? "text-destructive" : ""}
        label={totals.remaining.amount < 0 ? "Over plan" : "Left"}
        money={{
          ...totals.remaining,
          amount: Math.abs(totals.remaining.amount),
        }}
        note={
          left === undefined
            ? undefined
            : `${left} ${left === 1 ? "day" : "days"} left`
        }
      />
      <Metric
        label="Income"
        money={income.actual}
        note={
          plan && plan.expectedIncome.amount > 0
            ? `of ${formatMoney(plan.expectedIncome, locale)} expected`
            : undefined
        }
      />
      <Metric
        className={savings.saved.amount < 0 ? "text-destructive" : ""}
        label="Saved"
        money={savings.saved}
        note={
          goal.amount > 0
            ? `of ${formatMoney(goal, locale)} goal`
            : "No savings goal"
        }
        progress={
          goal.amount > 0
            ? {
                label: "Saved against the goal",
                value: progressValue(savings.saved.amount, goal.amount),
                tone: statusTone("under"),
              }
            : undefined
        }
      />
    </div>
  );
}

function Lines({
  budget,
  monthName,
  onShowCategory,
}: {
  budget: BudgetMonth;
  monthName: string;
  onShowCategory: (categoryId: string | null) => void;
}) {
  const planned = budget.lines.filter((line) => line.status !== "unplanned");
  const unplanned = budget.lines.filter((line) => line.status === "unplanned");
  const uncategorized = budget.uncategorized;
  if (!budget.lines.length && uncategorized.spent.amount === 0) {
    return budget.plan ? (
      <p className="text-muted-foreground text-sm">
        The plan has no categories yet, and nothing was spent in {monthName}.
      </p>
    ) : null;
  }
  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle>{budget.plan ? "Categories" : "Spending"}</CardTitle>
      </CardHeader>
      <CardContent className="grid gap-4">
        {planned.length ? (
          <ul className="grid gap-3">
            {planned.map((line) => (
              <LineRow
                key={line.categoryId}
                line={line}
                monthName={monthName}
                onShowCategory={onShowCategory}
              />
            ))}
          </ul>
        ) : null}
        {unplanned.length ? (
          <div className="grid gap-2">
            {budget.plan ? (
              <h3 className="text-muted-foreground text-xs font-medium">
                Not in the plan
              </h3>
            ) : null}
            <ul className="grid gap-3">
              {unplanned.map((line) => (
                <LineRow
                  key={line.categoryId}
                  line={line}
                  monthName={monthName}
                  onShowCategory={onShowCategory}
                />
              ))}
            </ul>
          </div>
        ) : null}
        {uncategorized.spent.amount > 0 ? (
          <div
            className="flex flex-wrap items-baseline gap-x-2 gap-y-1 border-t pt-3"
            data-testid="finance-budget-uncategorized"
          >
            <button
              className="focus-visible:ring-ring/50 rounded-sm font-medium outline-none hover:underline focus-visible:ring-3"
              onClick={() => onShowCategory(null)}
              title={`Show the uncategorized transactions of ${monthName}`}
              type="button"
            >
              Uncategorized
            </button>
            <span className="text-muted-foreground text-xs">
              {uncategorized.transactionCount}{" "}
              {uncategorized.transactionCount === 1
                ? "transaction"
                : "transactions"}{" "}
              without a category count as spending
            </span>
            <Amount
              className="ml-auto font-medium"
              money={uncategorized.spent}
            />
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}

function LineRow({
  line,
  monthName,
  onShowCategory,
}: {
  line: BudgetLine;
  monthName: string;
  onShowCategory: (categoryId: string | null) => void;
}) {
  const locale = useBrowserLocale();
  const pending = pendingText(line.pending, locale);
  return (
    <li className="grid gap-1" data-testid="finance-budget-line">
      <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-1">
        <button
          className="focus-visible:ring-ring/50 flex min-w-0 items-center gap-1.5 rounded-sm text-left outline-none hover:underline focus-visible:ring-3"
          onClick={() => onShowCategory(line.categoryId)}
          title={`Show ${line.categoryName}'s transactions in ${monthName}`}
          type="button"
        >
          <span
            aria-hidden="true"
            className="bg-muted-foreground/40 size-2 shrink-0 rounded-full"
            style={
              line.categoryColor
                ? { backgroundColor: line.categoryColor }
                : undefined
            }
          />
          <span className="truncate font-medium">{line.categoryName}</span>
        </button>
        {line.archived ? <Badge variant="outline">Archived</Badge> : null}
        {line.status === "over" ? (
          <Badge variant="destructive">Over budget</Badge>
        ) : null}
        <span className="ml-auto shrink-0 text-sm tabular-nums">
          {spentOfText(line.spent, line.planned, locale)}
        </span>
      </div>
      <Progress
        aria-label={`${line.categoryName}: ${line.percent ?? 0}% of its budget`}
        className={statusTone(line.status)}
        value={progressValue(line.spent.amount, line.planned.amount)}
      />
      <p
        className={cn(
          "text-muted-foreground flex flex-wrap gap-x-2 text-xs",
          line.status === "over" && "text-destructive",
        )}
      >
        <span>{remainingText(line.remaining, locale, line.status)}</span>
        {line.percent !== null ? <span>{line.percent}%</span> : null}
        {pending ? <span>{pending}</span> : null}
      </p>
    </li>
  );
}

function Notes({ budget }: { budget: BudgetMonth }) {
  const locale = useBrowserLocale();
  const source = planSourceText(budget);
  const excluded = excludedText(budget);
  const unallocated = budget.plan?.unallocated;
  const { movedToSavings, leftover } = budget.savings;
  return (
    <div className="text-muted-foreground grid gap-1 text-xs">
      {source ? <p>{source}</p> : null}
      {unallocated ? (
        <p data-testid="finance-budget-unallocated">
          {unallocated.amount < 0
            ? `The plan spends ${formatMoney({ ...unallocated, amount: -unallocated.amount }, locale)} more than the expected income.`
            : `Not planned yet: ${formatMoney(unallocated, locale)} of the expected income.`}
        </p>
      ) : null}
      {movedToSavings.amount !== 0 ? (
        <p className="flex items-center gap-1">
          <PiggyBank aria-hidden="true" className="size-3.5" />
          <Amount money={movedToSavings} /> moved to Savings,{" "}
          <Amount money={leftover} /> left over.
        </p>
      ) : null}
      <p>
        Spending counts booked and pending money out in {budget.currency};
        refunds lower it. Transfers, Savings, and income are not spending.
      </p>
      {excluded ? <p>{excluded}</p> : null}
    </div>
  );
}

function Metric({
  className,
  label,
  money,
  note,
  progress,
}: {
  className?: string;
  label: string;
  money: Money;
  note?: string;
  progress?: { label: string; value: number; tone: string };
}) {
  return (
    <Card size="sm">
      <CardContent className="grid gap-1">
        <p className="text-muted-foreground text-xs font-medium">{label}</p>
        <Amount
          className={cn(
            "block text-lg font-semibold tracking-tight wrap-anywhere sm:text-xl",
            className,
          )}
          money={money}
        />
        {progress ? (
          <Progress
            aria-label={progress.label}
            className={progress.tone}
            value={progress.value}
          />
        ) : null}
        {note ? <p className="text-muted-foreground text-xs">{note}</p> : null}
      </CardContent>
    </Card>
  );
}
