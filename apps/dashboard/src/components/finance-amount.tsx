"use client";

import { type Money, formatMoney } from "@/lib/finance-money";
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
