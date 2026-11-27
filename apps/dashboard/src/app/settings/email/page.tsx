import type { Metadata } from "next";
import { notFound } from "next/navigation";

import { EmailSettings } from "@/components/email-settings";
import { SettingsNav } from "@/components/security-log";
import { emailSettings } from "@/lib/auth/email-routes";
import { accountPage } from "@/lib/auth/page-context";
import { recentlyAuthenticated } from "@/lib/auth/reauth";

export const metadata: Metadata = { title: "Email · Life OS" };
export const dynamic = "force-dynamic";

/**
 * Settings → Email (docs/install/email.md): the account's address and its
 * verification, and the person's own switches for the security alerts.
 * Only with a real sign-in, and only while email works on this server.
 * Each person's own: nobody, an admin included, changes another's.
 */
export default async function EmailPage() {
  const { rt, principal, admin } = await accountPage("/settings/email");
  const [settings, recentAuth] = await Promise.all([
    emailSettings(rt, principal.userId),
    recentlyAuthenticated(rt.pool, {
      id: principal.sessionId,
      createdAt: principal.sessionCreatedAt,
    }),
  ]);
  if (!settings.enabled) notFound();
  return (
    <EmailSettings
      address={settings.address}
      alerts={settings.alerts}
      nav={<SettingsNav admin={admin} current="/settings/email" />}
      recentAuth={recentAuth}
      signInCodes={settings.signInCodes}
      verified={settings.verified}
    />
  );
}
