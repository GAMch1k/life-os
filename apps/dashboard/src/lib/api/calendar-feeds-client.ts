import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import { OWNER_HEADERS } from "@/lib/api/actor";
import type { paths } from "@/lib/api/generated/calendar-feeds";

/** The browser reaches the calendar-feeds integration under the integrations prefix. */
export const CALENDAR_FEEDS_PROXY_PATH = "/api/integrations/calendar-feeds";

export function createCalendarFeedsClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.calendarFeeds
    : CALENDAR_FEEDS_PROXY_PATH,
) {
  return createClient<paths>({ baseUrl, headers: OWNER_HEADERS });
}

export const calendarFeedsClient = createCalendarFeedsClient();
