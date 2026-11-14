import type { Metadata } from "next";
import { redirect } from "next/navigation";

import { SecuritySettings } from "@/components/security-settings";
import { accountInfo, listSessions } from "@/lib/auth/account";
import { currentCheck } from "@/lib/auth/page-context";
import { accountCount } from "@/lib/auth/store";
import { userTimeZone } from "@/lib/user-time-zone";

export const metadata: Metadata = { title: "Security · Life OS" };
export const dynamic = "force-dynamic";

/**
 * Settings → Security: the password, two-factor sign-in (an authenticator
 * app with recovery codes, passkeys), the sessions signed in, and, for the
 * owner, legacy access. Only with a real sign-in: the proxy sends legacy
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
  const [info, sessions, passkeys, totp, accounts] = await Promise.all([
    accountInfo(rt, principal),
    listSessions(rt, principal),
    rt.pool.query<{ id: string; name: string | null; createdAt: Date | null }>(
      'SELECT id, name, "createdAt" FROM passkey WHERE "userId" = $1 ORDER BY "createdAt"',
      [principal.userId],
    ),
    rt.pool.query<{ enabled: boolean | null }>(
      'SELECT "twoFactorEnabled" AS enabled FROM "user" WHERE id = $1',
      [principal.userId],
    ),
    accountCount(rt.pool),
  ]);
  const params = await searchParams;
  const timeZone = await userTimeZone();
  return (
    <SecuritySettings
      accounts={accounts}
      info={info}
      passkeyOrigin={new URL(rt.env.publicUrl).origin}
      passkeys={passkeys.rows.map((row) => ({
        id: row.id,
        name: row.name,
        createdAt: row.createdAt?.toISOString() ?? null,
      }))}
      publicUrl={rt.env.publicUrl}
      sessions={sessions}
      timeZone={timeZone}
      totpEnabled={totp.rows[0]?.enabled === true}
      welcome={params.welcome === "1"}
    />
  );
}
