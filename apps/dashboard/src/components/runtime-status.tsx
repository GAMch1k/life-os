import Link from "next/link";
import { AlertTriangle, ArrowRight, CheckCircle2, XCircle } from "lucide-react";

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
} from "@/lib/runtime-status";
import { cn } from "@/lib/utils";

const STATE_LABEL: Record<CheckState, string> = {
  ok: "OK",
  warning: "Needs attention",
  problem: "Problem",
};

function StateIcon({ state }: { state: CheckState }) {
  if (state === "ok")
    return (
      <CheckCircle2 aria-hidden="true" className="size-5 text-emerald-600" />
    );
  if (state === "warning")
    return (
      <AlertTriangle aria-hidden="true" className="size-5 text-amber-600" />
    );
  return <XCircle aria-hidden="true" className="size-5 text-red-600" />;
}

function CheckCard({ check }: { check: StatusCheck }) {
  return (
    <Card aria-labelledby={`check-${check.id}`} role="region">
      <CardHeader>
        <div className="flex items-start justify-between gap-3">
          <CardTitle id={`check-${check.id}`}>{check.title}</CardTitle>
          <span className="flex items-center gap-1.5 text-sm font-medium">
            <StateIcon state={check.state} />
            {STATE_LABEL[check.state]}
          </span>
        </div>
        <CardDescription className="text-foreground text-base">
          {check.summary}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {check.detail && (
          <pre className="bg-muted overflow-x-auto rounded-md p-2 text-xs whitespace-pre-wrap">
            {check.detail}
          </pre>
        )}
        {check.action && <p className="text-sm">{check.action}</p>}
        {check.facts.length > 0 && (
          <dl className="text-muted-foreground grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-sm">
            {check.facts.map((fact) => (
              <div className="contents" key={fact.label}>
                <dt>{fact.label}</dt>
                <dd className="text-foreground">{fact.value}</dd>
              </div>
            ))}
          </dl>
        )}
      </CardContent>
    </Card>
  );
}

export function RuntimeStatusView({ status }: { status: RuntimeStatus }) {
  const alerts = statusAlerts(status);
  return (
    <div className="mx-auto max-w-5xl space-y-6">
      <section>
        <p className="text-muted-foreground text-sm font-medium">Status</p>
        <h1 className="mt-1 text-3xl font-semibold tracking-tight">
          Is Life OS working?
        </h1>
        <p className="text-muted-foreground mt-2 max-w-2xl">
          Processing, backlogs, and dependencies as reported by the running
          services. Checked {formatTime(status.checkedAt)} ·{" "}
          <Link
            className="text-primary underline-offset-4 hover:underline"
            href="/status"
          >
            Refresh
          </Link>
        </p>
      </section>
      <p
        className={cn(
          "flex items-center gap-2 rounded-xl border p-4 font-medium",
          status.overall === "ok" && "border-emerald-200 bg-emerald-50",
          status.overall === "warning" && "border-amber-200 bg-amber-50",
          status.overall === "problem" && "border-red-200 bg-red-50",
        )}
        role="status"
      >
        <StateIcon state={status.overall} />
        {status.overall === "ok"
          ? "Everything is working."
          : `${alerts.length} ${alerts.length === 1 ? "thing needs" : "things need"} attention.`}
      </p>
      <div className="grid gap-4 md:grid-cols-2">
        {status.checks.map((check) => (
          <CheckCard check={check} key={check.id} />
        ))}
      </div>
    </div>
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
          ? "border-red-200 bg-red-50 hover:bg-red-100"
          : "border-amber-200 bg-amber-50 hover:bg-amber-100",
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
