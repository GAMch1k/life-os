import Link from "next/link";
import { ArrowDown, ArrowUp, History, Workflow } from "lucide-react";

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { CopyButton } from "@/components/copy-button";
import { ExpandableText } from "@/components/expandable-text";
import {
  activityHref,
  actorLabel,
  emptyState,
  EVENT_TYPES,
  eventLabel,
  eventTime,
  fullSummary,
  hasFilters,
  OUTCOMES,
  SOURCES,
  sourceDescription,
  sourceLabel,
  SUBJECT_TYPES,
  subjectLabel,
  type ActivityEvent,
  type ActivityFilters,
  type ActivityLog,
  type Relation,
  type Trace,
  type TraceNode,
} from "@/lib/activity-log";
import { cn } from "@/lib/utils";

const fieldClass =
  "border-input bg-background focus-visible:ring-ring/50 h-9 w-full rounded-lg border px-2.5 text-sm focus-visible:ring-3 focus-visible:outline-none";

const OUTCOME_STYLES: Record<string, string> = {
  succeeded: "bg-success-subtle text-success ring-success/30",
  pending: "bg-warning-subtle text-warning ring-warning/30",
  failed: "bg-destructive-subtle text-destructive ring-destructive/30",
};

function OutcomeBadge({ outcome }: { outcome: string }) {
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded-md px-1.5 py-0.5 text-xs font-medium capitalize ring-1 ring-inset",
        OUTCOME_STYLES[outcome] ?? "bg-muted text-muted-foreground",
      )}
    >
      {outcome}
    </span>
  );
}

function EventMeta({
  event,
  timeZone,
}: {
  event: ActivityEvent;
  timeZone: string;
}) {
  return (
    <p className="text-muted-foreground text-xs">
      <time dateTime={event.occurredAt}>
        {eventTime(event.occurredAt, timeZone)}
      </time>
      {" · "}
      {sourceLabel(event.source)} · {actorLabel(event.actor)}
    </p>
  );
}

export function ActivityLogView({ log }: { log: ActivityLog }) {
  const { filters, timeZone, list, selectedId, selection } = log;
  return (
    <div className="mx-auto max-w-screen-xl space-y-4 sm:space-y-6">
      <h1 className="text-2xl font-semibold tracking-tight">Event Log</h1>
      <Filters filters={filters} />
      <div className="grid items-start gap-4 sm:gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.15fr)]">
        <EventList
          filters={filters}
          list={list}
          selectedId={selectedId}
          timeZone={timeZone}
        />
        <section aria-label="Selected event" className="lg:sticky lg:top-20">
          {selection === null ? (
            <Card>
              <CardContent className="text-muted-foreground py-10 text-center text-sm">
                Select an entry to see what led to it and what it led to.
              </CardContent>
            </Card>
          ) : selection.kind === "unavailable" ? (
            <Card>
              <CardContent className="text-muted-foreground py-10 text-center text-sm">
                This entry&apos;s trace is unavailable right now.
              </CardContent>
            </Card>
          ) : selection.kind === "missing" ? (
            <Card>
              <CardContent className="text-muted-foreground py-10 text-center text-sm">
                No activity entry exists with this id.
              </CardContent>
            </Card>
          ) : (
            <TraceView
              filters={filters}
              timeZone={timeZone}
              trace={selection.trace}
            />
          )}
        </section>
      </div>
    </div>
  );
}

