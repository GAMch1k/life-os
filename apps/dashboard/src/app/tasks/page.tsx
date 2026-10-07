import { TasksSummary } from "@/components/tasks-summary";
import { getTasksSummary } from "@/lib/tasks-summary";
export const dynamic = "force-dynamic";
export default async function TasksPage() {
  return <TasksSummary state={await getTasksSummary()} />;
}
