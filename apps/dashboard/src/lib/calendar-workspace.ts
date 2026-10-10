import type { EventInput } from "@fullcalendar/core";

import { calendarClient } from "@/lib/api/calendar-client";
import type { components } from "@/lib/api/generated/calendar";
import {
  NO_REPEAT,
  readRepeat,
  type Recurrence,
  recurrenceLabel,
  type RepeatDraft,
  repeatDraftFrom,
  shiftWeekdays,
  type Weekday,
  WEEKDAY_NAME,
} from "@/lib/calendar-recurrence";
import { fromDateTimeInput, toDateTimeInput } from "@/lib/tasks-workspace";
import {
  addDays,
  dateTimeFormat,
  dayKey,
  isValidTimeZone,
  startOfDay,
} from "@/lib/time-zone";

// The Calendar workspace (/calendar): FullCalendar renders the Calendar
// service's events, which stay the only source of truth. These helpers are
// the boundary between the two (all-day ends are inclusive in the API and
// exclusive in FullCalendar), the event form, and the labels the page shows.

export type CalendarEvent = components["schemas"]["CalendarEvent"];
export type Reminder = components["schemas"]["Reminder"];
export type ReminderDefaults = components["schemas"]["ReminderDefaults"];
export type CalendarSource = components["schemas"]["CalendarSource"];
/** How important an event is: the importance of its reminder notifications. */
export type Priority = components["schemas"]["EventPriority"];
type CreateEventRequest = components["schemas"]["CreateEventRequest"];
type UpdateEventRequest = components["schemas"]["UpdateEventRequest"];
/**
 * An update body as Calendar takes it: `recurrence: null` stops an event
 * repeating. The generated type misses that (OpenAPI 3.0 ignores `nullable`
 * beside a `$ref`), so `updateBody` hands it to the client.
 */
export type EventUpdate = Omit<UpdateEventRequest, "recurrence"> & {
  recurrence?: Recurrence | null;
};

/** An `EventUpdate` as the client's PATCH and split bodies are typed. */
export const updateBody = (body: EventUpdate) => body as UpdateEventRequest;
type UpdateOccurrenceRequest = components["schemas"]["UpdateOccurrenceRequest"];
type UpdateSettingsRequest = components["schemas"]["UpdateSettingsRequest"];
type UpdateSourceRequest = components["schemas"]["UpdateSourceRequest"];

export const CALENDAR_VIEWS = ["month", "week", "day", "list"] as const;
export type CalendarView = (typeof CALENDAR_VIEWS)[number];

/** FullCalendar's view for each of the page's views. */
export const FULLCALENDAR_VIEW: Record<CalendarView, string> = {
  month: "dayGridMonth",
  week: "timeGridWeek",
  day: "timeGridDay",
  // A custom list view: 30 days from the date in focus (see the workspace).
  list: "listAgenda",
};

export const VIEW_LABEL: Record<CalendarView, string> = {
  month: "Month",
  week: "Week",
  day: "Day",
  list: "List",
};

export function parseView(value: unknown): CalendarView | undefined {
  return CALENDAR_VIEWS.find((view) => view === value);
}

/** The page's view for a FullCalendar view type. */
export function viewOf(type: string): CalendarView | undefined {
  return CALENDAR_VIEWS.find((view) => FULLCALENDAR_VIEW[view] === type);
}

/** A valid YYYY-MM-DD day, else undefined. */
export function parseDay(value: unknown): string | undefined {
  if (typeof value !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(value)) {
    return undefined;
  }
  const date = new Date(`${value}T00:00:00Z`);
  return !Number.isNaN(date.getTime()) &&
    date.toISOString().slice(0, 10) === value
    ? value
    : undefined;
}

const pad = (value: number) => String(value).padStart(2, "0");

/**
 * The browser-local day (YYYY-MM-DD) of a FullCalendar date. FullCalendar
 * runs with `timeZone: "local"`, so its dates are the browser's wall clock.
 */
