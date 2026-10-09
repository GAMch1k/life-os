"use client";

import {
  CalendarDays,
  ChevronLeft,
  ChevronRight,
  List,
  CalendarRange,
} from "lucide-react";
import { useMemo, useState } from "react";

import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import type { Task } from "@/lib/tasks-summary";
import type { TasksWorkspaceState } from "@/lib/tasks-workspace";
import { cn } from "@/lib/utils";

type View = "list" | "schedule";

const dayKey = (value: Date) => value.toLocaleDateString("en-CA");
const dateOnly = (value: string) => new Date(value);
const isSameDay = (left: Date, right: Date) => dayKey(left) === dayKey(right);
const dateLabel = (date: Date) =>
  date.toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  });

function startOfDay(value: Date) {
  const result = new Date(value);
  result.setHours(0, 0, 0, 0);
  return result;
}
function addDays(value: Date, days: number) {
  const result = new Date(value);
  result.setDate(result.getDate() + days);
  return result;
}
function dueLabel(task: Task) {
  return task.dueAt
    ? dateOnly(task.dueAt).toLocaleDateString(undefined, {
        month: "short",
        day: "numeric",
        hour: "numeric",
        minute: "2-digit",
      })
    : "No date";
}

function TaskRow({ task }: { task: Task }) {
  return (
    <li className="flex items-center justify-between gap-3 border-b py-3 last:border-0">
      <div className="min-w-0">
        <p className="font-medium">{task.title}</p>
        {task.notes ? (
          <p className="text-muted-foreground mt-0.5 truncate text-sm">
            {task.notes}
          </p>
        ) : null}
      </div>
      <time className="text-muted-foreground shrink-0 text-xs">
        {dueLabel(task)}
      </time>
    </li>
  );
}

function TaskSection({
  title,
  tasks,
  empty,
}: {
  title: string;
  tasks: Task[];
  empty?: string;
}) {
  return (
    <section aria-label={title}>
      <div className="mb-2 flex items-center justify-between">
        <h2 className="font-semibold">{title}</h2>
        <span className="text-muted-foreground text-sm">{tasks.length}</span>
      </div>
      {tasks.length ? (
        <ul className="rounded-lg border px-3">
          <>
            {tasks.map((task) => (
              <TaskRow key={task.id} task={task} />
            ))}
          </>
        </ul>
      ) : (
        <p className="text-muted-foreground rounded-lg border border-dashed p-3 text-sm">
          {empty ?? "Nothing here."}
        </p>
      )}
    </section>
  );
}

