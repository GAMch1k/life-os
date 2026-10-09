import Link from "next/link";
import {
  AlertTriangle,
  ArrowRight,
  CalendarDays,
  CheckSquare2,
  Inbox,
} from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import type { HomeAttention } from "@/lib/home-attention";

export function HomeAttentionSurface({
  attention,
}: {
  attention: HomeAttention;
}) {
  const urgent = [
    ...(attention.overdue ?? []),
    ...(attention.today ?? []),
  ].slice(0, 5);
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
          href="/inbox"
          icon={<AlertTriangle className="size-4 text-amber-600" />}
          title="Human Review"
          detail={
            attention.humanReview === null
              ? "Inbox is unavailable"
              : attention.humanReview.length
                ? `${attention.humanReview.length} capture${attention.humanReview.length === 1 ? " needs" : "s need"} a decision`
                : "Nothing waiting for review"
          }
          items={attention.humanReview?.slice(0, 2).map((item) => item.content)}
        />
        <AttentionCard
          href="/tasks"
          icon={<CheckSquare2 className="size-4" />}
          title="Urgent tasks"
          detail={
            attention.overdue === null
              ? "Tasks are unavailable"
              : urgent.length
                ? `${attention.overdue.length} overdue · ${attention.today?.length ?? 0} due today`
                : "No overdue or due-today tasks"
          }
          items={urgent.map((task) => task.title)}
        />
        <AttentionCard
          href="/calendar"
          icon={<CalendarDays className="size-4" />}
          title="Next on your calendar"
          detail={
            attention.nextEvent === undefined
              ? "Calendar is unavailable"
              : attention.nextEvent
                ? attention.nextEvent.title
                : "No upcoming event"
          }
        />
        <AttentionCard
          href="/tasks"
          icon={<Inbox className="size-4" />}
          title="Unscheduled tasks"
          detail={
            attention.unscheduled === null
              ? "Tasks are unavailable"
              : attention.unscheduled.length
                ? "A few open tasks without a date"
                : "No unscheduled tasks"
          }
          items={attention.unscheduled?.map((task) => task.title)}
        />
      </div>
    </div>
  );
}
function AttentionCard({
  detail,
  href,
  icon,
  items,
  title,
}: {
  detail: string;
  href: string;
  icon: React.ReactNode;
  items?: string[];
  title: string;
}) {
  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center gap-2">
          {icon}
          <CardTitle className="text-base">{title}</CardTitle>
        </div>
        <CardDescription>{detail}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-2">
        {items?.map((item) => (
          <p className="truncate text-sm" key={item}>
            {item}
          </p>
        ))}
        <Link
          className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 pt-1 text-sm"
          href={href}
        >
          Open workspace <ArrowRight className="size-3.5" />
        </Link>
      </CardContent>
    </Card>
  );
}
