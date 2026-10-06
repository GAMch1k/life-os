import createClient from "openapi-fetch";

import { serviceBaseUrls } from "@/config/services";
import type { paths } from "@/lib/api/generated/calendar";

export function createCalendarClient(baseUrl = serviceBaseUrls.calendar) {
  return createClient<paths>({ baseUrl });
}

export const calendarClient = createCalendarClient();
