import { type NextRequest } from "next/server";

import { serviceBaseUrls } from "@/config/services";
import { proxyServiceRequest } from "@/lib/service-proxy";

type Context = { params: Promise<{ path: string[] }> };

/**
 * The calendar-feeds integration (`/api/integrations/calendar-feeds`, like
 * the gateway's route): the feeds' URL, filter, preset, and refresh state.
 * A feed's name, enabled, default reminders, and default priority are its
 * Calendar source's, under `/api/calendar/v1/sources`.
 */
function proxy(request: NextRequest, context: Context) {
  return proxyServiceRequest(request, context, serviceBaseUrls.calendarFeeds);
}

export const GET = proxy;
export const POST = proxy;
export const PATCH = proxy;
export const DELETE = proxy;
export const HEAD = proxy;
export const OPTIONS = proxy;
