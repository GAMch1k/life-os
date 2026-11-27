import type { Metadata } from "next";
import { redirect } from "next/navigation";

import { SettingsNav } from "@/components/security-log";
import { SecuritySettings } from "@/components/security-settings";
import { accountInfo, listSessions } from "@/lib/auth/account";
import { hasAuthenticator } from "@/lib/auth/email-account";
import { emailSettings } from "@/lib/auth/email-routes";
import { currentCheck } from "@/lib/auth/page-context";
import { isAdminRole } from "@/lib/auth/roles";
import { accountCount } from "@/lib/auth/store";
import { userTimeZone } from "@/lib/user-time-zone";

export const metadata: Metadata = { title: "Security · Life OS" };
export const dynamic = "force-dynamic";

/**
 * Settings → Security: the password, two-factor sign-in (an authenticator
 * app with recovery codes, sign-in codes by email while email works,
 * passkeys), the sessions signed in, and, for the owner, legacy access. Only with a real sign-in: the proxy sends legacy
 * access to the login page.
 */
export default async function SecurityPage({
  searchParams,
}: PageProps<"/settings/security">) {
  const { rt, result } = await currentCheck();
  if (result.status !== 204 || result.principal.via !== "session") {
    redirect("/login?next=%2Fsettings%2Fsecurity");
  }
  const principal = result.principal;
  const [info, sessions, passkeys, totp, accounts, email] = await Promise.all([
    accountInfo(rt, principal),
    listSessions(rt, principal),
    rt.pool.query<{ id: string; name: string | null; createdAt: Date | null }>(
      'SELECT id, name, "createdAt" FROM passkey WHERE "userId" = $1 ORDER BY "createdAt"',
      [principal.userId],
    ),
    // The authenticator app: verified, not only "twoFactorEnabled", which
    // sign-in codes by email turn on too.
    hasAuthenticator(rt.pool, principal.userId),
    accountCount(rt.pool),
    emailSettings(rt, principal.userId),
  ]);
  const params = await searchParams;
  const timeZone = await userTimeZone();
  return (
    <SecuritySettings
      accounts={accounts}
      info={info}
      nav={
        <SettingsNav
          admin={isAdminRole(principal.role)}
          current="/settings/security"
        />
      }
      passkeyOrigin={new URL(rt.env.publicUrl).origin}
      passkeys={passkeys.rows.map((row) => ({
        id: row.id,
        name: row.name,
        createdAt: row.createdAt?.toISOString() ?? null,
      }))}
      publicUrl={rt.env.publicUrl}
      sessions={sessions}
      timeZone={timeZone}
      totpEnabled={totp}
      email={
        email.enabled
          ? {
              address: email.address,
              verified: email.verified,
              signInCodes: email.signInCodes,
            }
          : null
      }
      welcome={params.welcome === "1"}
    />
  );
}
