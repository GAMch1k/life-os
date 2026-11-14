import type { Metadata } from "next";

import { SecurityLogList, SettingsNav } from "@/components/security-log";
import { accountPage } from "@/lib/auth/page-context";
import { listSecurityEvents } from "@/lib/auth/security-log";
import { userTimeZone } from "@/lib/user-time-zone";

export const metadata: Metadata = { title: "Security log · Life OS" };
export const dynamic = "force-dynamic";

/**
 * Settings → Security log: the entries of the security log that concern
 * this account (docs/architecture/multi-user.md#logs): its sign-ins and
 * failed ones, changes to its sign-in, and what an admin or the server
 * did to it. Only with a real sign-in.
 */
export default async function SecurityLogPage({
  searchParams,
}: PageProps<"/settings/security-log">) {
  const { rt, principal, admin } = await accountPage("/settings/security-log");
  const params = await searchParams;
  const cursor = typeof params.cursor === "string" ? params.cursor : undefined;
  const [page, timeZone] = await Promise.all([
    listSecurityEvents(rt.pool, {
      userId: principal.userId,
      viewer: principal.userId,
      cursor,
    }),
    userTimeZone(),
  ]);
  return (
    <div className="mx-auto max-w-4xl space-y-4">
      <header className="space-y-1">
        <h1 className="text-xl font-semibold tracking-tight">Security log</h1>
        <p className="text-muted-foreground text-sm">
          Sign-ins to your account, failed ones, and changes to how you sign in,
          newest first. Entries are kept for a year.
        </p>
      </header>
      <SettingsNav admin={admin} current="/settings/security-log" />
      <SecurityLogList
        description="What concerns your account, with the device and address it came from."
        items={page.items}
        nextHref={
          page.nextCursor
            ? `/settings/security-log?cursor=${encodeURIComponent(page.nextCursor)}`
            : null
        }
        showSubject={false}
        timeZone={timeZone}
      />
    </div>
  );
}
