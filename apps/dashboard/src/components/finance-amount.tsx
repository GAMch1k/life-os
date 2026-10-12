"use client";

import { type Money, balanceBreakdown, formatMoney } from "@/lib/finance-money";
import { useBrowserLocale } from "@/lib/use-browser-locale";
import { cn } from "@/lib/utils";

/**
 * An amount in its currency, formatted the browser's way. `signed` marks
 * money in with "+" and colors it, for transaction lists; money out keeps
 * its "-".
 */
export function Amount({
  className,
  money,
  signed = false,
}: {
  className?: string;
  money: Money;
  signed?: boolean;
}) {
  const locale = useBrowserLocale();
  return (
    <span
      className={cn(
        "tabular-nums",
        signed && money.amount > 0 && "text-success",
        className,
      )}
    >
      {formatMoney(money, locale, { signed })}
    </span>
  );
}

/**
 * The quiet line under a balance that has pending money: what is booked and
 * what is pending ("Booked €251.05 · Pending -€145.35"). Nothing when no
 * money is pending.
 */
export function BalanceBreakdown({
  booked,
  className,
  pending,
}: {
  booked: Money;
  className?: string;
  pending: Money;
}) {
  const locale = useBrowserLocale();
  const text = balanceBreakdown(booked, pending, locale);
  if (!text) return null;
  return (
    <span
      className={cn(
        "text-muted-foreground block text-xs tabular-nums",
        className,
      )}
      data-testid="balance-breakdown"
    >
      {text}
    </span>
  );
}
