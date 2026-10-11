import Link from "next/link";
import {
  Check,
  CircleX,
  Clock,
  LoaderCircle,
  RotateCcw,
  TriangleAlert,
  Workflow,
} from "lucide-react";

import { CaptureImageStrip } from "@/components/capture-images";
import { Card, CardContent } from "@/components/ui/card";
import { captureImages } from "@/lib/capture-images";
import {
  formatInboxTime,
  type InboxBacklog,
  type InboxItem,
  type InboxItemStatus,
  inboxItemStatus,
  inboxStatusLabel,
  retryReasonLabel,
  shownOfTotal,
} from "@/lib/inbox-summary";
import { cn } from "@/lib/utils";

// Presentation shared by Home's capture card and /inbox. Each capture shows
// the one bucket it is in; nothing here fetches or mutates.

const STATUS_STYLE: Record<
  InboxItemStatus["kind"],
  { icon: React.ComponentType<{ className?: string }>; tone: string }
> = {
  received: { icon: Clock, tone: "bg-muted text-muted-foreground" },
  retrying: { icon: RotateCcw, tone: "bg-muted text-muted-foreground" },
  retry: { icon: Clock, tone: "bg-muted text-muted-foreground" },
  processing: {
    icon: LoaderCircle,
    tone: "bg-muted text-muted-foreground [&>svg]:animate-spin",
  },
  review: {
    icon: TriangleAlert,
    tone: "border-border bg-background text-foreground",
  },
  failed: { icon: CircleX, tone: "bg-destructive/10 text-destructive" },
  processed: { icon: Check, tone: "bg-secondary text-secondary-foreground" },
  discarded: { icon: CircleX, tone: "bg-muted text-muted-foreground" },
};

export function InboxStateBadge({
  now,
  status,
  timeZone,
}: {
  now: Date;
  status: InboxItemStatus;
  timeZone: string;
}) {
  const { icon: Icon, tone } = STATUS_STYLE[status.kind];
  return (
    <span
      title={
        status.kind === "retry" && status.reason
          ? `Retry scheduled: ${retryReasonLabel(status.reason)}`
          : undefined
      }
      className={cn(
        "inline-flex shrink-0 items-center gap-1 rounded-md border border-transparent px-1.5 py-0.5 text-xs font-medium whitespace-nowrap",
        tone,
      )}
    >
      <Icon aria-hidden="true" className="size-3" />
      {inboxStatusLabel(status, timeZone, now)}
    </span>
  );
}

/** Header counts for /inbox, each linking to its section. */
export function InboxCounts({ backlog }: { backlog: InboxBacklog | null }) {
  if (!backlog) {
    return (
      <p className="text-muted-foreground text-sm">
        Inbox counts are unavailable.
      </p>
    );
  }
  const failed = backlog.failedCount ?? 0;
  const counts = [
    { href: "#pending", count: backlog.pendingCount, label: "pending" },
    {
      href: "#human-review",
      count: backlog.humanReviewCount,
      label: "to review",
    },
    { href: "#failed", count: failed, label: "failed", alert: true },
  ].filter(({ count }) => count > 0);
  if (!counts.length) {
    return <p className="text-muted-foreground text-sm">All clear</p>;
  }
  return (
    <ul aria-label="Inbox counts" className="flex flex-wrap gap-1.5">
      {counts.map(({ alert, count, href, label }) => (
        <li key={href}>
          <a
            className={cn(
              "hover:bg-muted inline-flex h-7 items-center rounded-md border px-2 text-sm tabular-nums transition-colors",
              alert && "border-destructive/30 text-destructive",
            )}
            href={href}
          >
            {count} {label}
          </a>
        </li>
      ))}
    </ul>
  );
}

/** A compact titled list on /inbox. `null` items mean it could not be read. */
export function InboxSection({
  children,
  count,
  icon,
  id,
  note,
  title,
}: {
  children: React.ReactNode;
  count?: number;
  icon: React.ReactNode;
  id: string;
  note?: string;
  title: string;
}) {
  return (
    <Card
      aria-labelledby={`${id}-title`}
      className="scroll-mt-20"
      id={id}
      role="region"
      size="sm"
    >
      <CardContent className="space-y-2">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          {icon}
          <h2 className="text-sm font-semibold" id={`${id}-title`}>
            {title}
          </h2>
          {count !== undefined ? (
            <span className="text-muted-foreground text-xs tabular-nums">
              {count}
            </span>
          ) : null}
          {note ? (
            <span className="text-muted-foreground ml-auto text-xs">
              {note}
            </span>
          ) : null}
        </div>
        {children}
      </CardContent>
    </Card>
  );
}

