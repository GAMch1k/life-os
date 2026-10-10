import { FinanceWorkspace } from "@/components/finance-workspace";
import { currentMonth } from "@/lib/finance-summary";
import { getFinanceWorkspace } from "@/lib/finance-workspace";
import { dayKey } from "@/lib/time-zone";
import { userTimeZone } from "@/lib/user-time-zone";

export const dynamic = "force-dynamic";

export default async function FinancePage() {
  // The server decides today and the month once, in the owner's timezone,
  // and the client workspace renders from these props, so hydration matches
  // the server HTML. The key starts the workspace over when TimeZoneSync
  // re-renders the page in the browser's zone.
  const timeZone = await userTimeZone();
  const now = new Date();
  const month = currentMonth(now, timeZone);
  return (
    <FinanceWorkspace
      key={timeZone}
      month={month}
      state={await getFinanceWorkspace(month)}
      timeZone={timeZone}
      today={dayKey(now, timeZone)}
    />
  );
}