export function TasksWorkspace({ state }: { state: TasksWorkspaceState }) {
  const [view, setView] = useState<View>("list");
  const [selected, setSelected] = useState(() => startOfDay(new Date()));
  const [month, setMonth] = useState(
    () => new Date(selected.getFullYear(), selected.getMonth(), 1),
  );
  const tasks = useMemo(
    () => (state.kind === "ready" ? state.tasks : []),
    [state],
  );
  const groups = useMemo(() => {
    const today = startOfDay(new Date());
    const tomorrow = addDays(today, 1);
    return {
      overdue: tasks.filter(
        (task) => task.dueAt && dateOnly(task.dueAt) < today,
      ),
      today: tasks.filter(
        (task) =>
          task.dueAt &&
          dateOnly(task.dueAt) >= today &&
          dateOnly(task.dueAt) < tomorrow,
      ),
      upcoming: tasks.filter(
        (task) => task.dueAt && dateOnly(task.dueAt) >= tomorrow,
      ),
      unscheduled: tasks.filter((task) => !task.dueAt),
    };
  }, [tasks]);
  const days = useMemo(() => {
    const count = new Date(
      month.getFullYear(),
      month.getMonth() + 1,
      0,
    ).getDate();
    return Array.from(
      { length: count },
      (_, i) => new Date(month.getFullYear(), month.getMonth(), i + 1),
    );
  }, [month]);
  const week = useMemo(
    () => Array.from({ length: 7 }, (_, i) => addDays(selected, i)),
    [selected],
  );
  const choose = (day: Date) => {
    setSelected(startOfDay(day));
    setMonth(new Date(day.getFullYear(), day.getMonth(), 1));
  };

  if (state.kind === "unavailable")
    return (
      <Card>
        <CardHeader>
          <CardTitle>Tasks</CardTitle>
          <CardDescription>
            Tasks are temporarily unavailable. Your data has not been replaced
            with placeholders.
          </CardDescription>
        </CardHeader>
      </Card>
    );
  return (
    <div className="mx-auto max-w-6xl space-y-6">
      <header>
        <p className="text-muted-foreground text-sm font-medium">
          Tasks workspace
        </p>
        <h1 className="mt-1 text-3xl font-semibold tracking-tight">
          Plan the work in front of you
        </h1>
        <p className="text-muted-foreground mt-2">
          Tasks stay independent from Calendar events. Unscheduled work is
          always visible here.
        </p>
      </header>
      <div className="grid gap-6 lg:grid-cols-[17rem_minmax(0,1fr)]">
        <Card className="h-fit">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <CalendarDays className="size-4" />
              Date navigator
            </CardTitle>
            <CardDescription>
              {month.toLocaleDateString(undefined, {
                month: "long",
                year: "numeric",
              })}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="mb-3 flex justify-between">
              <Button
                aria-label="Previous month"
                variant="ghost"
                size="icon-sm"
                onClick={() =>
                  setMonth(
                    new Date(month.getFullYear(), month.getMonth() - 1, 1),
                  )
                }
              >
                <ChevronLeft />
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => choose(new Date())}
              >
                Today
              </Button>
              <Button
                aria-label="Next month"
                variant="ghost"
                size="icon-sm"
                onClick={() =>
                  setMonth(
                    new Date(month.getFullYear(), month.getMonth() + 1, 1),
                  )
                }
              >
                <ChevronRight />
              </Button>
            </div>
            <div className="text-muted-foreground grid grid-cols-7 gap-1 text-center text-xs">
              {"SMTWTFS".split("").map((day) => (
                <span key={day}>{day}</span>
              ))}
            </div>
            <div className="mt-1 grid grid-cols-7 gap-1">
              {Array.from({ length: days[0]?.getDay() ?? 0 }, (_, i) => (
                <span key={`blank-${i}`} />
              ))}
              {days.map((day) => (
                <button
                  aria-label={`Focus ${dateLabel(day)}`}
                  data-testid={`task-day-${dayKey(day)}`}
                  className={cn(
                    "hover:bg-muted size-8 rounded-md text-sm",
                    isSameDay(day, selected) &&
                      "bg-primary text-primary-foreground hover:bg-primary",
                  )}
                  key={day.toISOString()}
                  onClick={() => choose(day)}
                >
                  {day.getDate()}
                </button>
              ))}
            </div>
          </CardContent>
        </Card>
        <main className="min-w-0">
          <div className="mb-5 flex flex-wrap items-center justify-between gap-3">
            <div>
              <p className="text-muted-foreground text-sm">Focused date</p>
              <p className="font-medium">{dateLabel(selected)}</p>
            </div>
            <div className="flex rounded-lg border p-1">
              <Button
                aria-pressed={view === "list"}
                variant={view === "list" ? "secondary" : "ghost"}
                size="sm"
                onClick={() => setView("list")}
              >
                <List />
                List
              </Button>
              <Button
                aria-pressed={view === "schedule"}
                variant={view === "schedule" ? "secondary" : "ghost"}
                size="sm"
                onClick={() => setView("schedule")}
              >
                <CalendarRange />
                Schedule
              </Button>
            </div>
          </div>
          {view === "list" ? (
            <div className="grid gap-6">
              <TaskSection
                title="Overdue"
                tasks={groups.overdue}
                empty="No overdue tasks."
              />
              <TaskSection
                title="Today"
                tasks={groups.today}
                empty="Nothing due today."
              />
              <TaskSection
                title="Upcoming"
                tasks={groups.upcoming}
                empty="No upcoming tasks."
              />
              <TaskSection
                title="Unscheduled / No date"
                tasks={groups.unscheduled}
                empty="No unscheduled tasks."
              />
            </div>
          ) : (
            <section aria-label="Schedule view">
              <h2 className="sr-only">Schedule</h2>
              <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-7">
                {week.map((day) => {
                  const scheduled = tasks.filter(
                    (task) =>
                      task.dueAt && isSameDay(dateOnly(task.dueAt), day),
                  );
                  return (
                    <Card
                      key={day.toISOString()}
                      data-testid={`schedule-day-${dayKey(day)}`}
                      className={cn(
                        isSameDay(day, selected) && "ring-primary/30 ring-2",
                      )}
                    >
                      <CardHeader>
                        <CardTitle>
                          {day.toLocaleDateString(undefined, {
                            weekday: "short",
                          })}
                        </CardTitle>
                        <CardDescription>{dateLabel(day)}</CardDescription>
                      </CardHeader>
                      <CardContent>
                        {scheduled.length ? (
                          <ul>
                            {scheduled.map((task) => (
                              <TaskRow key={task.id} task={task} />
                            ))}
                          </ul>
                        ) : (
                          <p className="text-muted-foreground text-sm">
                            No scheduled tasks.
                          </p>
                        )}
                      </CardContent>
                    </Card>
                  );
                })}
              </div>
              <p className="text-muted-foreground mt-4 text-sm">
                Unscheduled Tasks stay in the List view; this schedule is a date
                projection, not a Calendar-event view.
              </p>
            </section>
          )}
        </main>
      </div>
    </div>
  );
}
