"use client";

import { CalendarDays, CalendarRange, ChevronDown, List } from "lucide-react";
import type { ReactNode } from "react";
import { useMemo, useState } from "react";
import { enGB } from "react-day-picker/locale/en-GB";

import { TaskList } from "@/components/task-row";
import { Button } from "@/components/ui/button";
import { Calendar } from "@/components/ui/calendar";
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import type { Task } from "@/lib/tasks-summary";
import {
  type TasksWorkspaceState,
  dayLabel,
  daysWithTasks,
  dueDay,
  groupTasks,
  relativeDayLabel,
  weekOf,
} from "@/lib/tasks-workspace";
import { dayKey, startOfDay } from "@/lib/time-zone";
import { cn } from "@/lib/utils";

type View = "list" | "schedule";

// A day with open Tasks gets a small dot under its number.
const HAS_TASKS =
  "after:pointer-events-none after:absolute after:bottom-0.5 after:left-1/2 after:z-20 after:size-1 after:-translate-x-1/2 after:rounded-full after:bg-primary data-[outside=true]:after:bg-muted-foreground data-[selected=true]:after:bg-primary-foreground";

type SectionProps = {
  title: string;
  tasks: Task[];
  empty: string;
  today: string;
  timeZone: string;
  onChange: (task: Task) => void;
  action?: ReactNode;
  alert?: boolean;
  testId?: string;
};

function TaskSection({
  title,
  tasks,
  empty,
  action,
  alert,
  testId,
  ...row
}: SectionProps) {
  return (
    <section aria-label={title} data-testid={testId}>
      <div className="mb-1.5 flex min-h-6 items-center gap-2">
        <h2
          className={cn(
            "text-sm font-semibold",
            alert && tasks.length > 0 && "text-destructive",
          )}
        >
          {title}
        </h2>
        <span className="text-muted-foreground text-xs tabular-nums">
          {tasks.length}
        </span>
        {action ? <div className="ml-auto">{action}</div> : null}
      </div>
      {tasks.length ? (
        <TaskList tasks={tasks} {...row} />
      ) : (
        <p className="text-muted-foreground rounded-lg border border-dashed px-3 py-2 text-sm">
          {empty}
        </p>
      )}
    </section>
  );
}

