import type { Metadata } from "next";

import { SettingsNav } from "@/components/security-log";
import { TokensSettings } from "@/components/tokens-settings";
import { listTokens } from "@/lib/auth/account-credentials";
import { accountPage } from "@/lib/auth/page-context";
import { recentlyAuthenticated } from "@/lib/auth/reauth";
import { userTimeZone } from "@/lib/user-time-zone";

export const metadata: Metadata = { title: "API tokens · Life OS" };
export const dynamic = "force-dynamic";

/**
 * Settings → API tokens (docs/install/accounts.md#api-tokens): tokens for
 * scripts and development tools, each with a name, scopes, and an expiry,
 * shown once. Only with a real sign-in.
 */
export default async function TokensPage() {
  const { rt, principal, admin } = await accountPage("/settings/tokens");
  const [tokens, timeZone, recentAuth] = await Promise.all([
    listTokens(rt, principal.userId),
    userTimeZone(),
    recentlyAuthenticated(rt.pool, {
      id: principal.sessionId,
      createdAt: principal.sessionCreatedAt,
    }),
  ]);
  return (
    <TokensSettings
      nav={<SettingsNav admin={admin} current="/settings/tokens" />}
      publicUrl={rt.env.publicUrl}
      recentAuth={recentAuth}
      timeZone={timeZone}
      tokens={tokens}
    />
  );
}
