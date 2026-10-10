import { type NextRequest } from "next/server";

import { serviceBaseUrls } from "@/config/services";
import { proxyServiceRequest } from "@/lib/service-proxy";

type Context = { params: Promise<{ path: string[] }> };

function proxy(request: NextRequest, context: Context) {
  return proxyServiceRequest(request, context, serviceBaseUrls.notifications);
}

export const GET = proxy;
export const POST = proxy;
export const HEAD = proxy;
export const OPTIONS = proxy;
