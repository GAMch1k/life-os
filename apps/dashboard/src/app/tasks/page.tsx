import { TasksWorkspace } from "@/components/tasks-workspace";
import { getTasksWorkspace } from "@/lib/tasks-workspace";
export const dynamic = "force-dynamic";
export default async function TasksPage() {
  return <TasksWorkspace state={await getTasksWorkspace()} />;
}
