import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import { OWNER_HEADERS } from "@/lib/api/actor";
import type { paths } from "@/lib/api/generated/calendar";

export function createCalendarClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.calendar
    : "/api/calendar",
) {
  return createClient<paths>({ baseUrl, headers: OWNER_HEADERS });
}

export const calendarClient = createCalendarClient();
