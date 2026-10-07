import { CalendarSummary } from "@/components/calendar-summary";
import { getCalendarSummary } from "@/lib/calendar-summary";
export const dynamic = "force-dynamic";
export default async function CalendarPage() {
  return <CalendarSummary state={await getCalendarSummary()} />;
}
