import { tasksClient } from "@/lib/api/tasks-client";
import type { Task } from "@/lib/tasks-summary";

export type TasksWorkspaceState =
  | { kind: "ready"; tasks: Task[] }
  | { kind: "empty" }
  | { kind: "unavailable" };

export async function getTasksWorkspace(): Promise<TasksWorkspaceState> {
  const { data, error } = await tasksClient.GET("/v1/tasks", {
    params: { query: { completed: false, limit: 100 } },
  });
  if (error || !data) return { kind: "unavailable" };
  if (data.items.length === 0) return { kind: "empty" };
  return { kind: "ready", tasks: data.items };
}
