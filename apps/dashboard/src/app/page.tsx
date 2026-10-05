import {
  CalendarDays,
  CheckSquare2,
  Inbox,
  Landmark,
  Mic,
  Plus,
  type LucideIcon,
} from "lucide-react";

const workspaces: ReadonlyArray<{
  id: string;
  title: string;
  description: string;
  emptyState: string;
  icon: LucideIcon;
}> = [
  {
    id: "finance",
    title: "Finance",
    description: "Accounts, transactions, and budgets will live here.",
    emptyState: "Waiting for the Finance service",
    icon: Landmark,
  },
  {
    id: "tasks",
    title: "Tasks",
    description: "Priorities and next actions will be gathered here.",
    emptyState: "Waiting for the Tasks service",
    icon: CheckSquare2,
  },
  {
    id: "inbox",
    title: "Inbox",
    description: "New items that need review will arrive here.",
    emptyState: "Waiting for the Inbox service",
    icon: Inbox,
  },
  {
    id: "calendar",
    title: "Calendar",
    description: "Upcoming events and time commitments will appear here.",
    emptyState: "Waiting for the Calendar service",
    icon: CalendarDays,
  },
];

export default function Home() {
  return (
    <div className="mx-auto max-w-6xl space-y-6 sm:space-y-8">
      <section
        className="bg-card scroll-mt-24 overflow-hidden rounded-3xl border shadow-sm"
        id="overview"
      >
        <div className="p-6 sm:p-8 lg:p-10">
          <p className="text-muted-foreground text-sm font-medium">Overview</p>
          <h1 className="mt-2 max-w-3xl text-3xl font-semibold tracking-tight sm:text-4xl lg:text-5xl">
            Your life, in one clear view.
          </h1>
          <p className="text-muted-foreground mt-4 max-w-2xl text-base leading-7">
            A calm home for the things that need your attention. Connect each
            service as it becomes available; this dashboard will never fill the
            gaps with made-up data.
          </p>
        </div>

        <div className="bg-muted/50 grid border-t sm:grid-cols-2 sm:divide-x">
          <div className="border-b p-5 sm:border-b-0 sm:p-6">
            <div className="flex items-center gap-3">
              <span className="bg-background flex size-10 items-center justify-center rounded-xl border shadow-sm">
                <Plus aria-hidden="true" className="size-4" />
              </span>
              <div>
                <p className="text-sm font-medium">Quick capture</p>
                <p className="text-muted-foreground text-xs">Planned control</p>
              </div>
            </div>
          </div>
          <div className="p-5 sm:p-6">
            <div className="flex items-center gap-3">
              <span className="bg-background flex size-10 items-center justify-center rounded-xl border shadow-sm">
                <Mic aria-hidden="true" className="size-4" />
              </span>
              <div>
                <p className="text-sm font-medium">Voice capture</p>
                <p className="text-muted-foreground text-xs">Planned control</p>
              </div>
            </div>
          </div>
        </div>
      </section>

      <section aria-labelledby="workspace-heading">
        <div className="mb-4 flex items-end justify-between gap-4">
          <div>
            <p className="text-muted-foreground text-sm font-medium">
              Workspaces
            </p>
            <h2
              className="mt-1 text-2xl font-semibold tracking-tight"
              id="workspace-heading"
            >
              The systems behind your day
            </h2>
          </div>
          <p className="text-muted-foreground hidden text-sm sm:block">
            Ready for service connections
          </p>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          {workspaces.map(
            ({ description, emptyState, icon: Icon, id, title }) => (
              <article
                className="bg-card scroll-mt-24 rounded-2xl border p-5 shadow-sm sm:p-6"
                id={id}
                key={id}
              >
                <div className="flex items-start justify-between gap-4">
                  <span className="bg-secondary text-secondary-foreground flex size-11 shrink-0 items-center justify-center rounded-xl">
                    <Icon aria-hidden="true" className="size-5" />
                  </span>
                  <span className="text-muted-foreground rounded-full border px-2.5 py-1 text-xs font-medium">
                    Not connected
                  </span>
                </div>
                <h3 className="mt-6 text-lg font-semibold">{title}</h3>
                <p className="text-muted-foreground mt-1 min-h-10 text-sm leading-5">
                  {description}
                </p>
                <div className="bg-muted/60 text-muted-foreground mt-5 rounded-xl border border-dashed p-4 text-sm">
                  {emptyState}
                </div>
              </article>
            ),
          )}
        </div>
      </section>
    </div>
  );
}
