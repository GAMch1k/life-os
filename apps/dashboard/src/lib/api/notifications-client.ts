import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import { OWNER_HEADERS } from "@/lib/api/actor";
import type { paths } from "@/lib/api/generated/notifications";

export function createNotificationsClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.notifications
    : "/api/notifications",
) {
  return createClient<paths>({ baseUrl, headers: OWNER_HEADERS });
}

export const notificationsClient = createNotificationsClient();
