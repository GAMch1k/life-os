import Link from "next/link";
import { ScrollText } from "lucide-react";

import { DatePicker } from "@/components/date-picker";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Field, FieldLabel } from "@/components/ui/field";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  SECURITY_EVENT_KINDS,
  SECURITY_KINDS,
  type SecurityEvent,
} from "@/lib/auth/security-log";
import {
  hasSecurityFilters,
  securityActor,
  securityDetail,
  securityTitle,
  type SecurityFilters,
} from "@/lib/security-log-view";
import { dateTimeFormat } from "@/lib/time-zone";
import { cn } from "@/lib/utils";

/**
 * The security log's pages: Settings → Security log (the entries that
 * concern you) and the admins' view of every account's, with filters
 * (docs/frontend/dashboard.md#security-log).
 */

function when(iso: string, timeZone: string) {
  return dateTimeFormat(timeZone, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(iso));
}

function timeOnly(iso: string, timeZone: string) {
  return dateTimeFormat(timeZone, { timeStyle: "short" }).format(new Date(iso));
}

function OutcomeBadge({ outcome }: { outcome: SecurityEvent["outcome"] }) {
  if (outcome === "ok") return null;
  return (
    <Badge className="bg-destructive-subtle text-destructive ring-destructive/30 ring-1 ring-inset">
      {outcome === "refused" ? "Refused" : "Failed"}
    </Badge>
  );
}

/** The pages of Settings and of the server's admin, as links. */
export function SettingsNav({
  admin,
  current,
}: {
  admin: boolean;
  current: string;
}) {
  const link = (href: string, label: string) => (
    <Link
      aria-current={current === href ? "page" : undefined}
      className={buttonVariants({
        size: "sm",
        variant: current === href ? "secondary" : "ghost",
      })}
      href={href}
      key={href}
    >
      {label}
    </Link>
  );
  return (
    <nav
      aria-label="Settings"
      className="flex flex-wrap items-center gap-x-1 gap-y-2"
    >
      {link("/settings/security", "Security")}
      {link("/settings/security-log", "Security log")}
      {admin ? (
        <>
          <span className="text-muted-foreground px-2 text-xs font-medium tracking-wide uppercase">
            Server
          </span>
          {link("/admin/security-log", "All accounts' log")}
          {link("/admin/diagnostics", "Diagnostics")}
        </>
      ) : null}
    </nav>
  );
}

export function SecurityLogList({
  description,
  items,
  nextHref,
  showSubject,
  timeZone,
}: {
  description: string;
  items: SecurityEvent[];
  nextHref: string | null;
  /** The admin view names whom each entry concerned. */
  showSubject: boolean;
  timeZone: string;
}) {
  return (
    <Card aria-labelledby="security-log-title" role="region">
      <CardHeader>
        <CardTitle className="flex items-center gap-2" id="security-log-title">
          <ScrollText aria-hidden="true" className="size-4" />
          Entries
        </CardTitle>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        {items.length === 0 ? (
          <p className="text-muted-foreground py-6 text-center text-sm">
            No entries.
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>When</TableHead>
                <TableHead>What</TableHead>
                <TableHead>By</TableHead>
                {showSubject ? <TableHead>Account</TableHead> : null}
                <TableHead>From</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((event) => {
                const detail = securityDetail(event);
                return (
                  <TableRow key={event.id}>
                    <TableCell className="align-top whitespace-nowrap">
                      {when(event.at, timeZone)}
                      {event.count > 1 ? (
                        <span className="text-muted-foreground block text-xs">
                          to {timeOnly(event.lastAt, timeZone)}
                        </span>
                      ) : null}
                    </TableCell>
                    <TableCell className="align-top whitespace-normal">
                      <span className="flex flex-wrap items-center gap-2 font-medium">
                        {securityTitle(event)}
                        <OutcomeBadge outcome={event.outcome} />
                      </span>
                      {detail ? (
                        <span className="text-muted-foreground block text-xs">
                          {detail}
                        </span>
                      ) : null}
                    </TableCell>
                    <TableCell className="align-top whitespace-normal">
                      {securityActor(event)}
                    </TableCell>
                    {showSubject ? (
                      <TableCell className="align-top whitespace-normal">
                        {event.subject
                          ? (event.subject.name ?? "A deleted account")
                          : "—"}
                      </TableCell>
                    ) : null}
                    <TableCell className="text-muted-foreground align-top whitespace-normal">
                      {[event.device, event.address]
                        .filter(Boolean)
                        .join(" · ") || "—"}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
        {nextHref ? (
          <Link
            className={cn(
              buttonVariants({ size: "sm", variant: "outline" }),
              "justify-self-start",
            )}
            href={nextHref}
          >
            Older entries
          </Link>
        ) : null}
      </CardContent>
    </Card>
  );
}

/** The admin view's filters: account, kind, and days (GET, like Activity's). */
export function SecurityLogFilters({
  filters,
  users,
}: {
  filters: SecurityFilters;
  users: { id: string; name: string }[];
}) {
  return (
    <form
      action="/admin/security-log"
      aria-label="Filter the security log"
      className="bg-background grid grid-cols-2 gap-3 rounded-xl border p-3 sm:grid-cols-4 sm:p-4 lg:grid-cols-[repeat(4,minmax(0,1fr))_auto]"
    >
      <Field className="min-w-0 gap-1">
        <FieldLabel htmlFor="security-user">Account</FieldLabel>
        <NativeSelect
          className="w-full"
          defaultValue={filters.user ?? ""}
          id="security-user"
          name="user"
        >
          <NativeSelectOption value="">All accounts</NativeSelectOption>
          {users.map((user) => (
            <NativeSelectOption key={user.id} value={user.id}>
              {user.name}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </Field>
      <Field className="min-w-0 gap-1">
        <FieldLabel htmlFor="security-kind">Kind</FieldLabel>
        <NativeSelect
          className="w-full"
          defaultValue={filters.kind ?? ""}
          id="security-kind"
          name="kind"
        >
          <NativeSelectOption value="">All kinds</NativeSelectOption>
          {SECURITY_KINDS.map((kind) => (
            <NativeSelectOption key={kind} value={kind}>
              {SECURITY_EVENT_KINDS[kind]}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </Field>
      <Field className="min-w-0 gap-1">
        <FieldLabel htmlFor="security-from">From</FieldLabel>
        <DatePicker
          defaultValue={filters.fromDate ?? ""}
          id="security-from"
          label="From"
          name="fromDate"
          placeholder="Any day"
        />
      </Field>
      <Field className="min-w-0 gap-1">
        <FieldLabel htmlFor="security-to">To</FieldLabel>
        <DatePicker
          defaultValue={filters.toDate ?? ""}
          id="security-to"
          label="To"
          name="toDate"
          placeholder="Any day"
        />
      </Field>
      <div className="col-span-2 flex items-end gap-2 sm:col-span-4 lg:col-span-1">
        <Button type="submit">Apply</Button>
        {hasSecurityFilters(filters) && (
          <Link
            className="text-muted-foreground hover:text-foreground h-8 px-1 text-sm leading-8 underline-offset-4 hover:underline"
            href="/admin/security-log"
          >
            Clear
          </Link>
        )}
      </div>
    </form>
  );
}
