import { type NextRequest } from "next/server";

import { serviceBaseUrls } from "@/config/services";
import { proxyServiceRequest } from "@/lib/service-proxy";

type Context = { params: Promise<{ path: string[] }> };

/**
 * The banking integration (`/api/integrations/banking`, like the gateway's
 * route). Besides the owner's actor it passes on the browser's user agent
 * and language and the address the gateway saw (X-Real-IP): with them the
 * bank counts a "Sync now" as made by the owner, not against its daily
 * limit.
 */
const PSU_HEADERS = ["user-agent", "accept-language", "x-real-ip"] as const;

function proxy(request: NextRequest, context: Context) {
  return proxyServiceRequest(request, context, serviceBaseUrls.banking, {
    forwardHeaders: PSU_HEADERS,
  });
}

export const GET = proxy;
export const POST = proxy;
export const DELETE = proxy;
export const HEAD = proxy;
export const OPTIONS = proxy;
