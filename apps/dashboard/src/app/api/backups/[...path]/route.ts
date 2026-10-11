import { type NextRequest, NextResponse } from "next/server";

import { serviceBaseUrls } from "@/config/services";
import { proxyServiceRequest } from "@/lib/service-proxy";

type Context = { params: Promise<{ path: string[] }> };

/**
 * The backup service (docs/services/backup.md) has no gateway route; the
 * Status page's "Back up now" reaches it here. Only reading its status and
 * starting a backup of every database pass, with the owner's actor (the
 * browser's own client sends it); restore checks and anything else stay on
 * the private network.
 */
const ALLOWED = new Set(["GET v1/status", "POST v1/backups"]);

async function proxy(request: NextRequest, context: Context) {
  const { path } = await context.params;
  if (!ALLOWED.has(`${request.method} ${path.join("/")}`)) {
    return NextResponse.json(
      { error: { code: "not_found", message: "Not Found" } },
      { status: 404 },
    );
  }
  return proxyServiceRequest(request, context, serviceBaseUrls.backup);
}

export const GET = proxy;
export const POST = proxy;
