import type { NextRequest } from "next/server";

import { serviceBaseUrls } from "@/config/services";
import { backupsHandler } from "@/lib/admin-surfaces";
import { authRuntime } from "@/lib/auth/runtime";
import { proxyServiceRequest } from "@/lib/service-proxy";

type Context = { params: Promise<{ path: string[] }> };

/**
 * The backup service (docs/services/backup.md) has no gateway route; the
 * Status page's "Back up now" reaches it here. Only reading its status and
 * starting a backup of every database pass, for the server's admins
 * (src/lib/admin-surfaces.ts), with their identity token and actor; restore
 * checks and anything else stay on the private network.
 */
async function proxy(request: NextRequest, context: Context) {
  const { path } = await context.params;
  return backupsHandler(authRuntime(), request, path.join("/"), () =>
    proxyServiceRequest(request, context, serviceBaseUrls.backup),
  );
}

export const GET = proxy;
export const POST = proxy;
