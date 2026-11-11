import { type NextRequest, NextResponse } from "next/server";

import { moduleOffResponse, serviceOn } from "@/lib/modules";
import { processorStatusUrl } from "@/lib/runtime-status";
import { proxyServiceRequest } from "@/lib/service-proxy";

type Context = { params: Promise<{ path: string[] }> };

/**
 * The Inbox processor has no gateway route; "What can I cook?" reaches its
 * recipe ideas job here (docs/services/mcp.md): asking for ideas now, with
 * the owner's actor (the browser sends it), and reading whether a run is
 * waiting, running, or done. Nothing else of the processor is passed on.
 */
const ALLOWED = new Set(["GET ideas", "POST ideas/requests"]);

async function proxy(request: NextRequest, context: Context) {
  // Without AI processing, no processor to ask (and no name to resolve).
  if (!serviceOn("processor")) return moduleOffResponse();
  const { path } = await context.params;
  if (!ALLOWED.has(`${request.method} ${path.join("/")}`)) {
    return NextResponse.json(
      { error: { code: "not_found", message: "Not Found" } },
      { status: 404 },
    );
  }
  return proxyServiceRequest(request, context, processorStatusUrl);
}

export const GET = proxy;
export const POST = proxy;
