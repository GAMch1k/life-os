import { protectedResourceHandler } from "@/lib/auth/oauth-metadata";
import { authRuntime } from "@/lib/auth/runtime";
import { serviceOn } from "@/lib/modules";

// MCP's OAuth protected resource metadata (RFC 9728), at
// /.well-known/oauth-protected-resource/api/mcp (and the root form): the
// resource <public URL>/api/mcp, its authorization server, and the scopes.
// Public: an MCP client reads it before it signs in. 404 while the ai
// module (MCP) is off.
export const dynamic = "force-dynamic";

export function GET(request: Request) {
  // The ai module runs MCP and the processor together.
  return protectedResourceHandler(
    authRuntime(),
    request,
    serviceOn("processor"),
  );
}
