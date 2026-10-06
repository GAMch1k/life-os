import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import type { paths } from "@/lib/api/generated/tasks";

export function createTasksClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.tasks
    : "/api/tasks",
) {
  return createClient<paths>({ baseUrl });
}

export const tasksClient = createTasksClient();
