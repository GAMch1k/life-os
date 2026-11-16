import { authRuntime } from "@/lib/auth/runtime";
import { oauthMetadataHandler } from "@/lib/auth/oauth-metadata";

// The OAuth authorization server's metadata (RFC 8414) at the site root,
// for MCP clients (docs/services/mcp.md#signing-in-with-oauth). Public:
// the gateway routes it here without the auth check.
export const dynamic = "force-dynamic";

export function GET(request: Request) {
  return oauthMetadataHandler(authRuntime(), request);
}