function Filters({ filters }: { filters: ActivityFilters }) {
  return (
    <form
      action="/activity"
      aria-label="Filter activity"
      className="bg-background grid grid-cols-2 gap-3 rounded-xl border p-3 sm:grid-cols-3 sm:p-4 lg:grid-cols-[repeat(6,minmax(0,1fr))_auto]"
    >
      {/* Narrowed views (one workflow or one entity) survive filter changes. */}
      {filters.correlationId && (
        <input
          name="correlationId"
          type="hidden"
          value={filters.correlationId}
        />
      )}
      {filters.subjectId && (
        <input name="subjectId" type="hidden" value={filters.subjectId} />
      )}
      <label className="min-w-0 space-y-1 text-sm">
        <span className="font-medium">Domain</span>
        <select
          className={fieldClass}
          defaultValue={filters.source ?? ""}
          name="source"
        >
          <option value="">All domains</option>
          {SOURCES.map((source) => (
            <option
              key={source.value}
              title={source.description}
              value={source.value}
            >
              {source.label}
            </option>
          ))}
        </select>
      </label>
      <label className="min-w-0 space-y-1 text-sm">
        <span className="font-medium">Entity</span>
        <select
          className={fieldClass}
          defaultValue={filters.subjectType ?? ""}
          name="subjectType"
        >
          <option value="">All entities</option>
          {SUBJECT_TYPES.map((subject) => (
            <option key={subject.value} value={subject.value}>
              {subject.label}
            </option>
          ))}
        </select>
      </label>
      <label className="min-w-0 space-y-1 text-sm">
        <span className="font-medium">Event</span>
        <select
          className={fieldClass}
          defaultValue={filters.type ?? ""}
          name="type"
        >
          <option value="">All events</option>
          {EVENT_TYPES.map((type) => (
            <option key={type} value={type}>
              {eventLabel({ type, data: {} })}
            </option>
          ))}
          {filters.type && !EVENT_TYPES.includes(filters.type) && (
            <option value={filters.type}>{filters.type}</option>
          )}
        </select>
      </label>
      <label className="min-w-0 space-y-1 text-sm">
        <span className="font-medium">Status</span>
        <select
          className={fieldClass}
          defaultValue={filters.outcome ?? ""}
          name="outcome"
        >
          <option value="">Any status</option>
          {OUTCOMES.map((outcome) => (
            <option key={outcome} value={outcome}>
              {outcome.charAt(0).toUpperCase() + outcome.slice(1)}
            </option>
          ))}
        </select>
      </label>
      <label className="min-w-0 space-y-1 text-sm">
        <span className="font-medium">From</span>
        <input
          className={fieldClass}
          defaultValue={filters.fromDate}
          name="fromDate"
          type="date"
        />
      </label>
      <label className="min-w-0 space-y-1 text-sm">
        <span className="font-medium">To</span>
        <input
          className={fieldClass}
          defaultValue={filters.toDate}
          name="toDate"
          type="date"
        />
      </label>
      <div className="col-span-2 flex items-end gap-2 sm:col-span-3 lg:col-span-1">
        <button
          className="bg-primary text-primary-foreground hover:bg-primary/80 h-9 rounded-lg px-3 text-sm font-medium"
          type="submit"
        >
          Apply
        </button>
        {hasFilters(filters) && (
          <Link
            className="text-muted-foreground hover:text-foreground h-9 px-1 text-sm leading-9 underline-offset-4 hover:underline"
            href="/activity"
          >
            Clear
          </Link>
        )}
      </div>
    </form>
  );
}

function EventList({
  filters,
  list,
  selectedId,
  timeZone,
}: {
  filters: ActivityFilters;
  list: ActivityLog["list"];
  selectedId?: string;
  timeZone: string;
}) {
  const empty = emptyState(filters);
  const domain = sourceDescription(filters.source);
  const narrowed = filters.correlationId
    ? "Showing one workflow."
    : filters.subjectId
      ? "Showing one entity's history."
      : undefined;
  return (
    <section aria-labelledby="events-heading">
      <Card>
        <CardHeader>
          <CardTitle id="events-heading">
            <span className="flex items-center gap-2">
              <History aria-hidden="true" className="size-4" />
              Events
            </span>
          </CardTitle>
          <CardDescription>
            Newest first.{" "}
            {domain && (
              <>
                <span className="text-foreground font-medium">
                  {sourceLabel(filters.source!)}:
                </span>{" "}
                {domain}{" "}
              </>
            )}
            {narrowed && (
              <>
                {narrowed}{" "}
                <Link
                  className="text-primary underline-offset-4 hover:underline"
                  href={activityHref({
                    ...filters,
                    correlationId: undefined,
                    subjectId: undefined,
                    subjectType: filters.subjectId
                      ? undefined
                      : filters.subjectType,
                    cursor: undefined,
                  })}
                >
                  Show all
                </Link>
              </>
            )}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {list === null ? (
            <p className="text-muted-foreground py-6 text-center text-sm">
              Activity is unavailable right now.
            </p>
          ) : list.items.length === 0 ? (
            <div className="space-y-2 py-6 text-center text-sm">
              <p className="text-muted-foreground">{empty.message}</p>
              {empty.link && (
                <Link
                  className="text-primary underline-offset-4 hover:underline"
                  href={empty.link.href}
                >
                  {empty.link.label}
                </Link>
              )}
            </div>
          ) : (
            <ol aria-label="Activity entries" className="divide-y">
              {list.items.map((event) => (
                <li key={event.id}>
                  <Link
                    aria-current={event.id === selectedId ? "true" : undefined}
                    className={cn(
                      "hover:bg-muted/60 -mx-2 block rounded-lg px-2 py-3",
                      event.id === selectedId && "bg-secondary",
                    )}
                    href={activityHref(filters, { event: event.id })}
                    scroll={false}
                  >
                    <span className="flex items-start justify-between gap-3">
                      <span className="min-w-0 text-sm font-medium wrap-anywhere">
                        {eventLabel(event)}
                      </span>
                      <OutcomeBadge outcome={event.outcome} />
                    </span>
                    <span className="mt-0.5 block text-sm wrap-anywhere">
                      {event.summary}
                    </span>
                    <span className="mt-1 block">
                      <EventMeta event={event} timeZone={timeZone} />
                    </span>
                  </Link>
                </li>
              ))}
            </ol>
          )}
          {(list?.nextCursor || filters.cursor) && (
            <nav
              aria-label="Activity pages"
              className="mt-4 flex justify-between text-sm"
            >
              {filters.cursor ? (
                <Link
                  className="text-primary underline-offset-4 hover:underline"
                  href={activityHref(filters, { cursor: undefined })}
                >
                  Newest
                </Link>
              ) : (
                <span />
              )}
              {list?.nextCursor && (
                <Link
                  className="text-primary underline-offset-4 hover:underline"
                  href={activityHref(filters, { cursor: list.nextCursor })}
                >
                  Older events
                </Link>
              )}
            </nav>
          )}
        </CardContent>
      </Card>
    </section>
  );
}

