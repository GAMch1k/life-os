import { tasksClient } from "@/lib/api/tasks-client";
import type { Task } from "@/lib/tasks-summary";
import { DATE_LOCALE, addDays, dayKey, startOfDay } from "@/lib/time-zone";

/** The Tasks API's largest page. */
export const TASKS_PAGE_SIZE = 100;
/** A safety cap: at most this many open Tasks load with the page. */
export const OPEN_TASKS_CAP = 1000;
/** Recently completed means completed today or in the six days before. */
export const RECENT_COMPLETED_DAYS = 7;

/** A run of Tasks in the API's order; `nextCursor` continues it. */
export type TaskPage = { items: Task[]; nextCursor: string | null };

export type TasksWorkspaceState =
  | {
      kind: "ready";
      /** Every open Task, unless there are more than the cap. */
      open: Task[];
      /** True when more open Tasks exist than were loaded. */
      openCapped: boolean;
      /**
       * Completed Tasks, most recently completed first: every recent one (up
       * to the cap) and possibly some older ones; `nextCursor` continues with
       * older ones. Null when they could not be read.
       */
      completed: TaskPage | null;
    }
  | { kind: "unavailable" };

/**
 * One page of `GET /v1/tasks`, or undefined when the service answers with an
 * error or cannot be reached. On the server it calls the service directly; in
 * the browser it goes through the Dashboard's `/api/tasks` proxy.
 */
export async function fetchTaskPage(
  completed: boolean,
  cursor?: string,
): Promise<TaskPage | undefined> {
  const { data, error } = await tasksClient
    .GET("/v1/tasks", {
      params: {
        query: {
          completed,
          limit: TASKS_PAGE_SIZE,
          ...(cursor ? { cursor } : {}),
        },
      },
    })
    .catch(() => ({ data: undefined, error: true }));
  if (error || !data) return undefined;
  return { items: data.items, nextCursor: data.nextCursor ?? null };
}

/**
 * Follows `nextCursor` until the list ends, at least `cap` Tasks are loaded,
 * or `enough` says the loaded Tasks suffice. The result's `nextCursor` is set
 * when the list goes on. Undefined when any page fails: a partial list must
 * not pass for the whole one.
 */
export async function collectTasks(
  fetchPage: (cursor?: string) => Promise<TaskPage | undefined>,
  { cap, enough }: { cap: number; enough?: (items: Task[]) => boolean },
): Promise<TaskPage | undefined> {
  const items: Task[] = [];
  let cursor: string | null = null;
  do {
    const page = await fetchPage(cursor ?? undefined);
    if (!page) return undefined;
    items.push(...page.items);
    cursor = page.nextCursor;
  } while (cursor && items.length < cap && !enough?.(items));
  return { items, nextCursor: cursor };
}

/** The first instant of the recently completed window, in the owner's zone. */
export function recentCompletedSince(today: string, timeZone: string) {
  return startOfDay(addDays(today, 1 - RECENT_COMPLETED_DAYS), timeZone);
}

/** Whether a completed Task was completed at or after `since`. */
export function completedSince(task: Task, since: Date) {
  return Boolean(
    task.completedAt && new Date(task.completedAt).getTime() >= since.getTime(),
  );
}

/**
 * Splits completed Tasks (most recent first) into those completed since
 * `since` and older ones.
 */
export function splitCompleted(completed: Task[], since: Date) {
  return {
    recent: completed.filter((task) => completedSince(task, since)),
    older: completed.filter((task) => !completedSince(task, since)),
  };
}

/**
 * Loads every open Task (up to the cap) and the completed Tasks the page
 * shows first: the recently completed ones. Completed Tasks come most recent
 * first, so paging stops once a page reaches past the recent window.
 */
