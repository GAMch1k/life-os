import type { Metadata } from "next";

import { ConnectedApps } from "@/components/connected-apps";
import { SettingsNav } from "@/components/security-log";
import { accountPage } from "@/lib/auth/page-context";
import { listConnectedApps } from "@/lib/auth/oauth";
import { userTimeZone } from "@/lib/user-time-zone";

export const metadata: Metadata = { title: "Connected apps · Life OS" };
export const dynamic = "force-dynamic";

/**
 * Settings → Connected apps (docs/install/accounts.md#connected-apps): the
 * MCP clients (AI assistants and their tools) you let use Life OS, and
 * disconnecting one, which counts at once. Only with a real sign-in.
 */
export default async function ConnectedAppsPage() {
  const { rt, principal, admin } = await accountPage(
    "/settings/connected-apps",
  );
  const [apps, timeZone] = await Promise.all([
    listConnectedApps(rt.pool, principal.userId),
    userTimeZone(),
  ]);
  return (
    <ConnectedApps
      apps={apps}
      mcpUrl={`${rt.env.publicUrl}/api/mcp/mcp`}
      nav={<SettingsNav admin={admin} current="/settings/connected-apps" />}
      timeZone={timeZone}
    />
  );
}
