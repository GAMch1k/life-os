import { FinanceWorkspace } from "@/components/finance-workspace";
import { parseMonth, parseTab } from "@/lib/finance-budget";
import { currentMonth } from "@/lib/finance-summary";
import { getFinanceWorkspace } from "@/lib/finance-workspace";
import { dayKey } from "@/lib/time-zone";
import { userTimeZone } from "@/lib/user-time-zone";

export const dynamic = "force-dynamic";

export default async function FinancePage({
  searchParams,
}: PageProps<"/finance">) {
  // The server decides today and the month once, in the owner's timezone,
  // and the client workspace renders from these props, so hydration matches
  // the server HTML. The key starts the workspace over when TimeZoneSync
  // re-renders the page in the browser's zone. `?tab=budget&month=YYYY-MM`
  // opens a tab (and the budget's month), as budget notifications link.
  const params = await searchParams;
  const timeZone = await userTimeZone();
  const now = new Date();
  const month = currentMonth(now, timeZone);
  const budgetMonth = parseMonth(params.month) ?? month;
  return (
    <FinanceWorkspace
      budgetMonth={budgetMonth}
      initialTab={parseTab(params.tab)}
      key={timeZone}
      month={month}
      state={await getFinanceWorkspace(month, budgetMonth)}
      today={dayKey(now, timeZone)}
    />
  );
}