export async function getTasksWorkspace({
  today,
  timeZone,
}: {
  today: string;
  timeZone: string;
}): Promise<TasksWorkspaceState> {
  const since = recentCompletedSince(today, timeZone);
  const [open, completed] = await Promise.all([
    collectTasks((cursor) => fetchTaskPage(false, cursor), {
      cap: OPEN_TASKS_CAP,
    }),
    collectTasks((cursor) => fetchTaskPage(true, cursor), {
      cap: OPEN_TASKS_CAP,
      enough: (items) =>
        items.length > 0 && !completedSince(items[items.length - 1], since),
    }),
  ]);
  if (!open) return { kind: "unavailable" };
  return {
    kind: "ready",
    open: open.items,
    openCapped: open.nextCursor !== null,
    completed: completed ?? null,
  };
}

// Everything below is pure and runs identically on the server and in the
// browser: days are YYYY-MM-DD keys in the owner's timezone, and labels are
// assembled from Intl parts in the fixed DATE_LOCALE, so the hydrated page
// never differs from the server render whatever the browser's own timezone
// or language is.

/** The owner's calendar day a Task is due on, or null when it is undated. */
export function dueDay(dueAt: string | null | undefined, timeZone: string) {
  return dueAt ? dayKey(dueAt, timeZone) : null;
}

/**
 * Whether a due instant stands for a whole day rather than a time of day: it
 * is the owner's local midnight, or a UTC midnight (how a bare date is often
 * sent) that falls on the same calendar day for the owner. Such dates are
 * shown without a time.
 */
export function isDateOnly(dueAt: string, timeZone: string) {
  const instant = new Date(dueAt);
  const day = dayKey(instant, timeZone);
  if (startOfDay(day, timeZone).getTime() === instant.getTime()) return true;
  const iso = instant.toISOString();
  return iso.endsWith("T00:00:00.000Z") && iso.slice(0, 10) === day;
}

export type TaskGroups = Record<
  "selected" | "overdue" | "today" | "upcoming" | "unscheduled" | "completed",
  Task[]
>;

/**
 * Splits Tasks into the List view's sections. Every Task lands in exactly one
 * section; open Tasks due on a focused day other than today move from
 * Overdue/Upcoming into `selected`. Undated open Tasks are always
 * `unscheduled` (newest first), never given a made-up date; `completed` is
 * most recently completed first.
 */
export function groupTasks(
  tasks: Task[],
  {
    today,
    selected,
    timeZone,
  }: { today: string; selected: string; timeZone: string },
): TaskGroups {
  const groups: TaskGroups = {
    selected: [],
    overdue: [],
    today: [],
    upcoming: [],
    unscheduled: [],
    completed: [],
  };
  for (const task of tasks) {
    const day = dueDay(task.dueAt, timeZone);
    if (task.completed) groups.completed.push(task);
    else if (!day) groups.unscheduled.push(task);
    else if (day === today) groups.today.push(task);
    else if (day === selected) groups.selected.push(task);
    else if (day < today) groups.overdue.push(task);
    else groups.upcoming.push(task);
  }
  // Newest undated work first, and the most recently completed first, also
  // for Tasks that changed section on this page.
  groups.unscheduled.sort((a, b) => later(a.createdAt, b.createdAt));
  groups.completed.sort((a, b) => later(a.completedAt, b.completedAt));
  return groups;
}

/** Sort order for "most recent first"; a missing instant sorts last. */
function later(a: string | null | undefined, b: string | null | undefined) {
  const [first, second] = [a, b].map((value) =>
    value ? Date.parse(value) : -Infinity,
  );
  return first === second ? 0 : first > second ? -1 : 1;
}

/** The days that have open dated Tasks, for the date picker's indicator. */
export function daysWithTasks(tasks: Task[], timeZone: string) {
  const days = new Set<string>();
  for (const task of tasks) {
    const day = task.completed ? null : dueDay(task.dueAt, timeZone);
    if (day) days.add(day);
  }
  return days;
}

/** The Monday-to-Sunday week containing a day, as seven day keys. */
export function weekOf(day: string) {
  const weekday = new Date(`${day}T00:00:00Z`).getUTCDay();
  const monday = addDays(day, -((weekday + 6) % 7));
  return Array.from({ length: 7 }, (_, index) => addDays(monday, index));
}

