"use client";

import {
  CalendarDays,
  CalendarRange,
  ChevronDown,
  ChevronRight,
  List,
} from "lucide-react";
import type { ReactNode } from "react";
import { useEffect, useMemo, useState } from "react";
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
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { usePersistentToggle } from "@/lib/persistent-toggle";
import type { Task } from "@/lib/tasks-summary";
import {
  OPEN_TASKS_CAP,
  RECENT_COMPLETED_DAYS,
  type TasksWorkspaceState,
  dayLabel,
  daysWithTasks,
  dueDay,
  fetchTaskPage,
  focusNotice,
  groupTasks,
  recentCompletedSince,
  relativeDayLabel,
  splitCompleted,
  weekOf,
} from "@/lib/tasks-workspace";
import { dayKey, startOfDay } from "@/lib/time-zone";
import { cn } from "@/lib/utils";

type View = "list" | "schedule";

/** Undated Tasks listed before "Show all". */
const UNDATED_PREVIEW = 5;
/** Older completed Tasks revealed per "Show older". */
const OLDER_STEP = 20;
/** localStorage keys for the sections' expanded state. */
const UNDATED_EXPANDED = "lifeos.tasks.undated.expanded";
const COMPLETED_EXPANDED = "lifeos.tasks.completed.expanded";
const EMPTY =
  "text-muted-foreground rounded-lg border border-dashed px-3 py-2 text-sm";

// A day with open Tasks gets a small dot under its number.
const HAS_TASKS =
  "after:pointer-events-none after:absolute after:bottom-0.5 after:left-1/2 after:z-20 after:size-1 after:-translate-x-1/2 after:rounded-full after:bg-primary data-[outside=true]:after:bg-muted-foreground data-[selected=true]:after:bg-primary-foreground";

type RowProps = {
  today: string;
  timeZone: string;
  onChange: (task: Task) => void;
  onDeleted: (id: string) => void;
  /** The Task the page's link names, highlighted (see TaskFocus). */
  focusId?: string;
};

type SectionProps = RowProps & {
  title: string;
  tasks: Task[];
  empty: string;
  action?: ReactNode;
  alert?: boolean;
  testId?: string;
};

function SectionHeading({
  title,
  count,
  alert,
  action,
}: {
  title: string;
  count: ReactNode;
  alert?: boolean;
  action?: ReactNode;
}) {
  return (
    <div className="mb-1.5 flex min-h-6 items-center gap-2">
      <h2 className={cn("text-sm font-semibold", alert && "text-destructive")}>
        {title}
      </h2>
      <span className="text-muted-foreground text-xs tabular-nums">
        {count}
      </span>
      {action ? <div className="ml-auto">{action}</div> : null}
    </div>
  );
}

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
      <SectionHeading
        title={title}
        count={tasks.length}
        alert={alert && tasks.length > 0}
        action={action}
      />
      {tasks.length ? (
        <TaskList tasks={tasks} {...row} />
      ) : (
        <p className={EMPTY}>{empty}</p>
      )}
    </section>
  );
}

/**
 * Undated open Tasks, newest first. The section is always shown with the
 * full count; it lists the newest few until the owner asks for all of them,
 * or lists them all while the linked Task is beyond the newest few (without
 * remembering that as the owner's choice).
 */
function UndatedSection({
  tasks,
  capped,
  ...row
}: RowProps & { tasks: Task[]; capped: boolean }) {
  const [stored, setExpanded] = usePersistentToggle(UNDATED_EXPANDED);
  const [revealed, setRevealed] = useState(
    () => tasks.findIndex((task) => task.id === row.focusId) >= UNDATED_PREVIEW,
  );
  const expanded = stored || revealed;
  const title = "Unscheduled / No date";
  const collapsible = tasks.length > UNDATED_PREVIEW;
  const shown =
    expanded || !collapsible ? tasks : tasks.slice(0, UNDATED_PREVIEW);
  return (
    <section aria-label={title} data-testid="tasks-undated">
      <SectionHeading
        title={title}
        count={`${tasks.length}${capped ? "+" : ""}`}
      />
      {tasks.length ? (
        <div className="space-y-1.5">
          <div id="tasks-undated-list">
            <TaskList tasks={shown} {...row} />
          </div>
          {collapsible ? (
            <Button
              aria-controls="tasks-undated-list"
              aria-expanded={expanded}
              className="aria-expanded:bg-transparent"
              size="xs"
              variant="ghost"
              onClick={() => {
                setRevealed(false);
                setExpanded(!expanded);
              }}
            >
              {expanded ? "Show fewer" : `Show all ${tasks.length}`}
            </Button>
          ) : null}
        </div>
      ) : (
        <p className={EMPTY}>No unscheduled Tasks.</p>
      )}
    </section>
  );
}

