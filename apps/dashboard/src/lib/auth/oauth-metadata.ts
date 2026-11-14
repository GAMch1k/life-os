import { oauthProviderAuthServerMetadata } from "@better-auth/oauth-provider";

import type { CheckRuntime } from "./check";

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
