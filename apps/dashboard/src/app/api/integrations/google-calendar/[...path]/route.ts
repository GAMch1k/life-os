import { type NextRequest } from "next/server";

import { serviceBaseUrls } from "@/config/services";
import { proxyServiceRequest } from "@/lib/service-proxy";

type Context = { params: Promise<{ path: string[] }> };

/**
 * The Google Calendar integration (`/api/integrations/google-calendar`,
 * like the gateway's route): the Google connection, the calendar list, and
 * where Life OS events are exported in Google.
 */
function proxy(request: NextRequest, context: Context) {
  return proxyServiceRequest(request, context, serviceBaseUrls.googleCalendar);
}

export const GET = proxy;
export const POST = proxy;
export const PATCH = proxy;
export const DELETE = proxy;
export const HEAD = proxy;
export const OPTIONS = proxy;