/**
 * Completed Tasks, collapsed by default. The heading counts those completed
 * in the last seven days (owner's days); expanded, it lists them and offers
 * older ones a page at a time, loading more from the Tasks API when the
 * loaded ones run out. A Task leaves it only when it is reopened or deleted.
 */
function CompletedSection({
  recent,
  older,
  more,
  unavailable,
  onShowOlder,
  ...row
}: RowProps & {
  recent: Task[];
  older: Task[];
  /** Older completed Tasks exist beyond those loaded. */
  more: boolean;
  unavailable: boolean;
  /** Loads the next page of older completed Tasks; false when it failed. */
  onShowOlder: () => Promise<boolean>;
}) {
  const [expanded, setExpanded] = usePersistentToggle(COMPLETED_EXPANDED);
  const [olderShown, setOlderShown] = useState(0);
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState(false);
  const title = "Completed";
  const showOlder = async () => {
    const wanted = olderShown + OLDER_STEP;
    if (older.length < wanted && more) {
      setLoading(true);
      setFailed(false);
      const loaded = await onShowOlder();
      setLoading(false);
      if (!loaded) return setFailed(true);
    }
    setOlderShown(wanted);
  };
  // When every loaded completed Task is recent and the API has more, loading
  // stopped at the cap, so the recent count is a lower bound.
  const count = unavailable
    ? "unavailable"
    : `${recent.length}${more && older.length === 0 ? "+" : ""} this week`;
  return (
    <section aria-label={title} data-testid="tasks-completed">
      <h2 className="mb-1.5">
        <Button
          aria-controls="tasks-completed-list"
          aria-expanded={expanded}
          className="-ml-2 gap-2 font-semibold aria-expanded:bg-transparent"
          size="sm"
          variant="ghost"
          onClick={() => setExpanded(!expanded)}
        >
          <ChevronRight
            aria-hidden="true"
            className={cn("transition-transform", expanded && "rotate-90")}
          />
          {title}
          <span className="text-muted-foreground text-xs font-normal tabular-nums">
            {count}
          </span>
        </Button>
      </h2>
      <div className="space-y-1.5" hidden={!expanded} id="tasks-completed-list">
        {!expanded ? null : unavailable ? (
          <p className={EMPTY}>Completed Tasks are unavailable right now.</p>
        ) : (
          <>
            <p className="text-muted-foreground text-xs">
              Completed in the last {RECENT_COMPLETED_DAYS} days
            </p>
            {recent.length ? (
              <TaskList tasks={recent} {...row} />
            ) : (
              <p className={EMPTY}>
                Nothing completed in the last {RECENT_COMPLETED_DAYS} days.
              </p>
            )}
            {olderShown > 0 && older.length ? (
              <>
                <p className="text-muted-foreground pt-1 text-xs">Older</p>
                <TaskList tasks={older.slice(0, olderShown)} {...row} />
              </>
            ) : null}
            {older.length > olderShown || more ? (
              <Button
                disabled={loading}
                size="xs"
                variant="ghost"
                onClick={showOlder}
              >
                {loading ? "Loading…" : "Show older"}
              </Button>
            ) : null}
            {failed ? (
              <p className="text-destructive text-sm">
                Could not load older completed Tasks.
              </p>
            ) : null}
          </>
        )}
      </div>
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
  const focus = state.kind === "ready" ? state.focus : undefined;
  // The linked open Task, listed even when it is beyond the open-Task cap.
  const focusId = focus?.kind === "open" ? focus.task.id : undefined;
  const [tasks, setTasks] = useState(() => {
    if (state.kind !== "ready") return [];
    const loaded = [...state.open, ...(state.completed?.items ?? [])];
    return focus?.kind === "open" &&
      !loaded.some((task) => task.id === focus.task.id)
      ? [...loaded, focus.task]
      : loaded;
  });
  // A link to a Task that is not open says so, until dismissed.
  const [noticeShown, setNoticeShown] = useState(true);
  const notice = noticeShown ? focusNotice(focus, today, timeZone) : undefined;
  // Scroll to the linked Task once it is rendered (every open Task is in the
  // List view, which the page opens with).
  useEffect(() => {
    if (focusId)
      document
        .getElementById(`task-${focusId}`)
        ?.scrollIntoView({ block: "center" });
  }, [focusId]);
  // Continues the completed list with older Tasks than those loaded.
  const [completedCursor, setCompletedCursor] = useState(() =>
    state.kind === "ready" ? (state.completed?.nextCursor ?? null) : null,
  );
  const onChange = (updated: Task) =>
    setTasks((current) =>
      current.map((task) => (task.id === updated.id ? updated : task)),
    );
  // A deleted Task leaves every section and the schedule at once.
  const onDeleted = (id: string) =>
    setTasks((current) => current.filter((task) => task.id !== id));
  const loadOlderCompleted = async () => {
    if (!completedCursor) return true;
    const page = await fetchTaskPage(true, completedCursor);
    if (!page) return false;
    setTasks((current) => {
      const known = new Set(current.map((task) => task.id));
      return [...current, ...page.items.filter((task) => !known.has(task.id))];
    });
    setCompletedCursor(page.nextCursor);
    return true;
  };
  const groups = useMemo(
    () => groupTasks(tasks, { today, selected, timeZone }),
    [tasks, today, selected, timeZone],
  );
  const completed = useMemo(
    () =>
      splitCompleted(groups.completed, recentCompletedSince(today, timeZone)),
    [groups.completed, today, timeZone],
  );
  const taskDays = useMemo(
    () => daysWithTasks(tasks, timeZone),
    [tasks, timeZone],
  );
  const choose = (day: string) => {
    setSelected(day);
    setMonth(day);
  };
  const row = { today, timeZone, onChange, onDeleted, focusId };

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

  const capped = state.openCapped;
  const unscheduled = (
    <UndatedSection tasks={groups.unscheduled} capped={capped} {...row} />
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
        <ToggleGroup
          aria-label="View"
          size="sm"
          spacing={0}
          variant="outline"
          value={[view]}
          onValueChange={(chosen) => {
            // Pressing the current view again would leave none pressed.
            if (chosen[0] === "list" || chosen[0] === "schedule")
              setView(chosen[0]);
          }}
        >
          <ToggleGroupItem value="list">
            <List />
            List
          </ToggleGroupItem>
          <ToggleGroupItem value="schedule">
            <CalendarRange />
            Schedule
          </ToggleGroupItem>
        </ToggleGroup>
      </header>

      {notice ? (
        <div
          className={cn(EMPTY, "flex items-center gap-2")}
          data-testid="tasks-link-notice"
          role="status"
        >
          <p className="min-w-0 flex-1 wrap-anywhere">{notice}</p>
          <Button
            size="xs"
            variant="ghost"
            onClick={() => setNoticeShown(false)}
          >
            Dismiss
          </Button>
        </div>
      ) : null}

      {capped ? (
        <p className={EMPTY} role="note">
          Only the first {OPEN_TASKS_CAP} open Tasks, soonest due first, are
          shown. Later and undated Tasks beyond them are not listed here.
        </p>
      ) : null}

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
              <CompletedSection
                recent={completed.recent}
                older={completed.older}
                more={completedCursor !== null}
                unavailable={!state.completed}
                onShowOlder={loadOlderCompleted}
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
