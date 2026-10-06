import { tasksClient } from "@/lib/api/tasks-client";
import type { components } from "@/lib/api/generated/tasks";

export type Task = components["schemas"]["Task"];

export type TasksSummaryState =
  | { kind: "ready"; tasks: Task[] }
  | { kind: "empty" }
  | { kind: "unavailable" };

function endOfToday() {
  const date = new Date();
  date.setUTCHours(23, 59, 59, 999);
  return date.toISOString();
}

export async function getTasksSummary(): Promise<TasksSummaryState> {
  const { data, error } = await tasksClient.GET("/v1/tasks", {
    params: { query: { dueBefore: endOfToday(), limit: 20 } },
  });

  if (error || !data) {
    return { kind: "unavailable" };
  }
  if (data.items.length === 0) {
    return { kind: "empty" };
  }

  return { kind: "ready", tasks: data.items };
}
