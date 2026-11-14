import type { Metadata } from "next";
import Link from "next/link";
import { Activity, ListTree } from "lucide-react";

import { AdminRefusal } from "@/components/admin-refusal";
import { SettingsNav } from "@/components/security-log";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { adminPage } from "@/lib/auth/page-context";
import {
  DIAGNOSTIC_CODES,
  type DiagnosticCount,
  getDiagnostics,
} from "@/lib/diagnostics";
import { dateTimeFormat } from "@/lib/time-zone";
import { userTimeZone } from "@/lib/user-time-zone";

export const metadata: Metadata = { title: "Diagnostics · Life OS" };
export const dynamic = "force-dynamic";

const PATH = "/admin/diagnostics";

function when(iso: string, timeZone: string) {
  return dateTimeFormat(timeZone, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(iso));
}

function Unreadable() {
  return (
    <p className="text-muted-foreground py-6 text-center text-sm">
      Activity could not be read right now.
    </p>
  );
}

function Counts({
  counts,
  timeZone,
}: {
  counts: DiagnosticCount[] | null;
  timeZone: string;
}) {
  if (counts === null) return <Unreadable />;
  if (counts.length === 0) {
    return (
      <p className="text-muted-foreground py-6 text-center text-sm">
        No server errors.
      </p>
    );
  }
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Service</TableHead>
          <TableHead>What</TableHead>
          <TableHead className="text-right">Count</TableHead>
          <TableHead>Last</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {counts.map((count) => (
          <TableRow key={`${count.service}-${count.code}`}>
            <TableCell>{count.service}</TableCell>
            <TableCell className="whitespace-normal">
              {DIAGNOSTIC_CODES[count.code] ?? count.code}
              <span className="text-muted-foreground block font-mono text-xs">
                {count.code}
              </span>
            </TableCell>
            <TableCell className="text-right tabular-nums">
              {count.count.toLocaleString("en")}
            </TableCell>
            <TableCell>{when(count.lastAt, timeZone)}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

/**
 * Server diagnostics, for the server's admins (docs/architecture/multi-user.md#logs):
 * each service's server errors, by code, route, and status, from Activity.
 * Codes, ids, counts, and times, never content. The containers, the host's
 * health, the backups, and the AI's queue are on Status.
 */
export default async function DiagnosticsPage() {
  const { gate, admin } = await adminPage(PATH);
  if (!gate.ok) return <AdminRefusal code={gate.code} title="Diagnostics" />;
  const [diagnostics, timeZone] = await Promise.all([
    getDiagnostics(),
    userTimeZone(),
  ]);
  return (
    <div className="mx-auto max-w-5xl space-y-4">
      <header className="space-y-1">
        <h1 className="text-xl font-semibold tracking-tight">Diagnostics</h1>
        <p className="text-muted-foreground text-sm">
          The services&apos; server errors, counted by what went wrong and
          where. They carry codes, ids, counts, and times, never anyone&apos;s
          titles, amounts, or messages, and are kept for 90 days. The
          containers, the server&apos;s memory and disks, the backups, and the
          AI&apos;s queue are on{" "}
          <Link
            className="text-primary underline-offset-4 hover:underline"
            href="/status"
          >
            Status
          </Link>
          .
        </p>
      </header>
      <SettingsNav admin={admin} current={PATH} />
      <div className="grid gap-4 md:grid-cols-2">
        <Card aria-labelledby="diagnostics-day" role="region">
          <CardHeader>
            <CardTitle className="flex items-center gap-2" id="diagnostics-day">
              <Activity aria-hidden="true" className="size-4" />
              Last 24 hours
            </CardTitle>
          </CardHeader>
          <CardContent>
            <Counts counts={diagnostics.day} timeZone={timeZone} />
          </CardContent>
        </Card>
        <Card aria-labelledby="diagnostics-week" role="region">
          <CardHeader>
            <CardTitle
              className="flex items-center gap-2"
              id="diagnostics-week"
            >
              <Activity aria-hidden="true" className="size-4" />
              Last 7 days
            </CardTitle>
          </CardHeader>
          <CardContent>
            <Counts counts={diagnostics.week} timeZone={timeZone} />
          </CardContent>
        </Card>
      </div>
      <Card aria-labelledby="diagnostics-recent" role="region">
        <CardHeader>
          <CardTitle
            className="flex items-center gap-2"
            id="diagnostics-recent"
          >
            <ListTree aria-hidden="true" className="size-4" />
            Recent
          </CardTitle>
          <CardDescription>
            One row counts a burst of one error at one route, sent once a
            minute. The request id finds the last one in the service&apos;s log.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {diagnostics.recent === null ? (
            <Unreadable />
          ) : diagnostics.recent.length === 0 ? (
            <p className="text-muted-foreground py-6 text-center text-sm">
              No server errors.
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>When</TableHead>
                  <TableHead>Service</TableHead>
                  <TableHead>What</TableHead>
                  <TableHead>Where</TableHead>
                  <TableHead className="text-right">Count</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {diagnostics.recent.map((event) => (
                  <TableRow key={event.id}>
                    <TableCell className="align-top">
                      {when(event.lastAt, timeZone)}
                    </TableCell>
                    <TableCell className="align-top">{event.service}</TableCell>
                    <TableCell className="align-top whitespace-normal">
                      {DIAGNOSTIC_CODES[event.code] ?? event.code}
                      {event.status ? (
                        <span className="text-muted-foreground block text-xs">
                          HTTP {event.status}
                          {event.durationMs != null
                            ? ` · up to ${event.durationMs} ms`
                            : ""}
                        </span>
                      ) : null}
                    </TableCell>
                    <TableCell className="align-top whitespace-normal">
                      <span className="font-mono text-xs">
                        {event.route ?? "—"}
                      </span>
                      {event.requestId ? (
                        <span className="text-muted-foreground block font-mono text-xs break-all">
                          {event.requestId}
                        </span>
                      ) : null}
                    </TableCell>
                    <TableCell className="text-right align-top tabular-nums">
                      {event.count.toLocaleString("en")}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
