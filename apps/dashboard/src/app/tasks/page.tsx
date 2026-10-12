import { TasksWorkspace } from "@/components/tasks-workspace";
import { getTasksWorkspace, parseTaskParam } from "@/lib/tasks-workspace";
import { dayKey } from "@/lib/time-zone";
import { userTimeZone } from "@/lib/user-time-zone";

export const dynamic = "force-dynamic";

export default async function TasksPage({ searchParams }: PageProps<"/tasks">) {
  // The server decides the timezone and today once, and the client component
  // renders from these props, so hydration matches the server HTML whatever
  // the browser's language is. The key starts the workspace over (selected
  // day, month) when TimeZoneSync re-renders the page in the browser's zone,
  // or when a link names another Task: `?task=<id>`, as a timed reminder's
  // notification links, scrolls to that Task and highlights it.
  const focus = parseTaskParam((await searchParams).task);
  const timeZone = await userTimeZone();
  const today = dayKey(new Date(), timeZone);
  return (
    <TasksWorkspace
      key={`${timeZone}:${focus ?? ""}`}
      state={await getTasksWorkspace({ today, timeZone, focus })}
      timeZone={timeZone}
      today={today}
    />
  );
}
