import { oauthProviderAuthServerMetadata } from "@better-auth/oauth-provider";

import type { CheckRuntime } from "./check";
import { MCP_RESOURCE_PATH, OAUTH_SCOPES } from "./constants";

/**
 * GET /.well-known/oauth-protected-resource/api/mcp (RFC 9728): what an MCP
 * client reads after MCP's 401 points it here: the resource <public
 * URL>/api/mcp, the authorization server (the public URL, whose metadata
 * follows at /.well-known/oauth-authorization-server), and the scopes. The
 * root form, /.well-known/oauth-protected-resource, answers the same, for
 * clients that look there. 404 while MCP (the ai module) is off.
 */
export function protectedResourceHandler(
  rt: CheckRuntime,
  request: Request,
  mcpOn: boolean,
): Response {
  const { pathname } = new URL(request.url);
  const known = [
    "/.well-known/oauth-protected-resource",
    `/.well-known/oauth-protected-resource${MCP_RESOURCE_PATH}`,
  ];
  if (!mcpOn || !known.includes(pathname)) {
    return Response.json(
      { error: { code: "not_found", message: "Not found" } },
      { status: 404 },
    );
  }
  return Response.json(
    {
      resource: `${rt.env.publicUrl}${MCP_RESOURCE_PATH}`,
      authorization_servers: [rt.env.publicUrl],
      scopes_supported: OAUTH_SCOPES,
      bearer_methods_supported: ["header"],
      resource_name: "Life OS",
      resource_documentation:
        "https://github.com/GAMch1k/life-os/blob/master/docs/services/mcp.md",
    },
    { headers: { "cache-control": "public, max-age=3600" } },
  );
}

/**
 * GET /.well-known/oauth-authorization-server: Better Auth's OAuth provider
 * serves its metadata only to server code (the trial found), so the
 * Dashboard mounts it at the site root, where RFC 8414 puts it for an
 * issuer without a path (the public URL). Only at the public address: on a
 * plain-HTTP host the owner allowed for signing in, there is no OAuth.
 */
export async function oauthMetadataHandler(
  rt: CheckRuntime,
  request: Request,
): Promise<Response> {
  const response = await oauthProviderAuthServerMetadata(rt.auth)(request);
  const headers = new Headers(response.headers);
  headers.set("cache-control", "public, max-age=3600");
  return new Response(response.body, { status: response.status, headers });
}