const RELATION_LABELS: Record<Relation, string | undefined> = {
  selected: "Selected",
  upstream: "Led here",
  downstream: "Led to",
  other: undefined,
};

function TraceTree({
  filters,
  nodes,
}: {
  filters: ActivityFilters;
  nodes: TraceNode[];
}) {
  return (
    <ul className="border-border space-y-1 border-l pl-3">
      {nodes.map((node) => (
        <li key={node.event.id}>
          <Link
            aria-current={node.relation === "selected" ? "true" : undefined}
            className={cn(
              "hover:bg-muted/60 block rounded-md px-2 py-1.5 text-sm",
              node.relation === "selected" &&
                "bg-secondary ring-ring/40 ring-1",
              node.relation === "other" && "text-muted-foreground",
            )}
            href={activityHref(filters, { event: node.event.id })}
            scroll={false}
          >
            <span className="flex items-center justify-between gap-2">
              <span className="min-w-0 font-medium wrap-anywhere">
                {eventLabel(node.event)}
              </span>
              <span className="flex shrink-0 items-center gap-1.5">
                {RELATION_LABELS[node.relation] && (
                  <span className="text-muted-foreground text-xs">
                    {RELATION_LABELS[node.relation]}
                  </span>
                )}
                {node.event.outcome !== "succeeded" && (
                  <OutcomeBadge outcome={node.event.outcome} />
                )}
              </span>
            </span>
            <span className="text-muted-foreground block text-xs wrap-anywhere">
              {node.event.summary}
            </span>
          </Link>
          {node.children.length > 0 && (
            <div className="mt-1 ml-2">
              <TraceTree filters={filters} nodes={node.children} />
            </div>
          )}
        </li>
      ))}
    </ul>
  );
}

function ChainList({
  events,
  filters,
  empty,
}: {
  events: ActivityEvent[];
  filters: ActivityFilters;
  empty: string;
}) {
  if (events.length === 0) {
    return <p className="text-muted-foreground text-sm">{empty}</p>;
  }
  return (
    <ol className="space-y-1">
      {events.map((event) => (
        <li key={event.id}>
          <Link
            className="hover:bg-muted/60 -mx-2 block rounded-md px-2 py-1 text-sm wrap-anywhere"
            href={activityHref(filters, { event: event.id })}
            scroll={false}
          >
            <span className="font-medium">{eventLabel(event)}</span>
            <span className="text-muted-foreground"> — {event.summary}</span>
          </Link>
        </li>
      ))}
    </ol>
  );
}

