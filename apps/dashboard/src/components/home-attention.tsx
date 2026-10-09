import Link from "next/link";
import {
  AlertTriangle,
  ArrowRight,
  CalendarDays,
  CheckSquare2,
  ListTodo,
} from "lucide-react";

import { eventTimeLabel } from "@/components/calendar-summary";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import type { HomeAttention } from "@/lib/home-attention";

const URGENT_PREVIEW = 5;
const REVIEW_PREVIEW = 2;

type AttentionItem = { id: string; label: string; hint?: string };

function plural(count: number, more: boolean, noun: string) {
  return `${count}${more ? "+" : ""} ${noun}${count === 1 && !more ? "" : "s"}`;
}

export function HomeAttentionSurface({
  attention,
}: {
  attention: HomeAttention;
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

  return (
    <div className="mx-auto max-w-5xl space-y-6">
      <section>
        <p className="text-muted-foreground text-sm font-medium">Overview</p>
        <h1 className="mt-1 text-3xl font-semibold tracking-tight">
          What needs your attention?
        </h1>
        <p className="text-muted-foreground mt-2 max-w-2xl">
          A short control surface for decisions and deadlines. Use the dedicated
          workspaces for the full picture.
        </p>
      </section>
      <div className="grid gap-4 md:grid-cols-2">
        <AttentionCard
          id="attention-review"
          href="/inbox"
          linkLabel="Review in Inbox"
          icon={<AlertTriangle className="size-4 text-amber-600" />}
          title="Human Review"
          detail={
            humanReview === null
              ? "Inbox is unavailable right now."
              : humanReview.items.length
                ? `${plural(humanReview.items.length, humanReview.more, "capture")} waiting for your decision`
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
          icon={<CheckSquare2 className="size-4" />}
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
          href="/calendar"
          linkLabel="Open Calendar"
          icon={<CalendarDays className="size-4" />}
          title="Next on your calendar"
          detail={
            nextEvent === null
              ? "Calendar is unavailable right now."
              : nextEvent.kind === "none"
                ? "Nothing scheduled in the next 30 days."
                : eventTimeLabel(nextEvent.event)
          }
          items={
            nextEvent?.kind === "event"
              ? [{ id: nextEvent.event.id, label: nextEvent.event.title }]
              : undefined
          }
        />
        <AttentionCard
          id="attention-unscheduled"
          href="/tasks"
          linkLabel="Plan unscheduled tasks"
          icon={<ListTodo className="size-4" />}
          title="Unscheduled tasks"
          detail={
            unscheduled === null
              ? "Tasks are unavailable right now."
              : unscheduled.items.length
                ? `Open tasks without a date${unscheduled.more ? ", and more in Tasks" : ""}.`
                : unscheduled.more
                  ? "See the Tasks workspace for undated work."
                  : "Every open task has a date."
          }
          items={unscheduled?.items.map((task) => ({
            id: task.id,
            label: task.title,
          }))}
        />
      </div>
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
    <Card aria-labelledby={`${id}-title`} id={id} role="region">
      <CardHeader className="pb-3">
        <div className="flex items-center gap-2">
          {icon}
          <CardTitle className="text-base" id={`${id}-title`}>
            {title}
          </CardTitle>
        </div>
        <CardDescription>{detail}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-2">
        {items?.length ? (
          <ul className="space-y-1.5">
            {items.map((item) => (
              <li
                className="flex items-center justify-between gap-3 text-sm"
                key={item.id}
              >
                <span className="truncate">{item.label}</span>
                {item.hint ? (
                  <span className="text-muted-foreground shrink-0 text-xs">
                    {item.hint}
                  </span>
                ) : null}
              </li>
            ))}
          </ul>
        ) : null}
        <Link
          className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 pt-1 text-sm"
          href={href}
        >
          {linkLabel} <ArrowRight aria-hidden="true" className="size-3.5" />
        </Link>
      </CardContent>
    </Card>
  );
}
