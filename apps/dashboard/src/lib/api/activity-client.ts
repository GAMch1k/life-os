import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import type { paths } from "@/lib/api/generated/activity";

export function createActivityClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.activity
    : "/api/activity",
) {
  return createClient<paths>({ baseUrl });
}

export const activityClient = createActivityClient();