function TraceView({
  filters,
  timeZone,
  trace,
}: {
  filters: ActivityFilters;
  timeZone: string;
  trace: Trace;
}) {
  const { selected } = trace;
  const subject = subjectLabel(selected.subject.type).toLowerCase();
  return (
    <Card>
      <CardHeader>
        <div className="flex items-start justify-between gap-3">
          <CardTitle className="min-w-0 wrap-anywhere" id="selected-heading">
            {eventLabel(selected)}
          </CardTitle>
          <OutcomeBadge outcome={selected.outcome} />
        </div>
        <CardDescription>
          <ExpandableText
            fullText={fullSummary(selected)}
            key={selected.id}
            text={selected.summary}
          />
        </CardDescription>
        <EventMeta event={selected} timeZone={timeZone} />
      </CardHeader>
      <CardContent className="space-y-6">
        <section aria-labelledby="upstream-heading" className="space-y-2">
          <h3
            className="flex items-center gap-2 text-sm font-semibold"
            id="upstream-heading"
          >
            <ArrowUp aria-hidden="true" className="size-4" />
            What led here
          </h3>
          <ChainList
            empty={
              trace.externalCauseId
                ? "Its cause was recorded outside this workflow."
                : "This started the workflow."
            }
            events={trace.upstream}
            filters={filters}
          />
          {trace.externalCauseId && (
            <Link
              className="text-primary text-sm underline-offset-4 hover:underline"
              href={activityHref(filters, { event: trace.externalCauseId })}
            >
              Open the cause
            </Link>
          )}
        </section>
        <section aria-labelledby="downstream-heading" className="space-y-2">
          <h3
            className="flex items-center gap-2 text-sm font-semibold"
            id="downstream-heading"
          >
            <ArrowDown aria-hidden="true" className="size-4" />
            What it led to
          </h3>
          <ChainList
            empty="Nothing recorded as a result yet."
            events={trace.downstream}
            filters={filters}
          />
        </section>
        <section aria-labelledby="related-heading" className="space-y-2">
          <h3 className="text-sm font-semibold" id="related-heading">
            Same {subject} in other workflows
          </h3>
          {trace.related.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              Nothing else has happened to this {subject}.
            </p>
          ) : (
            <ol className="space-y-1">
              {trace.related.map((event) => (
                <li key={event.id}>
                  <Link
                    className="hover:bg-muted/60 -mx-2 block rounded-md px-2 py-1 text-sm wrap-anywhere"
                    href={activityHref(filters, { event: event.id })}
                    scroll={false}
                  >
                    <span className="font-medium">{eventLabel(event)}</span>
                    <span className="text-muted-foreground">
                      {" "}
                      — {eventTime(event.occurredAt, timeZone)} by{" "}
                      {actorLabel(event.actor)}
                    </span>
                  </Link>
                </li>
              ))}
            </ol>
          )}
        </section>
        <section aria-labelledby="workflow-heading" className="space-y-2">
          <div className="flex items-center justify-between gap-2">
            <h3
              className="flex items-center gap-2 text-sm font-semibold"
              id="workflow-heading"
            >
              <Workflow aria-hidden="true" className="size-4" />
              Whole workflow
            </h3>
            <Link
              className="text-primary text-xs underline-offset-4 hover:underline"
              href={activityHref(
                { correlationId: selected.correlationId },
                { event: selected.id },
              )}
            >
              List only this workflow
            </Link>
          </div>
          <TraceTree filters={filters} nodes={trace.roots} />
        </section>
        <details className="rounded-lg border p-3 text-sm">
          <summary className="cursor-pointer font-medium">Diagnostics</summary>
          <dl className="mt-3 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-xs">
            {(
              [
                ["Event id", selected.id],
                ["Type", selected.type],
                ["Source", selected.source],
                ["Subject", `${selected.subject.type}/${selected.subject.id}`],
                ["Correlation", selected.correlationId],
                ["Caused by", selected.causationId ?? "—"],
                [
                  "Actor",
                  `${selected.actor.kind}${selected.actor.name ? `:${selected.actor.name}` : ""}`,
                ],
                ["Occurred", selected.occurredAt],
                ["Recorded", selected.recordedAt],
              ] as const
            ).map(([label, value]) => (
              <div className="contents" key={label}>
                <dt className="text-muted-foreground">{label}</dt>
                <dd className="font-mono break-all">{value}</dd>
              </div>
            ))}
          </dl>
          <div className="mt-3 flex items-center justify-between gap-2">
            <span className="text-muted-foreground text-xs">Event data</span>
            <CopyButton label="Copy diagnostics" targetId="diagnostics-data" />
          </div>
          <pre
            className="bg-muted mt-1 max-h-64 overflow-auto rounded-md p-2 text-xs"
            id="diagnostics-data"
          >
            {JSON.stringify(selected.data, null, 2)}
          </pre>
        </details>
      </CardContent>
    </Card>
  );
}
