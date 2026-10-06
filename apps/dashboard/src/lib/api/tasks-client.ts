import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import type { paths } from "@/lib/api/generated/tasks";

export function createTasksClient(baseUrl = serviceBaseUrls.tasks) {
  return createClient<paths>({ baseUrl });
}

export const tasksClient = createTasksClient();
