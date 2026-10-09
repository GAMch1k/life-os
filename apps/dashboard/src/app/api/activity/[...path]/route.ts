import { type NextRequest } from "next/server";

import { serviceBaseUrls } from "@/config/services";
import { proxyServiceRequest } from "@/lib/service-proxy";

type Context = { params: Promise<{ path: string[] }> };
// Activity is recorded by the owning services on the private network; like the
// gateway, the Dashboard only reads it.
function proxy(request: NextRequest, context: Context) {
  return proxyServiceRequest(request, context, serviceBaseUrls.activity);
}
export const GET = proxy;
export const HEAD = proxy;