export function SectionUnavailable() {
  return (
    <p className="text-muted-foreground text-sm">
      Could not be read right now.
    </p>
  );
}

/** One capture: its text, its state, and when it happened. */
export function InboxItemRow({
  action,
  at,
  children,
  item,
  now,
  showStatus = false,
  timeZone,
}: {
  action?: React.ReactNode;
  at: string;
  children?: React.ReactNode;
  item: InboxItem;
  now: Date;
  /** Only where a section mixes states (Pending); elsewhere the title says it. */
  showStatus?: boolean;
  timeZone: string;
}) {
  return (
    <li className="flex items-start gap-3 py-2 first:pt-0 last:pb-0">
      <div className="min-w-0 flex-1">
        <p className="line-clamp-3 wrap-anywhere whitespace-pre-line">
          {item.content}
        </p>
        <CaptureImageStrip
          label={item.content}
          references={captureImages(item)}
        />
        {children}
      </div>
      <div className="flex shrink-0 flex-col items-end gap-1 sm:flex-row-reverse sm:items-center sm:gap-2">
        {showStatus ? (
          <InboxStateBadge
            now={now}
            status={inboxItemStatus(item, now)}
            timeZone={timeZone}
          />
        ) : null}
        <time
          className="text-muted-foreground text-xs tabular-nums"
          dateTime={at}
        >
          {formatInboxTime(at, timeZone, now)}
        </time>
        {action}
      </div>
    </li>
  );
}

export function PendingSection({
  backlog,
  items,
  now,
  timeZone,
}: {
  backlog: InboxBacklog | null;
  items: InboxItem[] | null;
  now: Date;
  timeZone: string;
}) {
  const total = backlog?.pendingCount ?? items?.length;
  if (items && !items.length && !total) return null;
  const notes = [
    backlog?.retryingCount ? `${backlog.retryingCount} retrying` : undefined,
    backlog?.nextRetryAt
      ? `next retry ${formatInboxTime(backlog.nextRetryAt, timeZone, now)}${
          backlog.nextRetryReason
            ? ` (${retryReasonLabel(backlog.nextRetryReason)})`
            : ""
        }`
      : undefined,
    items ? shownOfTotal(items.length, total) : undefined,
  ].filter(Boolean);
  return (
    <InboxSection
      count={total}
      icon={<Clock aria-hidden="true" className="size-4" />}
      id="pending"
      note={notes.join(" · ") || undefined}
      title="Pending"
    >
      {items ? (
        <ul className="divide-y">
          {items.map((item) => (
            <InboxItemRow
              at={item.createdAt}
              item={item}
              key={item.id}
              now={now}
              showStatus
              timeZone={timeZone}
            />
          ))}
        </ul>
      ) : (
        <SectionUnavailable />
      )}
    </InboxSection>
  );
}

export function ProcessedSection({
  items,
  now,
  timeZone,
}: {
  items: InboxItem[] | null;
  now: Date;
  timeZone: string;
}) {
  if (items && !items.length) return null;
  return (
    <InboxSection
      icon={<Check aria-hidden="true" className="size-4" />}
      id="processed"
      title="Recently processed"
    >
      {items ? (
        <ul className="divide-y">
          {items.map((item) => (
            <InboxItemRow
              action={
                <Link
                  aria-label={`Trace in Event Log: ${item.content}`}
                  className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs"
                  href={`/activity?correlationId=${encodeURIComponent(item.correlationId)}`}
                >
                  <Workflow aria-hidden="true" className="size-3" /> Trace
                </Link>
              }
              at={item.resolvedAt ?? item.updatedAt}
              item={item}
              key={item.id}
              now={now}
              timeZone={timeZone}
            />
          ))}
        </ul>
      ) : (
        <SectionUnavailable />
      )}
    </InboxSection>
  );
}
