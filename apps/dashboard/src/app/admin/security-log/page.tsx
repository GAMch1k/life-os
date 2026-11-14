import type { Metadata } from "next";

import { AdminRefusal } from "@/components/admin-refusal";
import {
  SecurityLogFilters,
  SecurityLogList,
  SettingsNav,
} from "@/components/security-log";
import { adminPage } from "@/lib/auth/page-context";
import { listSecurityEvents, securityLogUsers } from "@/lib/auth/security-log";
import {
  parseSecurityFilters,
  securityHref,
  securityRange,
} from "@/lib/security-log-view";
import { userTimeZone } from "@/lib/user-time-zone";

export const metadata: Metadata = { title: "Security log · Life OS" };
export const dynamic = "force-dynamic";

const PATH = "/admin/security-log";

/**
 * The security log of every account, for the server's admins
 * (docs/architecture/multi-user.md#logs), with filters by account, kind,
 * and days. A signed-in owner or admin with two-factor sign-in.
 */
export default async function AdminSecurityLogPage({
  searchParams,
}: PageProps<"/admin/security-log">) {
  const { rt, gate, admin } = await adminPage(PATH);
  if (!gate.ok) return <AdminRefusal code={gate.code} title="Security log" />;
  const filters = parseSecurityFilters(await searchParams);
  const timeZone = await userTimeZone();
  const range = securityRange(filters, timeZone);
  const [page, users] = await Promise.all([
    listSecurityEvents(rt.pool, {
      userId: filters.user,
      kind: filters.kind,
      from: range.from,
      to: range.to,
      cursor: filters.cursor,
    }),
    securityLogUsers(rt.pool),
  ]);
  return (
    <div className="mx-auto max-w-5xl space-y-4">
      <header className="space-y-1">
        <h1 className="text-xl font-semibold tracking-tight">
          Security log of every account
        </h1>
        <p className="text-muted-foreground text-sm">
          Sign-ins, failed sign-ins, changes to accounts, admin actions, legacy
          access, and the commands run on the server, newest first. No content
          of anyone&apos;s: who, what, when, and from where. Entries are kept
          for a year.
        </p>
      </header>
      <SettingsNav admin={admin} current={PATH} />
      <SecurityLogFilters filters={filters} users={users} />
      <SecurityLogList
        description="Use without signing in (legacy access) is one entry a day for each address and device, with the number of requests."
        items={page.items}
        nextHref={
          page.nextCursor
            ? securityHref(PATH, filters, { cursor: page.nextCursor })
            : null
        }
        showSubject
        timeZone={timeZone}
      />
    </div>
  );
}
