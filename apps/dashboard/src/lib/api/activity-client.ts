import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import { OWNER_HEADERS } from "@/lib/api/actor";
import type { paths } from "@/lib/api/generated/activity";

export function createActivityClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.activity
    : "/api/activity",
) {
  return createClient<paths>({ baseUrl, headers: OWNER_HEADERS });
}

export const activityClient = createActivityClient();
