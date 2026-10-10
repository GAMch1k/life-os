import { TasksWorkspace } from "@/components/tasks-workspace";
import { getTasksWorkspace } from "@/lib/tasks-workspace";
import { dayKey } from "@/lib/time-zone";
import { userTimeZone } from "@/lib/user-time-zone";

export const dynamic = "force-dynamic";

export default async function TasksPage() {
  // The server decides the timezone and today once, and the client component
  // renders from these props, so hydration matches the server HTML whatever
  // the browser's language is. The key starts the workspace over (selected
  // day, month) when TimeZoneSync re-renders the page in the browser's zone.
  const timeZone = await userTimeZone();
  return (
    <TasksWorkspace
      key={timeZone}
      state={await getTasksWorkspace()}
      timeZone={timeZone}
      today={dayKey(new Date(), timeZone)}
    />
  );
}
