import Link from "next/link";
import { ArrowRight, Wallet } from "lucide-react";

import { Amount } from "@/components/finance-amount";
import { Card, CardContent } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import {
  type HomeBudget,
  budgetHref,
  daysLeft,
  monthName,
  progressValue,
  statusTone,
  totalStatus,
} from "@/lib/finance-budget";

/**
 * Home's compact budget card: this month's spending against the plan, the
 * days left, and the categories closest to (or over) their amount. `today`
 * is the owner's day (YYYY-MM-DD).
 */
export function HomeBudgetCard({
  budget,
  today,
}: {
  budget: HomeBudget | null;
  today: string;
}) {
  const month = budget?.month ?? today.slice(0, 7);
  const name = monthName(month);
  const left = daysLeft(month, today);
  return (
    <Card
      aria-labelledby="home-budget-title"
      id="home-budget"
      role="region"
      size="sm"
    >
      <CardContent className="space-y-1.5">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <Wallet aria-hidden="true" className="size-4" />
          <h2 className="text-sm font-semibold" id="home-budget-title">
            {name} budget
          </h2>
          <Link
            className="text-muted-foreground hover:text-foreground ml-auto inline-flex items-center gap-1 text-xs"
            href={budgetHref()}
          >
            {budget?.kind === "none" ? "Plan your month" : "Open budget"}{" "}
            <ArrowRight aria-hidden="true" className="size-3" />
          </Link>
        </div>
        {budget === null ? (
          <p className="text-muted-foreground">
            Finance is unavailable right now.
          </p>
        ) : budget.kind === "none" ? (
          <p className="text-muted-foreground">No budget for {name} yet.</p>
        ) : (
          <>
            <p className="text-muted-foreground">
              <Amount
                money={{ amount: budget.spent, currency: budget.currency }}
              />{" "}
              of{" "}
              <Amount
                money={{ amount: budget.planned, currency: budget.currency }}
              />{" "}
              spent
              {left !== undefined
                ? ` · ${left} ${left === 1 ? "day" : "days"} left`
                : ""}
            </p>
            <Progress
              aria-label="Spent against the plan"
              className={statusTone(totalStatus(budget.spent, budget.planned))}
              value={progressValue(budget.spent, budget.planned)}
            />
            {budget.closest.length ? (
              <ul className="space-y-1">
                {budget.closest.map((line) => (
                  <li
                    className="flex items-center justify-between gap-3"
                    key={line.categoryId}
                  >
                    <span className="truncate">{line.categoryName}</span>
                    <span
                      className={
                        line.status === "over"
                          ? "text-destructive shrink-0 text-xs tabular-nums"
                          : "text-muted-foreground shrink-0 text-xs tabular-nums"
                      }
                    >
                      {line.percent ?? 0}%
                    </span>
                  </li>
                ))}
              </ul>
            ) : null}
            {budget.uncategorized > 0 ? (
              <p className="text-muted-foreground text-xs">
                <Amount
                  money={{
                    amount: budget.uncategorized,
                    currency: budget.currency,
                  }}
                />{" "}
                without a category
              </p>
            ) : null}
          </>
        )}
      </CardContent>
    </Card>
  );
}
