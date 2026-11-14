import type { Metadata } from "next";

import { SettingsNav } from "@/components/security-log";
import { TokensSettings } from "@/components/tokens-settings";
import { listTokens } from "@/lib/auth/account-credentials";
import { accountPage } from "@/lib/auth/page-context";
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
  const [tokens, timeZone] = await Promise.all([
    listTokens(rt, principal.userId),
    userTimeZone(),
  ]);
  return (
    <TokensSettings
      nav={<SettingsNav admin={admin} current="/settings/tokens" />}
      publicUrl={rt.env.publicUrl}
      timeZone={timeZone}
      tokens={tokens}
    />
  );
}
