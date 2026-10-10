import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import { OWNER_HEADERS } from "@/lib/api/actor";
import type { paths } from "@/lib/api/generated/tasks";

export function createTasksClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.tasks
    : "/api/tasks",
) {
  return createClient<paths>({ baseUrl, headers: OWNER_HEADERS });
}

export const tasksClient = createTasksClient();
