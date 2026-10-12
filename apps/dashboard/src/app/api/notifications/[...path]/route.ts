import { type NextRequest } from "next/server";

import { serviceBaseUrls } from "@/config/services";
import { proxyServiceRequest } from "@/lib/service-proxy";

type Context = { params: Promise<{ path: string[] }> };

// The user agent names a device that turns on web push ("Chrome on
// Android"); the gateway passes it on as well.
const FORWARDED = ["user-agent"] as const;

function proxy(request: NextRequest, context: Context) {
  return proxyServiceRequest(request, context, serviceBaseUrls.notifications, {
    forwardHeaders: FORWARDED,
  });
}

export const GET = proxy;
export const POST = proxy;
export const DELETE = proxy;
export const HEAD = proxy;
export const OPTIONS = proxy;
