import type { Metadata } from "next";

import { OAuthConsent } from "@/components/oauth-consent";
import { consentRequest, queryOf } from "@/lib/auth/oauth-pages";
import { accountPage } from "@/lib/auth/page-context";

export const metadata: Metadata = { title: "Connect an app · Life OS" };
export const dynamic = "force-dynamic";

/**
 * The OAuth consent page for MCP clients (docs/services/mcp.md#signing-in-with-oauth):
 * Better Auth's OAuth provider sends a signed-in person here with the
 * client's signed request; they allow reading, or reading and changing,
 * Life OS through MCP, or deny it. Only with a real sign-in.
 */
export default async function ConsentPage({
  searchParams,
}: PageProps<"/oauth/consent">) {
  const params = await searchParams;
  const { rt, principal } = await accountPage(
    `/oauth/consent?${queryOf(params)}`,
  );
  const request = await consentRequest(rt, params);
  const { rows } = await rt.pool.query<{
    username: string | null;
    name: string;
  }>('SELECT "displayUsername" AS username, name FROM "user" WHERE id = $1', [
    principal.userId,
  ]);
  const user = rows[0];
  return (
    <OAuthConsent
      request={request}
      username={user?.username ?? user?.name ?? ""}
    />
  );
}
