import { type NextRequest } from "next/server";

import { serviceBaseUrls } from "@/config/services";
import { moduleOffResponse, serviceOn } from "@/lib/modules";
import { proxyServiceRequest } from "@/lib/service-proxy";

type Context = { params: Promise<{ path: string[] }> };
function proxy(request: NextRequest, context: Context) {
  if (!serviceOn("voice")) return moduleOffResponse();
  return proxyServiceRequest(request, context, serviceBaseUrls.voice);
}
export const GET = proxy;
export const POST = proxy;
export const PATCH = proxy;
export const PUT = proxy;
export const DELETE = proxy;
export const HEAD = proxy;
export const OPTIONS = proxy;