export function TasksWorkspace({
  state,
  timeZone,
  today,
}: {
  state: TasksWorkspaceState;
  /** The owner's timezone; every day and time on the page is in it. */
  timeZone: string;
  /** The owner's today (YYYY-MM-DD), decided once by the server render. */
  today: string;
}) {
  const [view, setView] = useState<View>("list");
  const [selected, setSelected] = useState(today);
  const [month, setMonth] = useState(today);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [tasks, setTasks] = useState(() =>
    state.kind === "ready" ? state.tasks : [],
  );
  const onChange = (updated: Task) =>
    setTasks((current) =>
      current.map((task) => (task.id === updated.id ? updated : task)),
    );
  const groups = useMemo(
    () => groupTasks(tasks, { today, selected, timeZone }),
    [tasks, today, selected, timeZone],
  );
  const taskDays = useMemo(
    () => daysWithTasks(tasks, timeZone),
    [tasks, timeZone],
  );
  const choose = (day: string) => {
    setSelected(day);
    setMonth(day);
  };
  const row = { today, timeZone, onChange };

  if (state.kind === "unavailable")
    return (
      <div className="mx-auto max-w-6xl space-y-4">
        <h1 className="text-xl font-semibold tracking-tight">Tasks</h1>
        <Card>
          <CardHeader>
            <CardTitle>Tasks are unavailable</CardTitle>
            <CardDescription>
              Tasks are temporarily unavailable. Your data has not been replaced
              with placeholders.
            </CardDescription>
          </CardHeader>
        </Card>
      </div>
    );

  const unscheduled = (
    <TaskSection
      title="Unscheduled / No date"
      tasks={groups.unscheduled}
      empty="No unscheduled Tasks."
      {...row}
    />
  );
  const selectedLabel = relativeDayLabel(selected, today);

  return (
    <div
      className="mx-auto max-w-6xl space-y-4"
      data-testid="tasks-workspace"
      data-time-zone={timeZone}
      data-today={today}
    >
      <header className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-xl font-semibold tracking-tight">Tasks</h1>
        <div
          aria-label="View"
          className="bg-background flex rounded-lg border p-0.5"
          role="group"
        >
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
      </header>

      <div className="grid gap-4 lg:grid-cols-[auto_minmax(0,1fr)] lg:items-start lg:gap-6">
        <div className="space-y-2">
          <Button
            aria-controls="tasks-date-picker"
            aria-expanded={pickerOpen}
            className="w-full justify-between sm:w-56 lg:hidden"
            variant="outline"
            onClick={() => setPickerOpen((open) => !open)}
          >
            <span className="flex items-center gap-2">
              <CalendarDays />
              {selectedLabel}
            </span>
            <ChevronDown
              className={cn("transition-transform", pickerOpen && "rotate-180")}
            />
          </Button>
          <div
            className={cn(!pickerOpen && "hidden", "lg:block")}
            id="tasks-date-picker"
          >
            <Calendar
              mode="single"
              required
              selected={startOfDay(selected, timeZone)}
              onSelect={(date) => choose(dayKey(date, timeZone))}
              month={startOfDay(month, timeZone)}
              onMonthChange={(date) => setMonth(dayKey(date, timeZone))}
              today={startOfDay(today, timeZone)}
              timeZone={timeZone}
              locale={enGB}
              weekStartsOn={1}
              modifiers={{
                hasTasks: (date: Date) => taskDays.has(dayKey(date, timeZone)),
              }}
              modifiersClassNames={{ hasTasks: HAS_TASKS }}
              className="rounded-lg border"
              data-testid="tasks-calendar"
            />
          </div>
        </div>

        <div className="min-w-0">
          {view === "list" ? (
            <div className="grid gap-5">
              {selected !== today ? (
                <TaskSection
                  title={selectedLabel}
                  tasks={groups.selected}
                  empty={`Nothing due on ${dayLabel(selected, today)}.`}
                  testId="tasks-selected-day"
                  action={
                    <Button
                      size="xs"
                      variant="ghost"
                      onClick={() => choose(today)}
                    >
                      Back to today
                    </Button>
                  }
                  {...row}
                />
              ) : null}
              <TaskSection
                title="Overdue"
                tasks={groups.overdue}
                empty="No overdue Tasks."
                alert
                {...row}
              />
              <TaskSection
                title="Today"
                tasks={groups.today}
                empty="Nothing due today."
                testId={selected === today ? "tasks-selected-day" : undefined}
                {...row}
              />
              <TaskSection
                title="Upcoming"
                tasks={groups.upcoming}
                empty="No upcoming Tasks."
                {...row}
              />
              {unscheduled}
              <TaskSection
                title="Completed"
                tasks={groups.completed}
                empty="No completed Tasks."
                {...row}
              />
            </div>
          ) : (
            <div className="grid gap-5">
              <section aria-label="Schedule view" className="grid gap-4">
                <h2 className="sr-only">Schedule</h2>
                {weekOf(selected).map((day) => {
                  const scheduled = tasks.filter(
                    (task) =>
                      !task.completed && dueDay(task.dueAt, timeZone) === day,
                  );
                  const label = dayLabel(day, today);
                  return (
                    <section
                      aria-current={day === today ? "date" : undefined}
                      aria-label={label}
                      data-testid={`schedule-day-${day}`}
                      key={day}
                    >
                      <div className="mb-1.5 flex min-h-6 items-center gap-2">
                        <h3
                          className={cn(
                            "text-sm font-semibold",
                            day === selected && "text-primary",
                          )}
                        >
                          {label}
                        </h3>
                        {day === today ? (
                          <span className="text-muted-foreground text-xs">
                            Today
                          </span>
                        ) : null}
                        <span className="text-muted-foreground ml-auto text-xs tabular-nums">
                          {scheduled.length}
                        </span>
                      </div>
                      {scheduled.length ? (
                        <TaskList tasks={scheduled} {...row} />
                      ) : (
                        <p className="text-muted-foreground px-3 text-sm">
                          No Tasks.
                        </p>
                      )}
                    </section>
                  );
                })}
              </section>
              {unscheduled}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
