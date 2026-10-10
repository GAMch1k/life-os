import { CalendarWorkspace } from "@/components/calendar-workspace";
import { parseDay, parseView } from "@/lib/calendar-workspace";
import { dayKey } from "@/lib/time-zone";
import { userTimeZone } from "@/lib/user-time-zone";

export const dynamic = "force-dynamic";

export default async function CalendarPage({
  searchParams,
}: PageProps<"/calendar">) {
  // Events load in the browser, for the range FullCalendar shows. The view
  // and date come from the URL the workspace keeps up to date; the key starts
  // it over when TimeZoneSync re-renders the page in the browser's zone.
  const params = await searchParams;
  const timeZone = await userTimeZone();
  return (
    <CalendarWorkspace
      initialDate={parseDay(params.date)}
      initialView={parseView(params.view)}
      key={timeZone}
      timeZone={timeZone}
      today={dayKey(new Date(), timeZone)}
    />
  );
}
