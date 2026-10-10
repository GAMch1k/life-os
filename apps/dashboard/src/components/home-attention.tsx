import Link from "next/link";
import {
  ArrowRight,
  CalendarDays,
  CheckSquare2,
  ListTodo,
  TriangleAlert,
} from "lucide-react";

import { eventTimeLabel } from "@/components/calendar-summary";
import { Card, CardContent } from "@/components/ui/card";
import type { HomeAttention } from "@/lib/home-attention";

const URGENT_PREVIEW = 5;
const REVIEW_PREVIEW = 2;

type AttentionItem = { id: string; label: string; hint?: string };

function plural(count: number, more: boolean, noun: string) {
  return `${count}${more ? "+" : ""} ${noun}${count === 1 && !more ? "" : "s"}`;
}

/**
 * Home's summaries under the capture card. `humanReviewCount` is the true
 * count from the Inbox backlog; the review list itself is only a preview.
 * `timeZone` is the zone the page renders times in.
 */
export function HomeAttentionSurface({
  attention,
  humanReviewCount,
  timeZone,
}: {
  attention: HomeAttention;
  humanReviewCount?: number;
  timeZone: string;
}) {
  const { humanReview, nextEvent, unscheduled, urgent } = attention;
  const urgentItems: AttentionItem[] = urgent
    ? [
        ...urgent.overdue.map((task) => ({
          id: task.id,
          label: task.title,
          hint: "Overdue",
        })),
        ...urgent.today.map((task) => ({
          id: task.id,
          label: task.title,
          hint: "Today",
        })),
      ].slice(0, URGENT_PREVIEW)
    : [];
  const reviewCount = humanReview
    ? Math.max(humanReviewCount ?? 0, humanReview.items.length)
    : 0;
  const reviewMore =
    humanReviewCount === undefined && Boolean(humanReview?.more);

  return (
    <div className="grid gap-3 md:grid-cols-2">
      <AttentionCard
        id="attention-review"
        href="/inbox"
        linkLabel="Review in Inbox"
        icon={<TriangleAlert aria-hidden="true" className="size-4" />}
        title="Human Review"
        detail={
          humanReview === null
            ? "Inbox is unavailable right now."
            : reviewCount
              ? `${plural(reviewCount, reviewMore, "capture")} waiting for your decision`
              : "Nothing waiting for review."
        }
        items={humanReview?.items.slice(0, REVIEW_PREVIEW).map((item) => ({
          id: item.id,
          label: item.content,
        }))}
      />
      <AttentionCard
        id="attention-urgent"
        href="/tasks"
        linkLabel="Open Tasks"
        icon={<CheckSquare2 aria-hidden="true" className="size-4" />}
        title="Urgent tasks"
        detail={
          urgent === null
            ? "Tasks are unavailable right now."
            : urgentItems.length
              ? `${urgent.overdue.length} overdue · ${urgent.today.length}${urgent.more ? "+" : ""} due today`
              : "No overdue or due-today tasks."
        }
        items={urgentItems}
      />
      <AttentionCard
        id="attention-calendar"
        // At the next event's day, so it is in view.
        href={nextEvent?.kind === "event" ? nextEvent.href : "/calendar"}
        linkLabel="Open Calendar"
        icon={<CalendarDays aria-hidden="true" className="size-4" />}
        title="Next on your calendar"
        detail={
          nextEvent === null
            ? "Calendar is unavailable right now."
            : nextEvent.kind === "none"
              ? "Nothing scheduled in the next 30 days."
              : eventTimeLabel(nextEvent.event, timeZone)
        }
        items={
          nextEvent?.kind === "event"
            ? [
                {
                  id: nextEvent.event.id,
                  label: nextEvent.event.title,
                  // Imported events name their source, such as "ZAW".
                  hint: nextEvent.sourceName,
                },
              ]
            : undefined
        }
      />
      <AttentionCard
        id="attention-unscheduled"
        href="/tasks"
        linkLabel="Plan unscheduled tasks"
        icon={<ListTodo aria-hidden="true" className="size-4" />}
        title="Unscheduled tasks"
        detail={
          unscheduled === null
            ? "Tasks are unavailable right now."
            : unscheduled.items.length
              ? `${plural(unscheduled.items.length, unscheduled.more, "open task")} without a date`
              : unscheduled.more
                ? "See Tasks for undated work."
                : "Every open task has a date."
        }
        items={unscheduled?.items.map((task) => ({
          id: task.id,
          label: task.title,
        }))}
      />
    </div>
  );
}

function AttentionCard({
  detail,
  href,
  icon,
  id,
  items,
  linkLabel,
  title,
}: {
  detail: string;
  href: string;
  icon: React.ReactNode;
  id: string;
  items?: AttentionItem[];
  linkLabel: string;
  title: string;
}) {
  return (
    <Card aria-labelledby={`${id}-title`} id={id} role="region" size="sm">
      <CardContent className="space-y-1.5">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          {icon}
          <h2 className="text-sm font-semibold" id={`${id}-title`}>
            {title}
          </h2>
          <Link
            className="text-muted-foreground hover:text-foreground ml-auto inline-flex items-center gap-1 text-xs"
            href={href}
          >
            {linkLabel} <ArrowRight aria-hidden="true" className="size-3" />
          </Link>
        </div>
        <p className="text-muted-foreground">{detail}</p>
        {items?.length ? (
          <ul className="space-y-1">
            {items.map((item) => (
              <li
                className="flex items-center justify-between gap-3"
                key={item.id}
              >
                <span className="truncate">{item.label}</span>
                {item.hint ? (
                  <span className="text-muted-foreground max-w-[50%] shrink-0 truncate text-xs">
                    {item.hint}
                  </span>
                ) : null}
              </li>
            ))}
          </ul>
        ) : null}
      </CardContent>
    </Card>
  );
}
