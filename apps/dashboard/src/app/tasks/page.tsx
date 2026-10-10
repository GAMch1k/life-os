import { TasksWorkspace } from "@/components/tasks-workspace";
import { getTasksWorkspace } from "@/lib/tasks-workspace";
import { dayKey, userTimeZone } from "@/lib/time-zone";

export const dynamic = "force-dynamic";

export default async function TasksPage() {
  // The server decides the owner's timezone and today once, and the client
  // component renders from these props, so hydration matches the server HTML
  // whatever the browser's own timezone or language is.
  const timeZone = userTimeZone();
  return (
    <TasksWorkspace
      state={await getTasksWorkspace()}
      timeZone={timeZone}
      today={dayKey(new Date(), timeZone)}
    />
  );
}