export function localDay(date: Date) {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

export function isImported(event: CalendarEvent) {
  return event.source.kind !== "manual";
}

/**
 * Whether the event can be changed here beyond its reminders and priority:
 * Life OS events and Google events of calendars you may edit (changed in
 * Google first). Feed events, Google calendars you can only read, and
 * occurrences of Google rules Calendar cannot hold are read-only.
 */
export function isEditable(event: CalendarEvent) {
  return event.editable;
}

export function isCancelled(event: CalendarEvent) {
  return event.status === "cancelled";
}

/** The name Calendar gives an imported event's source, such as "ZAW". */
function sourceName(event: CalendarEvent) {
  return event.source.kind === "manual"
    ? undefined
    : event.source.name?.trim() || undefined;
}

/** Where an imported event comes from; undefined for Life OS events. */
export function sourceLabel(event: CalendarEvent) {
  switch (event.source.kind) {
    case "ics":
      return sourceName(event) ?? "ICS feed";
    case "google":
      return sourceName(event) ?? "Google Calendar";
    default:
      return undefined;
  }
}

/**
 * Why an event can't be edited here (Calendar says why); undefined when it
 * can.
 */
export function readOnlyReason(event: CalendarEvent) {
  if (event.editable) return undefined;
  if (event.readOnlyReason) return event.readOnlyReason;
  const from =
    event.source.kind === "ics"
      ? (sourceName(event) ?? "an ICS feed")
      : (sourceName(event) ?? "Google Calendar");
  return `Imported from ${from}, so it can only be changed there. Its reminders and priority can still be adjusted here.`;
}

/**
 * The source badge of an event that is not a Life OS one: "Google · Owner"
 * for a Google event, "Imported · ZAW" for a feed's.
 */
export function sourceBadge(event: CalendarEvent) {
  const source = sourceLabel(event);
  if (!source) return undefined;
  return event.source.kind === "google"
    ? `Google · ${source}`
    : `Imported · ${source}`;
}

/**
 * Where the event is in Google and how its sync stands, for its details:
 * "In Google: Life OS" with "Exported", "Waiting to be exported", or the
 * export's failure ("In Google: Owner", "Synced both ways" for a Google
 * event); undefined when it is not in Google.
 */
export function googleSyncLabel(
  event: CalendarEvent,
): { place: string; state: string; failing: boolean } | undefined {
  const link = event.google;
  if (!link) return undefined;
  const place = `In Google: ${link.calendarName}`;
  switch (link.state) {
    case "pending":
      return { place, state: "Waiting to be exported", failing: false };
    case "failed":
      return {
        place,
        state: `Export failed${link.error ? `: ${link.error}` : ""}. Retrying automatically.`,
        failing: true,
      };
    default:
      return {
        place,
        state: link.lifeOs ? "Exported" : "Synced both ways",
        failing: false,
      };
  }
}

/** The code of a Calendar error response, such as "google_conflict". */
export function apiErrorCode(error: unknown): string | undefined {
  if (typeof error !== "object" || error === null || !("error" in error)) {
    return undefined;
  }
  const body = (error as { error?: { code?: unknown } }).error;
  return typeof body?.code === "string" ? body.code : undefined;
}

/**
 * Whether Calendar refused a change because the event changed or was
 * deleted in Google meanwhile: it then shows Google's version, so the page
 * reads the event again.
 */
export function changedInGoogle(error: unknown) {
  const code = apiErrorCode(error);
  return code === "google_conflict" || code === "google_deleted";
}

// Priority ----------------------------------------------------------------------

export const PRIORITIES: Priority[] = ["low", "medium", "high"];

export const PRIORITY_LABEL: Record<Priority, string> = {
  low: "Low",
  medium: "Medium",
  high: "High",
};

/**
 * The badge an event's details show for its priority: high and low are
 * named, medium (the usual) is not.
 */
export function priorityBadge(event: Pick<CalendarEvent, "priority">) {
  return event.priority === "medium"
    ? undefined
    : `${PRIORITY_LABEL[event.priority]} priority`;
}

/**
 * What the form says about an imported event's priority: its source's
 * default, which it follows unless it gets another.
 */
export function defaultPriorityNote(event: CalendarEvent) {
  const source = sourceLabel(event);
  return source
    ? `${source} events are ${PRIORITY_LABEL[event.defaultPriority].toLowerCase()} priority unless you choose another.`
    : undefined;
}

/**
 * A key unique per listed item: occurrences of a recurring event share the
 * event's id, so they are told apart by their recurrence id.
 */
export function occurrenceKey(event: CalendarEvent) {
  return event.occurrence
    ? `${event.id}:${event.occurrence.recurrenceId}`
    : event.id;
}

/**
 * The event as FullCalendar input. An all-day event's last day is inclusive
 * in the API and its end exclusive in FullCalendar, so the end moves one day
 * later. Read-only (see isEditable) and cancelled events can't be dragged;
 * dragging an occurrence of a recurring event asks which occurrences move.
 */
export function toEventInput(event: CalendarEvent): EventInput | undefined {
  const imported = isImported(event);
  const cancelled = isCancelled(event);
  const common: EventInput = {
    id: occurrenceKey(event),
    title: event.title,
    editable: isEditable(event) && !cancelled,
    classNames: [
      "lifeos-event",
      ...(imported ? ["lifeos-event-imported"] : []),
      ...(cancelled ? ["lifeos-event-cancelled"] : []),
      // A small mark before the title (see calendar-workspace.css).
      ...(event.priority === "high" ? ["lifeos-event-high"] : []),
    ],
    extendedProps: { event },
  };
  if (event.allDay) {
    if (!event.startDate) return undefined;
    return {
      ...common,
      allDay: true,
      start: event.startDate,
      end: addDays(event.endDate ?? event.startDate, 1),
    };
  }
  if (!event.startAt) return undefined;
  return {
    ...common,
    allDay: false,
    start: event.startAt,
    end: event.endAt ?? undefined,
  };
}

const HOUR = 3_600_000;

/** A start and end of either kind, as the API takes them. */
export type EventTimes =
  | { allDay: true; startDate: string; endDate: string }
  | { allDay: false; startAt: string; endAt: string };

/** An event dragged or resized in FullCalendar: where it was dropped. */
export type EventMove = { allDay: boolean; start: Date; end: Date | null };

/**
 * The PATCH body for an event dragged or resized in FullCalendar. All-day
 * ends come back exclusive and are stored inclusive; a timed event dropped
 * from the all-day row has no end yet and keeps its length, or gets an hour.
 */
export function rescheduleRequest(
  change: EventMove,
  original: CalendarEvent,
): EventTimes {
  if (change.allDay) {
    const startDate = localDay(change.start);
    const last = change.end ? addDays(localDay(change.end), -1) : startDate;
    return {
      allDay: true,
      startDate,
      endDate: last < startDate ? startDate : last,
    };
  }
  const length =
    !original.allDay && original.startAt && original.endAt
      ? Date.parse(original.endAt) - Date.parse(original.startAt)
      : HOUR;
  const end = change.end ?? new Date(change.start.getTime() + length);
  return {
    allDay: false,
    startAt: change.start.toISOString(),
    endAt: end.toISOString(),
  };
}

// Labels ---------------------------------------------------------------------

const DAY = { weekday: "short", day: "numeric", month: "short" } as const;
const TIME = { hour: "2-digit", minute: "2-digit" } as const;

/** "Fri 16 Oct", plus the year when it isn't `year`: "Thu 14 Jan 2027". */
function dateLabel(day: string, year: string) {
  // A date is not an instant: it reads the same in every timezone.
  const label = dateTimeFormat("UTC", DAY).format(new Date(`${day}T00:00:00Z`));
  return day.startsWith(year) ? label : `${label} ${day.slice(0, 4)}`;
}

function instantLabel(instant: Date, timeZone: string, year: string) {
  return dateLabel(dayKey(instant, timeZone), year);
}

/**
 * When an event happens, in `timeZone`, such as "Fri 16 Oct · 15:00–16:00"
 * or "Fri 16 Oct – Sun 18 Oct · All day". Dates outside the year of `today`
 * (the owner's YYYY-MM-DD) carry their year.
 */
export function eventWhenLabel(
  event: CalendarEvent,
  timeZone: string,
  today: string,
) {
  const year = today.slice(0, 4);
  if (event.allDay) {
    if (!event.startDate) return "All day";
    const first = dateLabel(event.startDate, year);
    return event.endDate && event.endDate !== event.startDate
      ? `${first} – ${dateLabel(event.endDate, year)} · All day`
      : `${first} · All day`;
  }
  if (!event.startAt) return "Time not set";
  const start = new Date(event.startAt);
  const time = (instant: Date) =>
    dateTimeFormat(timeZone, TIME).format(instant);
  const label = `${instantLabel(start, timeZone, year)} · ${time(start)}`;
  const end = event.endAt ? new Date(event.endAt) : null;
  if (!end || end <= start) return label;
  return dayKey(end, timeZone) === dayKey(start, timeZone)
    ? `${label}–${time(end)}`
    : `${label} – ${instantLabel(end, timeZone, year)} · ${time(end)}`;
}

/** A date and time with its zone's abbreviation, such as "9 Oct 2026, 14:00 CEST". */
export function momentLabel(instant: string, timeZone: string) {
  return dateTimeFormat(timeZone, {
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    timeZoneName: "short",
  }).format(new Date(instant));
}

const plural = (count: number, unit: string) =>
  `${count} ${unit}${count === 1 ? "" : "s"}`;

export function reminderLabel(reminder: Reminder) {
  if (reminder.kind === "localTime") {
    const days = reminder.daysBefore ?? 0;
    const at = reminder.localTime ?? "";
    return days === 0
      ? `On the day at ${at}`
      : `${plural(days, "day")} before at ${at}`;
  }
  const minutes = reminder.minutesBefore ?? 0;
  if (minutes === 0) return "At the start";
  if (minutes % 1440 === 0) return `${plural(minutes / 1440, "day")} before`;
  if (minutes % 60 === 0) return `${plural(minutes / 60, "hour")} before`;
  return `${plural(minutes, "minute")} before`;
}

/** Whether two reminders are the same one, as Calendar compares them. */
export function sameReminder(a: Reminder, b: Reminder) {
  if (a.kind !== b.kind) return false;
  return a.kind === "localTime"
    ? (a.daysBefore ?? 0) === (b.daysBefore ?? 0) &&
        (a.localTime ?? "") === (b.localTime ?? "")
    : (a.minutesBefore ?? 0) === (b.minutesBefore ?? 0);
}

/** Whether two reminder lists are the same; null is an empty list. */
export function sameReminders(
  a: Reminder[] | null | undefined,
  b: Reminder[] | null | undefined,
) {
  const left = a ?? [];
  const right = b ?? [];
  return (
    left.length === right.length &&
    left.every((reminder, index) => sameReminder(reminder, right[index]))
  );
}

/**
 * Every reminder that applies to the event, one line each, in Calendar's
 * order (defaults first, then the event's own). A reminder that comes from
 * the defaults says where from: "ZAW default" for an event imported from
 * ZAW, else "default" (the owner's, from Reminder defaults).
 */
export function reminderLines(
  event: CalendarEvent,
): { label: string; origin?: string }[] {
  const source = sourceLabel(event);
  const origin = source ? `${source} default` : "default";
  return event.effectiveReminders.map((reminder) =>
    event.useDefaultReminders &&
    event.defaultReminders.some((fallback) => sameReminder(fallback, reminder))
      ? { label: reminderLabel(reminder), origin }
      : { label: reminderLabel(reminder) },
  );
}

/**
 * The defaults "Use default reminders" stands for in the event form, or
 * whether the owner's are still loading or unavailable.
 */
export type FormDefaults = Reminder[] | "loading" | "unavailable";

/**
 * The default reminders the event form offers: an imported event's
 * source's, else the owner's for timed or all-day events, following the
 * form's All day box. Until the owner's settings arrive, an existing event
 * shows its own defaults while its kind is unchanged.
 */
export function formDefaults(
  event: CalendarEvent | undefined,
  allDay: boolean,
  owner: ReminderDefaults | "loading" | "unavailable",
): FormDefaults {
  if (event && isImported(event)) return event.defaultReminders;
  if (typeof owner === "object") return allDay ? owner.allDay : owner.timed;
  return event && event.allDay === allDay ? event.defaultReminders : owner;
}

/**
 * The note under "Use default reminders": the defaults that would apply and
 * where they come from, such as "1 day before at 19:00 (ZAW)", or where to
 * set some when there are none.
 */
export function defaultRemindersNote(
  defaults: FormDefaults,
  { allDay, source }: { allDay: boolean; source?: string },
) {
  const kind = allDay ? "all-day events" : "timed events";
  if (defaults === "loading") return "Loading your default reminders…";
  if (defaults === "unavailable") {
    return "Your default reminders could not be loaded.";
  }
  if (defaults.length === 0) {
    return source
      ? `${source} has no default reminders yet; set them in Reminder defaults.`
      : `No default reminders for ${kind} yet; set them in Reminder defaults.`;
  }
  const list = defaults.map(reminderLabel).join(", ");
  if (source) return `${list} (${source})`;
  return `${list} (your ${defaults.length === 1 ? "default" : "defaults"} for ${kind})`;
}

/**
 * The zone a recurring event repeats in: a timed event's own, else the
 * owner's; all-day events compare their dates in the owner's.
 */
export function seriesZone(
  event: { allDay: boolean; timeZone?: string | null },
  ownerZone: string,
) {
  return event.allDay
    ? ownerZone
    : editingZone(event.timeZone ?? "", ownerZone);
}

const isInstant = (value: string | null | undefined): value is string =>
  Boolean(value) && !Number.isNaN(Date.parse(value as string));

/**
 * How a recurring event repeats, in plain words, such as "Weekly on Monday
 * and Wednesday" (see `recurrenceLabel`). It is read from the occurrence's
 * original start (`recurrenceId`), which falls on the series' weekday, day
 * of the month, and date even when this occurrence was moved.
 */
export function eventRecurrenceLabel(event: CalendarEvent, ownerZone: string) {
  if (!event.recurrence) return undefined;
  const zone = seriesZone(event, ownerZone);
  const original = event.occurrence?.recurrenceId;
  if (event.allDay) {
    const startDate = parseDay(original) ?? event.startDate;
    return startDate
      ? recurrenceLabel(event.recurrence, startDate, zone)
      : undefined;
  }
  const start = [original, event.startAt].find(isInstant);
  return start
    ? recurrenceLabel(event.recurrence, dayKey(start, zone), zone)
    : undefined;
}

/** The owner's day an event starts on, where the calendar should go. */
export function eventStartDay(event: CalendarEvent, timeZone: string) {
  if (event.allDay) return event.startDate ?? undefined;
  return event.startAt ? dayKey(event.startAt, timeZone) : undefined;
}

/** When an event starts, as epoch milliseconds (all-day: owner's midnight). */
function startTime(event: CalendarEvent, timeZone: string) {
  if (event.allDay) {
    return event.startDate
      ? startOfDay(event.startDate, timeZone).getTime()
      : Infinity;
  }
  return event.startAt ? Date.parse(event.startAt) : Infinity;
}

function isOver(event: CalendarEvent, now: Date, timeZone: string) {
  if (event.allDay) {
    const last = event.endDate ?? event.startDate;
    return last ? last < dayKey(now, timeZone) : false;
  }
  const end = event.endAt ?? event.startAt;
  return end ? Date.parse(end) <= now.getTime() : false;
}

/**
 * Search results as a dated list: events not over yet, soonest first, and
 * past ones, most recent first.
 */
export function splitSearchResults(
  items: CalendarEvent[],
  now: Date,
  timeZone: string,
) {
  const upcoming: CalendarEvent[] = [];
  const past: CalendarEvent[] = [];
  for (const event of items) {
    (isOver(event, now, timeZone) ? past : upcoming).push(event);
  }
  const by = (event: CalendarEvent) => startTime(event, timeZone);
  upcoming.sort((a, b) => by(a) - by(b));
  past.sort((a, b) => by(b) - by(a));
  return { upcoming, past };
}

// The event form --------------------------------------------------------------

export type ReminderUnit = "minutes" | "hours" | "days";

export type ReminderDraft =
  | { kind: "relative"; amount: string; unit: ReminderUnit }
  | { kind: "localTime"; daysBefore: string; time: string };

/** The event form's values, as the inputs hold them. */
export type EventDraft = {
  title: string;
  allDay: boolean;
  startDate: string;
  startTime: string;
  /** All-day: the last day (inclusive). */
  endDate: string;
  endTime: string;
  /** The zone the times are in; "" is the owner's zone, stored as no zone. */
  timeZone: string;
  location: string;
  notes: string;
  /** Whether the event's default reminders apply as well as its own. */
  useDefaultReminders: boolean;
  /** The event's own reminders. */
  reminders: ReminderDraft[];
  /** How important it is (its own, else its default). */
  priority: Priority;
  /** How it repeats (Repeat), following the start date. */
  repeat: RepeatDraft;
};

export type EventField =
  "title" | "start" | "end" | "timeZone" | "repeat" | "reminders";
export type FieldErrors = Partial<Record<EventField | "form", string>>;

const UNIT_MINUTES: Record<ReminderUnit, number> = {
  minutes: 1,
  hours: 60,
  days: 1440,
};

export const MAX_REMINDERS = 10;

export function toReminderDraft(reminder: Reminder): ReminderDraft {
  if (reminder.kind === "localTime") {
    return {
      kind: "localTime",
      daysBefore: String(reminder.daysBefore ?? 0),
      time: reminder.localTime ?? "",
    };
  }
  const minutes = reminder.minutesBefore ?? 0;
  const unit: ReminderUnit =
    minutes > 0 && minutes % 1440 === 0
      ? "days"
      : minutes > 0 && minutes % 60 === 0
        ? "hours"
        : "minutes";
  return {
    kind: "relative",
    amount: String(minutes / UNIT_MINUTES[unit]),
    unit,
  };
}

const wholeNumber = (value: string) =>
  /^\d+$/.test(value.trim()) ? Number(value.trim()) : undefined;

/** The reminder a draft row describes, or undefined when incomplete. */
export function fromReminderDraft(draft: ReminderDraft): Reminder | undefined {
  if (draft.kind === "localTime") {
    const daysBefore = wholeNumber(draft.daysBefore);
    return daysBefore !== undefined && /^\d{2}:\d{2}$/.test(draft.time)
      ? { kind: "localTime", daysBefore, localTime: draft.time }
      : undefined;
  }
  const amount = wholeNumber(draft.amount);
  return amount !== undefined
    ? { kind: "relative", minutesBefore: amount * UNIT_MINUTES[draft.unit] }
    : undefined;
}

/** Reads reminder rows; the first incomplete one is named. */
export function readReminders(
  drafts: ReminderDraft[],
): { reminders: Reminder[] } | { error: string } {
  const read = drafts.map(fromReminderDraft);
  const missing = read.findIndex((reminder) => !reminder);
  return missing >= 0
    ? {
        error: `Complete reminder ${missing + 1}: a whole number and, for a day reminder, a time.`,
      }
    : { reminders: read as Reminder[] };
}

function splitInput(value: string) {
  const [date = "", time = ""] = value.split("T");
  return { date, time: time.slice(0, 5) };
}

/** The zone a timed event's times are edited in: its own, else the owner's. */
export function editingZone(timeZone: string, ownerZone: string) {
  return timeZone && isValidTimeZone(timeZone) ? timeZone : ownerZone;
}

/** The form's values for an existing event. */
export function draftFromEvent(
  event: CalendarEvent,
  ownerZone: string,
): EventDraft {
  const common = {
    title: event.title,
    location: event.location ?? "",
    notes: event.description ?? "",
    useDefaultReminders: event.useDefaultReminders,
    reminders: event.reminders.map(toReminderDraft),
    priority: event.priority,
  };
  if (event.allDay) {
    const startDate = event.startDate ?? "";
    return {
      ...common,
      allDay: true,
      startDate,
      startTime: "09:00",
      endDate: event.endDate ?? startDate,
      endTime: "10:00",
      timeZone: "",
      repeat: repeatDraftFrom(event.recurrence, startDate, ownerZone),
    };
  }
  const timeZone = event.timeZone ?? "";
  const zone = editingZone(timeZone, ownerZone);
  const start = splitInput(toDateTimeInput(event.startAt, zone));
  const end = splitInput(toDateTimeInput(event.endAt, zone));
  return {
    ...common,
    allDay: false,
    startDate: start.date,
    startTime: start.time,
    endDate: end.date || start.date,
    endTime: end.time,
    timeZone,
    repeat: repeatDraftFrom(event.recurrence, start.date, zone),
  };
}

/**
 * The form's values for a new event: the range picked on the calendar
 * (browser-local dates from FullCalendar), else the next full hour today.
 * Clicking a single time slot makes a one-hour event.
 */
export function newDraft(
  ownerZone: string,
  now: Date,
  range?: { start: Date; end: Date; allDay: boolean },
): EventDraft {
  const blank = {
    title: "",
    location: "",
    notes: "",
    timeZone: "",
    useDefaultReminders: true,
    reminders: [],
    priority: "medium" as const,
    repeat: NO_REPEAT,
  };
  if (range?.allDay) {
    const startDate = localDay(range.start);
    const last = addDays(localDay(range.end), -1);
    return {
      ...blank,
      allDay: true,
      startDate,
      startTime: "09:00",
      endDate: last < startDate ? startDate : last,
      endTime: "10:00",
    };
  }
  let start: Date;
  let end: Date;
  if (range) {
    start = range.start;
    end =
      range.end.getTime() - start.getTime() <= HOUR / 2
        ? new Date(start.getTime() + HOUR)
        : range.end;
  } else {
    start = new Date(Math.ceil(now.getTime() / HOUR) * HOUR);
    end = new Date(start.getTime() + HOUR);
  }
  const startInput = splitInput(
    toDateTimeInput(start.toISOString(), ownerZone),
  );
  const endInput = splitInput(toDateTimeInput(end.toISOString(), ownerZone));
  return {
    ...blank,
    allDay: false,
    startDate: startInput.date,
    startTime: startInput.time,
    endDate: endInput.date,
    endTime: endInput.time,
  };
}

/**
 * Moves the end with the start, keeping the event's length, as calendars do
 * when the start changes. Returns the draft unchanged when a value is missing.
 */
export function moveStart(
  draft: EventDraft,
  next: { startDate: string; startTime: string },
): EventDraft {
  const moved = { ...draft, ...next };
  if (draft.allDay) {
    if (!draft.startDate || !draft.endDate || !next.startDate) return moved;
    const days =
      (Date.parse(`${draft.endDate}T00:00:00Z`) -
        Date.parse(`${draft.startDate}T00:00:00Z`)) /
      86_400_000;
    return { ...moved, endDate: addDays(next.startDate, Math.max(0, days)) };
  }
  const wall = (date: string, time: string) =>
    date && /^\d{2}:\d{2}$/.test(time)
      ? Date.parse(`${date}T${time}:00Z`)
      : NaN;
  const length =
    wall(draft.endDate, draft.endTime) - wall(draft.startDate, draft.startTime);
  const start = wall(next.startDate, next.startTime);
  if (Number.isNaN(length) || Number.isNaN(start) || length < 0) return moved;
  const end = new Date(start + length).toISOString();
  return { ...moved, endDate: end.slice(0, 10), endTime: end.slice(11, 16) };
}

/** When an event happens, with the zone a timed event is scheduled in. */
export type EventSchedule =
  | { allDay: true; startDate: string; endDate: string }
  | { allDay: false; startAt: string; endAt: string; timeZone: string | null };

/** An event's fields read from the form; null clears an optional one. */
export type EventFields = {
  title: string;
  description: string | null;
  location: string | null;
  /** The event's own reminders, added to the defaults when they are used. */
  reminders: Reminder[];
  useDefaultReminders: boolean;
  priority: Priority;
  /** How it repeats; null: it does not. */
  recurrence: Recurrence | null;
} & EventSchedule;

const TIME_INPUT = /^\d{2}:\d{2}$/;

/**
 * Reads the form. Only what the form itself can tell is checked here (a
 * title, the dates and times filled in, complete reminders, a complete
 * custom repeat); the Calendar API validates the rest, such as an end
 * before the start, and its message is shown at the field it concerns (see
 * `fieldErrorsFromApi`).
 */
export function readDraft(
  draft: EventDraft,
  ownerZone: string,
): { fields: EventFields } | { errors: FieldErrors } {
  const errors: FieldErrors = {};
  const title = draft.title.trim();
  if (!title) errors.title = "Add a title.";

  const reminders = readReminders(draft.reminders);
  if ("error" in reminders) errors.reminders = reminders.error;

  // A series ending on a day ends at its close in the zone it repeats in.
  const repeat = readRepeat(
    draft.repeat,
    draft.startDate,
    seriesZone(draft, ownerZone),
  );
  if ("error" in repeat) errors.repeat = repeat.error;

  const common = {
    title,
    description: draft.notes.trim() || null,
    location: draft.location.trim() || null,
    reminders: "reminders" in reminders ? reminders.reminders : [],
    useDefaultReminders: draft.useDefaultReminders,
    priority: draft.priority,
    recurrence: "recurrence" in repeat ? repeat.recurrence : null,
  };

  if (draft.allDay) {
    if (!parseDay(draft.startDate)) errors.start = "Choose a start date.";
    const endDate = draft.endDate || draft.startDate;
    if (draft.endDate && !parseDay(draft.endDate)) {
      errors.end = "Choose an end date.";
    }
    if (Object.keys(errors).length) return { errors };
    return {
      fields: {
        ...common,
        allDay: true,
        startDate: draft.startDate,
        endDate,
      },
    };
  }

  if (!parseDay(draft.startDate) || !TIME_INPUT.test(draft.startTime)) {
    errors.start = "Choose a start date and time.";
  }
  if (!parseDay(draft.endDate) || !TIME_INPUT.test(draft.endTime)) {
    errors.end = "Choose an end date and time.";
  }
  if (Object.keys(errors).length) return { errors };
  const zone = editingZone(draft.timeZone, ownerZone);
  return {
    fields: {
      ...common,
      allDay: false,
      startAt: fromDateTimeInput(`${draft.startDate}T${draft.startTime}`, zone),
      endAt: fromDateTimeInput(`${draft.endDate}T${draft.endTime}`, zone),
      timeZone: draft.timeZone || null,
    },
  };
}

/**
 * The create body: empty optional fields, no own reminders, medium priority
 * (Calendar's default), and no repeat left out.
 */
export function createRequest(fields: EventFields): CreateEventRequest {
  const body: CreateEventRequest = {
    title: fields.title,
    allDay: fields.allDay,
    ...(fields.description ? { description: fields.description } : {}),
    ...(fields.location ? { location: fields.location } : {}),
    ...(fields.reminders.length ? { reminders: fields.reminders } : {}),
    useDefaultReminders: fields.useDefaultReminders,
    ...(fields.priority !== "medium" ? { priority: fields.priority } : {}),
    ...(fields.recurrence ? { recurrence: fields.recurrence } : {}),
  };
  if (fields.allDay) {
    return { ...body, startDate: fields.startDate, endDate: fields.endDate };
  }
  return {
    ...body,
    startAt: fields.startAt,
    endAt: fields.endAt,
    ...(fields.timeZone ? { timeZone: fields.timeZone } : {}),
  };
}

/**
 * The reminder fields of a PATCH: only those that differ from the event, so
 * an unchanged form leaves them alone. An emptied list sends [] (no own
 * reminders), which keeps `useDefaultReminders` as it is.
 */
export function reminderChanges(
  fields: Pick<EventFields, "reminders" | "useDefaultReminders">,
  event: CalendarEvent,
): Pick<UpdateOccurrenceRequest, "reminders" | "useDefaultReminders"> {
  return {
    ...(sameReminders(fields.reminders, event.reminders)
      ? {}
      : { reminders: fields.reminders }),
    ...(fields.useDefaultReminders === event.useDefaultReminders
      ? {}
      : { useDefaultReminders: fields.useDefaultReminders }),
  };
}

/**
 * The priority of a PATCH: only when it differs from the event's. Calendar
 * treats the event's default (`defaultPriority`) as none of its own, so the
 * event follows its default again.
 */
export function priorityChange(
  fields: Pick<EventFields, "priority">,
  event: CalendarEvent,
): Pick<UpdateOccurrenceRequest, "priority"> {
  return fields.priority === event.priority
    ? {}
    : { priority: fields.priority };
}

/**
 * The PATCH body of an imported event, which can change only its reminders
 * and priority: what changed of them; empty when nothing did.
 */
export function reminderAndPriorityChanges(
  fields: Pick<EventFields, "reminders" | "useDefaultReminders" | "priority">,
  event: CalendarEvent,
) {
  return {
    ...reminderChanges(fields, event),
    ...priorityChange(fields, event),
  };
}

/**
 * The PATCH body of an event that is not an occurrence: every field the
 * form edits, null where it was cleared, the reminder fields and priority
 * that changed, and the repeat when it does or did repeat. Switching between timed and
 * all-day sends the new kind's start and end; the API clears the other
 * kind's fields.
 */
export function updateRequest(
  fields: EventFields,
  event: CalendarEvent,
): EventUpdate {
  const body: EventUpdate = {
    title: fields.title,
    description: fields.description,
    location: fields.location,
    ...reminderAndPriorityChanges(fields, event),
    ...(fields.recurrence || event.recurrence
      ? { recurrence: fields.recurrence }
      : {}),
    allDay: fields.allDay,
  };
  if (fields.allDay) {
    return { ...body, startDate: fields.startDate, endDate: fields.endDate };
  }
  return {
    ...body,
    startAt: fields.startAt,
    endAt: fields.endAt,
    timeZone: fields.timeZone,
  };
}

// Recurring events: which occurrences a change applies to ---------------------

/**
 * The scopes of a change to an occurrence of a recurring event, as in Google
 * Calendar: only this occurrence (PATCH it), this and the following ones
 * (split the series there, or truncate it to delete), or every occurrence
 * (PATCH or DELETE the series).
 */
export type Scope = "this" | "following" | "all";

export const SCOPE_LABEL: Record<Scope, string> = {
  this: "This event",
  following: "This and following events",
  all: "All events",
};

export const ALL_SCOPES: Scope[] = ["this", "following", "all"];

/** The scopes a delete offers: a skipped occurrence is already deleted alone. */
export function deleteScopes(occurrence: CalendarEvent): Scope[] {
  return isCancelled(occurrence) ? ["following", "all"] : ALL_SCOPES;
}

const text = (value: string | null | undefined) => value?.trim() || null;

/**
 * The title, notes, location, reminders, and priority that differ from the
 * event.
 */
function contentChanges(fields: EventFields, event: CalendarEvent) {
  return {
    ...(text(fields.title) === text(event.title)
      ? {}
      : { title: fields.title }),
    ...(text(fields.description) === text(event.description)
      ? {}
      : { description: fields.description }),
    ...(text(fields.location) === text(event.location)
      ? {}
      : { location: fields.location }),
    ...reminderAndPriorityChanges(fields, event),
  } satisfies UpdateOccurrenceRequest;
}

const sameInstant = (
  a: string | null | undefined,
  b: string | null | undefined,
) => isInstant(a) && isInstant(b) && Date.parse(a) === Date.parse(b);

/** Whether the start, end, kind, or time zone differ from the event's. */
function scheduleChanged(fields: EventSchedule, event: CalendarEvent) {
  if (fields.allDay !== event.allDay) return true;
  if (fields.allDay) {
    return (
      fields.startDate !== event.startDate ||
      fields.endDate !== (event.endDate ?? event.startDate)
    );
  }
  return (
    !sameInstant(fields.startAt, event.startAt) ||
    !sameInstant(fields.endAt, event.endAt) ||
    fields.timeZone !== (event.timeZone ?? null)
  );
}

/**
 * What an edit of an occurrence changes, compared with the occurrence the
 * form was filled from. `seriesOnly`: it changes what belongs to the series
 * (how it repeats, all day, the time zone), which can't change for one
 * occurrence, so This event is not offered.
 */
export function occurrenceEdit(
  fields: EventFields,
  occurrence: CalendarEvent,
  repeatChanged: boolean,
) {
  const seriesOnly =
    repeatChanged ||
    fields.allDay !== occurrence.allDay ||
    (!fields.allDay && fields.timeZone !== (occurrence.timeZone ?? null));
  return {
    changed:
      repeatChanged ||
      scheduleChanged(fields, occurrence) ||
      Object.keys(contentChanges(fields, occurrence)).length > 0,
    seriesOnly,
  };
}

/** The scopes an edit or a drag offers. */
export function editScopes(edit: { seriesOnly: boolean }): Scope[] {
  return edit.seriesOnly ? ["following", "all"] : ALL_SCOPES;
}

/**
 * An occurrence dragged or resized: its own fields at the new time, to save
 * like an edit in the form.
 */
export function movedFields(
  change: EventMove,
  occurrence: CalendarEvent,
): EventFields {
  const common = {
    title: occurrence.title,
    description: occurrence.description ?? null,
    location: occurrence.location ?? null,
    reminders: occurrence.reminders,
    useDefaultReminders: occurrence.useDefaultReminders,
    priority: occurrence.priority,
    recurrence: occurrence.recurrence ?? null,
  };
  const times = rescheduleRequest(change, occurrence);
  return times.allDay
    ? { ...common, ...times }
    : { ...common, ...times, timeZone: occurrence.timeZone ?? null };
}

/**
 * This event: the PATCH body for the occurrence, only what changed. Its
 * start and end keep the series' kind (`occurrenceEdit` tells when they
 * don't).
 */
export function occurrenceRequest(
  fields: EventFields,
  occurrence: CalendarEvent,
): UpdateOccurrenceRequest {
  const body: UpdateOccurrenceRequest = contentChanges(fields, occurrence);
  if (!scheduleChanged(fields, occurrence)) return body;
  return fields.allDay
    ? { ...body, startDate: fields.startDate, endDate: fields.endDate }
    : { ...body, startAt: fields.startAt, endAt: fields.endAt };
}

/** A new start and end: the kind and zone are named only when they change. */
function scheduleBody(
  schedule: EventSchedule,
  before: CalendarEvent,
): EventUpdate {
  const kind =
    schedule.allDay !== before.allDay ? { allDay: schedule.allDay } : {};
  if (schedule.allDay) {
    return {
      ...kind,
      startDate: schedule.startDate,
      endDate: schedule.endDate,
    };
  }
  return {
    ...kind,
    startAt: schedule.startAt,
    endAt: schedule.endAt,
    ...(schedule.timeZone !== (before.timeZone ?? null)
      ? { timeZone: schedule.timeZone }
      : {}),
  };
}

/** When an event starts on `timeZone`'s wall clock, "YYYY-MM-DDTHH:MM". */
function wallStart(
  event: {
    allDay: boolean;
    startDate?: string | null;
    startAt?: string | null;
  },
  timeZone: string,
) {
  if (event.allDay) {
    return event.startDate ? `${event.startDate}T00:00` : undefined;
  }
  return isInstant(event.startAt)
    ? toDateTimeInput(event.startAt, timeZone)
    : undefined;
}

const wallTime = (wall: string) => Date.parse(`${wall}:00Z`);
const daysBetween = (from: string, to: string) =>
  Math.round(
    (Date.parse(`${to.slice(0, 10)}T00:00:00Z`) -
      Date.parse(`${from.slice(0, 10)}T00:00:00Z`)) /
      86_400_000,
  );

/**
 * The series' rule once its occurrences move `days` days: weekly days move
 * with them (Monday and Wednesday, a day later, are Tuesday and Thursday).
 * Other rules follow the start by themselves; undefined when nothing moves.
 */
function movedRule(rule: Recurrence | null | undefined, days: number) {
  if (!rule?.byWeekday || days % 7 === 0) return undefined;
  return { ...rule, byWeekday: shiftWeekdays(rule.byWeekday, days) };
}

/** The rule to send: the form's when Repeat changed, else a moved one. */
function ruleChange(
  fields: EventFields,
  rule: Recurrence | null | undefined,
  days: number,
  repeatChanged: boolean,
): EventUpdate {
  if (repeatChanged) return { recurrence: fields.recurrence };
  const moved = movedRule(rule, days);
  return moved ? { recurrence: moved } : {};
}

type ScopeOptions = {
  /** Repeat changed in the form: its rule is sent as it is. */
  repeatChanged: boolean;
  /** The owner's zone, which all-day series and zoneless times are in. */
  ownerZone: string;
};

/**
 * This and following: the POST …/split body. The changes as for This event;
 * a new time is the new series' start. Unless Repeat changed, the new series
 * repeats like the old one (Calendar's default), its weekly days moving with
 * a move to another day.
 */
export function splitRequest(
  fields: EventFields,
  occurrence: CalendarEvent,
  { repeatChanged, ownerZone }: ScopeOptions,
): EventUpdate {
  let days = 0;
  let times: EventUpdate = {};
  if (scheduleChanged(fields, occurrence)) {
    times = scheduleBody(fields, occurrence);
    const from = wallStart(occurrence, seriesZone(occurrence, ownerZone));
    const to = wallStart(fields, seriesZone(fields, ownerZone));
    if (from && to) days = daysBetween(from, to);
  }
  return {
    ...contentChanges(fields, occurrence),
    ...times,
    ...ruleChange(fields, occurrence.recurrence, days, repeatChanged),
  };
}

/**
 * The series' new start and end when one of its occurrences moves to
 * `fields`' times: every occurrence moves by the same offset on the series'
 * wall clock, so a drag from Monday 18:00 to Tuesday 19:00 starts the series
 * a day and an hour later, across daylight saving changes. `days` is how
 * many days its start moved.
 */
export function movedSeries(
  fields: EventFields,
  occurrence: CalendarEvent,
  series: CalendarEvent,
  ownerZone: string,
): { schedule: EventSchedule; days: number } | undefined {
  const zone = seriesZone(series, ownerZone);
  const toZone = seriesZone(fields, ownerZone);
  const from = wallStart(occurrence, zone);
  const to = wallStart(fields, toZone);
  const start = wallStart(series, zone);
  if (!from || !to || !start) return undefined;
  const moved = new Date(wallTime(start) + wallTime(to) - wallTime(from))
    .toISOString()
    .slice(0, 16);
  const days = daysBetween(start, moved);
  if (fields.allDay) {
    const startDate = moved.slice(0, 10);
    const length = daysBetween(fields.startDate, fields.endDate);
    return {
      schedule: {
        allDay: true,
        startDate,
        endDate: addDays(startDate, Math.max(0, length)),
      },
      days,
    };
  }
  const startAt = fromDateTimeInput(moved, toZone);
  const length = Date.parse(fields.endAt) - Date.parse(fields.startAt);
  return {
    schedule: {
      allDay: false,
      startAt,
      endAt: new Date(Date.parse(startAt) + length).toISOString(),
      timeZone: fields.timeZone,
    },
    days,
  };
}

/**
 * All events: the PATCH body for the series (as stored, `series`). The
 * changes as for This event; a new time moves every occurrence by this
 * occurrence's offset (see `movedSeries`), weekly days included.
 */
export function seriesRequest(
  fields: EventFields,
  occurrence: CalendarEvent,
  series: CalendarEvent,
  { repeatChanged, ownerZone }: ScopeOptions,
): EventUpdate {
  let days = 0;
  let times: EventUpdate = {};
  if (scheduleChanged(fields, occurrence)) {
    const moved = movedSeries(fields, occurrence, series, ownerZone);
    if (moved) {
      times = scheduleBody(moved.schedule, series);
      days = moved.days;
    }
  }
  return {
    ...contentChanges(fields, occurrence),
    ...times,
    ...ruleChange(fields, series.recurrence, days, repeatChanged),
  };
}

/** The `message` of a Life OS API error body, if there is one. */
export function apiErrorMessage(error: unknown): string | undefined {
  if (typeof error !== "object" || error === null || !("error" in error)) {
    return undefined;
  }
  const body = (error as { error?: { message?: unknown } }).error;
  return typeof body?.message === "string" && body.message
    ? body.message
    : undefined;
}

const API_ERRORS: [
  RegExp,
  EventField,
  (string | ((match: RegExpMatchArray) => string))?,
][] = [
  [
    /byWeekday must include the weekday the event starts on \((\w{2})\)/i,
    "repeat",
    (match) =>
      `The repeat days must include ${WEEKDAY_NAME[match[1] as Weekday] ?? match[1]}, the day it starts.`,
  ],
  [
    /until must not be before the event starts/i,
    "repeat",
    "The repeat can't end before the event starts.",
  ],
  [/^recurrence/i, "repeat"],
  [/endAt must be after startAt/i, "end", "The end must be after the start."],
  [
    /endDate must not precede startDate/i,
    "end",
    "The end date can't be before the start date.",
  ],
  [/^title/i, "title"],
  [/timeZone/i, "timeZone"],
  [/reminder|minutesBefore|daysBefore|localTime/i, "reminders"],
];

/**
 * Places a Calendar API 400 message at the form field it concerns, in
 * plainer words where the API names its JSON fields; anything else is shown
 * for the whole form.
 */
export function fieldErrorsFromApi(message: string): FieldErrors {
  for (const [pattern, field, text] of API_ERRORS) {
    const match = message.match(pattern);
    if (!match) continue;
    return {
      [field]:
        typeof text === "function" ? text(match) : (text ?? sentence(message)),
    };
  }
  return { form: sentence(message) };
}

/** An API message as a sentence: capitalised, with a full stop. */
export function sentence(message: string) {
  const text = message.charAt(0).toUpperCase() + message.slice(1);
  return /[.!?]$/.test(text) ? text : `${text}.`;
}

// Reminder defaults -------------------------------------------------------------

/**
 * The settings PATCH body for the owner's default lists that changed;
 * undefined when neither did.
 */
export function settingsRequest(
  saved: ReminderDefaults,
  next: ReminderDefaults,
): UpdateSettingsRequest | undefined {
  const changed = {
    ...(sameReminders(saved.timed, next.timed) ? {} : { timed: next.timed }),
    ...(sameReminders(saved.allDay, next.allDay)
      ? {}
      : { allDay: next.allDay }),
  };
  return Object.keys(changed).length
    ? { defaultReminders: changed }
    : undefined;
}

/**
 * The source PATCH body for Reminder defaults: the default reminders and
 * default priority that changed; undefined when neither did.
 */
export function sourceDefaultsRequest(
  source: CalendarSource,
  next: { reminders: Reminder[]; priority: Priority },
): UpdateSourceRequest | undefined {
  const changed: UpdateSourceRequest = {
    ...(sameReminders(source.defaultReminders, next.reminders)
      ? {}
      : { defaultReminders: next.reminders }),
    ...(source.defaultPriority === next.priority
      ? {}
      : { defaultPriority: next.priority }),
  };
  return Object.keys(changed).length ? changed : undefined;
}

/**
 * The owner's default reminders, for the event form. Throws when Calendar
 * answers with an error or can't be reached.
 */
export async function fetchOwnerDefaults() {
  const { data, error } = await calendarClient.GET("/v1/settings");
  if (error || !data) throw new Error("Calendar is unavailable");
  return data.defaultReminders;
}

/**
 * Everything Reminder defaults edits: the owner's lists and every source
 * with its own. Throws when Calendar answers with an error or can't be
 * reached.
 */
export async function fetchReminderDefaults() {
  const [settings, sources] = await Promise.all([
    calendarClient.GET("/v1/settings"),
    calendarClient.GET("/v1/sources"),
  ]);
  if (!settings.data || !sources.data) {
    throw new Error("Calendar is unavailable");
  }
  return {
    owner: settings.data.defaultReminders,
    sources: sources.data.items,
  };
}

// Calendar API reads ------------------------------------------------------------

/** The list API's largest page. */
export const EVENTS_PAGE_SIZE = 100;
/** A safety cap on events loaded for one calendar range. */
export const RANGE_EVENTS_CAP = 1000;
/** Search results shown at once: the most relevant ones. */
export const SEARCH_LIMIT = 50;

/**
 * Every event overlapping `[from, to)`, following `nextCursor` up to the cap.
 * All-day events are matched as days in `timeZone`. Throws when Calendar
 * answers with an error or can't be reached.
 */
export async function fetchEventsInRange({
  from,
  to,
  timeZone,
  includeCancelled,
}: {
  from: Date;
  to: Date;
  timeZone: string;
  includeCancelled: boolean;
}) {
  const items: CalendarEvent[] = [];
  let cursor: string | undefined;
  do {
    const { data, error } = await calendarClient.GET("/v1/events", {
      params: {
        query: {
          from: from.toISOString(),
          to: to.toISOString(),
          timeZone,
          limit: EVENTS_PAGE_SIZE,
          ...(includeCancelled ? { includeCancelled } : {}),
          ...(cursor ? { cursor } : {}),
        },
      },
    });
    if (error || !data) throw new Error("Calendar is unavailable");
    items.push(...data.items);
    cursor = data.nextCursor ?? undefined;
  } while (cursor && items.length < RANGE_EVENTS_CAP);
  return items;
}

/**
 * The most relevant events matching every word of `query`, across all dates.
 * `more` is set when further matches exist. Throws on an error or abort.
 */
export async function searchEvents(
  query: string,
  {
    timeZone,
    includeCancelled,
    signal,
  }: { timeZone: string; includeCancelled: boolean; signal?: AbortSignal },
) {
  // The API takes at most 10 words and 200 characters.
  const q = query.trim().split(/\s+/).slice(0, 10).join(" ").slice(0, 200);
  const { data, error } = await calendarClient.GET("/v1/events/search", {
    params: {
      query: {
        q,
        timeZone,
        limit: SEARCH_LIMIT,
        ...(includeCancelled ? { includeCancelled } : {}),
      },
    },
    signal,
  });
  if (error || !data) throw new Error("Search is unavailable");
  return { items: data.items, more: Boolean(data.nextCursor) };
}

// Calendar API writes: occurrences of recurring events ----------------------------

/** What Calendar answered: the event it returned, or its error. */
export type WriteResult = {
  data?: CalendarEvent;
  error?: unknown;
  status: number;
};

const occurrencePath = (occurrence: CalendarEvent) => ({
  eventId: occurrence.id,
  // openapi-fetch URL-encodes it (an instant has colons).
  recurrenceId: occurrence.occurrence?.recurrenceId ?? "",
});

/**
 * Saves a change of an occurrence (`fields`, from the form or a drag) to
 * `scope`: This event PATCHes the occurrence, This and following splits the
 * series there (on its first occurrence Calendar changes the series), and
 * All events reads the series to PATCH it. Throws when Calendar can't be
 * reached.
 */
export async function saveOccurrence(
  scope: Scope,
  fields: EventFields,
  occurrence: CalendarEvent,
  options: ScopeOptions,
): Promise<WriteResult> {
  const path = occurrencePath(occurrence);
  if (scope === "this") {
    const { data, error, response } = await calendarClient.PATCH(
      "/v1/events/{eventId}/occurrences/{recurrenceId}",
      { params: { path }, body: occurrenceRequest(fields, occurrence) },
    );
    return { data, error, status: response.status };
  }
  if (scope === "following") {
    const { data, error, response } = await calendarClient.POST(
      "/v1/events/{eventId}/occurrences/{recurrenceId}/split",
      {
        params: { path },
        body: updateBody(splitRequest(fields, occurrence, options)),
      },
    );
    return { data, error, status: response.status };
  }
  const eventId = { eventId: occurrence.id };
  const series = await calendarClient.GET("/v1/events/{eventId}", {
    params: { path: eventId },
  });
  if (!series.data) {
    return { error: series.error, status: series.response.status };
  }
  const { data, error, response } = await calendarClient.PATCH(
    "/v1/events/{eventId}",
    {
      params: { path: eventId },
      body: updateBody(seriesRequest(fields, occurrence, series.data, options)),
    },
  );
  return { data, error, status: response.status };
}

/**
 * Deletes an occurrence to `scope`: This event skips it (`status:
 * cancelled`, kept as history and restorable), This and following ends the
 * series before it, All events deletes the series. Throws when Calendar
 * can't be reached.
 */
export async function deleteOccurrence(
  scope: Scope,
  occurrence: CalendarEvent,
): Promise<WriteResult> {
  const path = occurrencePath(occurrence);
  if (scope === "this") {
    const { data, error, response } = await calendarClient.PATCH(
      "/v1/events/{eventId}/occurrences/{recurrenceId}",
      { params: { path }, body: { status: "cancelled" } },
    );
    return { data, error, status: response.status };
  }
  const { error, response } =
    scope === "following"
      ? await calendarClient.POST(
          "/v1/events/{eventId}/occurrences/{recurrenceId}/truncate",
          { params: { path } },
        )
      : await calendarClient.DELETE("/v1/events/{eventId}", {
          params: { path: { eventId: occurrence.id } },
        });
  return { error, status: response.status };
}

/** Restores a skipped occurrence (`status: active`). */
export async function restoreOccurrence(
  occurrence: CalendarEvent,
): Promise<WriteResult> {
  const { data, error, response } = await calendarClient.PATCH(
    "/v1/events/{eventId}/occurrences/{recurrenceId}",
    {
      params: { path: occurrencePath(occurrence) },
      body: { status: "active" },
    },
  );
  return { data, error, status: response.status };
}

/**
 * The event (or the occurrence) as Calendar has it now, such as after a
 * change refused because it changed in Google meanwhile; undefined when it
 * no longer exists.
 */
export async function latestEvent(
  event: CalendarEvent,
): Promise<CalendarEvent | undefined> {
  const { data } = event.occurrence
    ? await calendarClient.GET(
        "/v1/events/{eventId}/occurrences/{recurrenceId}",
        { params: { path: occurrencePath(event) } },
      )
    : await calendarClient.GET("/v1/events/{eventId}", {
        params: { path: { eventId: event.id } },
      });
  return data;
}
