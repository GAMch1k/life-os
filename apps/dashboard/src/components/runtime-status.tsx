import Link from "next/link";
import {
  AlertTriangle,
  ArrowRight,
  CheckCircle2,
  CircleMinus,
  ShieldCheck,
  XCircle,
} from "lucide-react";

import { BackupNow } from "@/components/backup-now";
import { MasonryGrid } from "@/components/masonry-grid";
import { Sparkline } from "@/components/sparkline";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  formatTime,
  statusAlerts,
  type CheckState,
  type RuntimeStatus,
  type StatusCheck,
  type StatusFact,
  type StatusPart,
} from "@/lib/runtime-status";
import { memberLine, type MemberStatus } from "@/lib/member-status";
import { cn } from "@/lib/utils";

const STATE_LABEL: Record<CheckState, string> = {
  ok: "OK",
  neutral: "Not set up",
  warning: "Needs attention",
  problem: "Problem",
};

function StateIcon({
  className = "size-5",
  state,
}: {
  className?: string;
  state: CheckState;
}) {
  if (state === "ok")
    return (
      <CheckCircle2
        aria-hidden="true"
        className={cn("text-success", className)}
      />
    );
  if (state === "neutral")
    return (
      <CircleMinus
        aria-hidden="true"
        className={cn("text-muted-foreground", className)}
      />
    );
  if (state === "warning")
    return (
      <AlertTriangle
        aria-hidden="true"
        className={cn("text-warning", className)}
      />
    );
  return (
    <XCircle aria-hidden="true" className={cn("text-destructive", className)} />
  );
}

function Facts({ facts }: { facts: StatusFact[] }) {
  return (
    <dl className="text-muted-foreground grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-sm">
      {facts.map((fact) => (
        <div className="contents" key={fact.label}>
          <dt className="break-words">{fact.label}</dt>
          <dd className="text-foreground break-words">{fact.value}</dd>
        </div>
      ))}
    </dl>
  );
}

function ErrorDetail({ text }: { text: string }) {
  return (
    <pre className="bg-muted overflow-x-auto rounded-md p-2 text-xs whitespace-pre-wrap">
      {text}
    </pre>
  );
}

/** One part of a check, such as one calendar feed, with its own state. */
function Part({ part }: { part: StatusPart }) {
  return (
    <li className="space-y-2 py-3 first:pt-0 last:pb-0">
      <div className="flex items-start justify-between gap-3">
        <h3 className="min-w-0 font-medium break-words">{part.title}</h3>
        <span className="flex shrink-0 items-center gap-1 text-xs font-medium">
          <StateIcon className="size-4" state={part.state} />
          {STATE_LABEL[part.state]}
        </span>
      </div>
      <p className="text-sm">{part.summary}</p>
      {part.detail && <ErrorDetail text={part.detail} />}
      <Facts facts={part.facts} />
    </li>
  );
}

function CheckCard({
  check,
  timeZone,
}: {
  check: StatusCheck;
  timeZone: string;
}) {
  return (
    <Card aria-labelledby={`check-${check.id}`} role="region">
      <CardHeader>
        <div className="flex items-start justify-between gap-3">
          <CardTitle id={`check-${check.id}`}>{check.title}</CardTitle>
          <span className="flex shrink-0 items-center gap-1.5 text-sm font-medium">
            <StateIcon state={check.state} />
            {STATE_LABEL[check.state]}
          </span>
        </div>
        <CardDescription className="text-foreground text-base">
          {check.summary}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {check.detail && <ErrorDetail text={check.detail} />}
        {check.action && <p className="text-sm">{check.action}</p>}
        {check.chart && <Sparkline chart={check.chart} timeZone={timeZone} />}
        {check.sideCharts && (
          <div className="grid gap-3 sm:grid-cols-2">
            {check.sideCharts.map((chart) => (
              <Sparkline chart={chart} key={chart.label} timeZone={timeZone} />
            ))}
          </div>
        )}
        {check.facts.length > 0 && <Facts facts={check.facts} />}
        {check.parts && check.parts.length > 0 && (
          <ul aria-label={check.title} className="divide-y border-t pt-3">
            {check.parts.map((part) => (
              <Part key={part.id} part={part} />
            ))}
          </ul>
        )}
        {check.control?.kind === "backup-now" && (
          <BackupNow running={check.control.running} />
        )}
      </CardContent>
    </Card>
  );
}

