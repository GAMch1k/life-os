import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import { OWNER_HEADERS } from "@/lib/api/actor";
import type { paths } from "@/lib/api/generated/google-calendar";

/** The browser reaches the Google Calendar integration under the integrations prefix. */
export const GOOGLE_CALENDAR_PROXY_PATH = "/api/integrations/google-calendar";

export function createGoogleCalendarClient(
  baseUrl = typeof window === "undefined"
    ? serviceBaseUrls.googleCalendar
    : GOOGLE_CALENDAR_PROXY_PATH,
) {
  return createClient<paths>({ baseUrl, headers: OWNER_HEADERS });
}

export const googleCalendarClient = createGoogleCalendarClient();