const formats = new Map<string, Intl.DateTimeFormat>();

function parts(
  instant: Date,
  timeZone: string,
  options: Intl.DateTimeFormatOptions,
) {
  const id = `${timeZone}|${JSON.stringify(options)}`;
  let format = formats.get(id);
  if (!format) {
    format = new Intl.DateTimeFormat(DATE_LOCALE, { timeZone, ...options });
    formats.set(id, format);
  }
  const values = format.formatToParts(instant);
  return (type: Intl.DateTimeFormatPartTypes) =>
    values.find((part) => part.type === type)?.value ?? "";
}

/** "Mon 12 Oct", with the year only when it is not the current one. */
export function dayLabel(day: string, today: string) {
  const part = parts(new Date(`${day}T00:00:00Z`), "UTC", {
    weekday: "short",
    day: "numeric",
    month: "short",
    year: "numeric",
  });
  const label = `${part("weekday")} ${part("day")} ${part("month")}`;
  return day.slice(0, 4) === today.slice(0, 4)
    ? label
    : `${label} ${part("year")}`;
}

/** "Today", "Tomorrow", "Yesterday", or the day's label. */
export function relativeDayLabel(day: string, today: string) {
  if (day === today) return "Today";
  if (day === addDays(today, 1)) return "Tomorrow";
  if (day === addDays(today, -1)) return "Yesterday";
  return dayLabel(day, today);
}

/** The 24-hour wall-clock time (HH:mm) of an instant in the timezone. */
function clock(instant: Date, timeZone: string) {
  const part = parts(instant, timeZone, {
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  });
  return `${part("hour")}:${part("minute")}`;
}

/** The Task row's due label: "Today · 14:00", "Mon 12 Oct", or "No date". */
export function dueLabel(
  dueAt: string | null | undefined,
  today: string,
  timeZone: string,
) {
  if (!dueAt) return "No date";
  const day = relativeDayLabel(dayKey(dueAt, timeZone), today);
  return isDateOnly(dueAt, timeZone)
    ? day
    : `${day} · ${clock(new Date(dueAt), timeZone)}`;
}

/** A due instant as the owner's wall time for a datetime-local input. */
export function toDateTimeInput(
  dueAt: string | null | undefined,
  timeZone: string,
) {
  if (!dueAt) return "";
  const instant = new Date(dueAt);
  return `${dayKey(instant, timeZone)}T${clock(instant, timeZone)}`;
}

/** A datetime-local value, read as the owner's wall time, as an instant. */
export function fromDateTimeInput(value: string, timeZone: string) {
  const [day, time] = value.split("T");
  const wanted = Date.parse(`${day}T${time.slice(0, 5)}:00Z`);
  let instant =
    startOfDay(day, timeZone).getTime() +
    (wanted - Date.parse(`${day}T00:00:00Z`));
  // A DST change between midnight and that time shifts the wall clock once.
  const actual = new Date(instant);
  instant -=
    Date.parse(`${dayKey(actual, timeZone)}T${clock(actual, timeZone)}:00Z`) -
    wanted;
  return new Date(instant).toISOString();
}

/**
 * A due instant as the edit form's date picker and time field show it: the
 * owner's wall date (YYYY-MM-DD) and time (HH:mm); both "" when undated.
 */
export function toDueFields(
  dueAt: string | null | undefined,
  timeZone: string,
) {
  const [date = "", time = ""] = toDateTimeInput(dueAt, timeZone).split("T");
  return { date, time };
}

/**
 * The edit form's date and time, read as the owner's wall time, as a due
 * instant. A date without a time is due when that day starts (shown without
 * a time, see isDateOnly); without a date there is none.
 */
export function fromDueFields(date: string, time: string, timeZone: string) {
  return date
    ? fromDateTimeInput(`${date}T${time || "00:00"}`, timeZone)
    : undefined;
}