export function RuntimeStatusView({ status }: { status: RuntimeStatus }) {
  const alerts = statusAlerts(status);
  return (
    <div className="mx-auto max-w-5xl space-y-5">
      <header className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <h1 className="text-xl font-semibold tracking-tight">Status</h1>
        <p className="text-muted-foreground text-sm">
          Checked {formatTime(status.checkedAt, status.timeZone)} ·{" "}
          <Link
            className="text-primary underline-offset-4 hover:underline"
            href="/status"
          >
            Refresh
          </Link>
        </p>
      </header>
      <p
        className={cn(
          "flex items-center gap-2 rounded-xl border p-4 font-medium",
          status.overall === "ok" && "border-success/30 bg-success-subtle",
          status.overall === "warning" && "border-warning/30 bg-warning-subtle",
          status.overall === "problem" &&
            "border-destructive/30 bg-destructive-subtle",
        )}
        role="status"
      >
        <StateIcon state={status.overall} />
        {status.overall === "ok"
          ? "Everything is working."
          : `${alerts.length} ${alerts.length === 1 ? "thing needs" : "things need"} attention.`}
      </p>
      <MasonryGrid className="md:grid-cols-2">
        {status.checks.map((check) => (
          <CheckCard check={check} key={check.id} timeZone={status.timeZone} />
        ))}
      </MasonryGrid>
    </div>
  );
}

/**
 * Status for members (src/lib/member-status.ts): one line, without detail.
 * An admin without two-factor sign-in sees it too, with what to do.
 */
export function MemberStatusView({ status }: { status: MemberStatus }) {
  return (
    <div className="mx-auto max-w-5xl space-y-5">
      <header className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <h1 className="text-xl font-semibold tracking-tight">Status</h1>
        <p className="text-muted-foreground text-sm">
          Checked {formatTime(status.checkedAt, status.timeZone)} ·{" "}
          <Link
            className="text-primary underline-offset-4 hover:underline"
            href="/status"
          >
            Refresh
          </Link>
        </p>
      </header>
      <p
        className={cn(
          "flex items-center gap-2 rounded-xl border p-4 font-medium",
          status.overall === "ok"
            ? "border-success/30 bg-success-subtle"
            : "border-destructive/30 bg-destructive-subtle",
        )}
        role="status"
      >
        <StateIcon state={status.overall} />
        {memberLine(status.problems)}
      </p>
      {status.reason === "second_factor_required" ? (
        <Alert>
          <ShieldCheck aria-hidden="true" />
          <AlertTitle>The details need two-factor sign-in</AlertTitle>
          <AlertDescription>
            <p>
              You are one of this server&apos;s admins. Turn on an authenticator
              app or add a passkey in{" "}
              <Link
                className="text-primary underline-offset-4 hover:underline"
                href="/settings/security"
              >
                Settings → Security
              </Link>{" "}
              to see the checks, the server, and the backups here.
            </p>
          </AlertDescription>
        </Alert>
      ) : (
        <p className="text-muted-foreground text-sm">
          The server&apos;s admins see the details.
        </p>
      )}
    </div>
  );
}

/** Home's line for members, only when a module has a problem. */
export function MemberStatusAlert({ status }: { status: MemberStatus }) {
  if (status.overall === "ok") return null;
  return (
    <Link
      aria-label={`System status: ${memberLine(status.problems)}`}
      className="border-destructive/30 bg-destructive-subtle hover:bg-destructive/15 mx-auto flex max-w-5xl items-center gap-3 rounded-xl border p-4 transition-colors"
      href="/status"
    >
      <StateIcon state="problem" />
      <span className="flex-1 font-medium">{memberLine(status.problems)}</span>
      <span className="flex items-center gap-1 text-sm font-medium">
        View status
        <ArrowRight aria-hidden="true" className="size-4" />
      </span>
    </Link>
  );
}

/** Home shows only actionable conditions, linking to the Status page. */
export function StatusAlert({ status }: { status: RuntimeStatus }) {
  const [first, ...rest] = statusAlerts(status);
  if (!first) return null;
  return (
    <Link
      aria-label={`System status: ${first.summary}`}
      className={cn(
        "mx-auto flex max-w-5xl items-center gap-3 rounded-xl border p-4 transition-colors",
        first.state === "problem"
          ? "border-destructive/30 bg-destructive-subtle hover:bg-destructive/15"
          : "border-warning/30 bg-warning-subtle hover:bg-warning/15",
      )}
      href="/status"
    >
      <StateIcon state={first.state} />
      <span className="flex-1">
        <span className="font-medium">{first.summary}</span>
        {rest.length > 0 && (
          <span className="text-muted-foreground">
            {" "}
            and {rest.length} more {rest.length === 1 ? "issue" : "issues"}
          </span>
        )}
      </span>
      <span className="flex items-center gap-1 text-sm font-medium">
        View status
        <ArrowRight aria-hidden="true" className="size-4" />
      </span>
    </Link>
  );
}
