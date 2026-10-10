import { tasksClient } from "@/lib/api/tasks-client";
import type { Task } from "@/lib/tasks-summary";
import { DATE_LOCALE, addDays, dayKey, startOfDay } from "@/lib/time-zone";

export type TasksWorkspaceState =
  | { kind: "ready"; tasks: Task[] }
  | { kind: "empty" }
  | { kind: "unavailable" };

export async function getTasksWorkspace(): Promise<TasksWorkspaceState> {
  const { data, error } = await tasksClient.GET("/v1/tasks", {
    params: { query: { limit: 100 } },
  });
  if (error || !data) return { kind: "unavailable" };
  if (data.items.length === 0) return { kind: "empty" };
  return { kind: "ready", tasks: data.items };
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
 * `unscheduled`, never given a made-up date.
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
  return groups;
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
