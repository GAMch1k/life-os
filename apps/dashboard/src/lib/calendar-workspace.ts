import type { EventInput } from "@fullcalendar/core";

import { calendarClient } from "@/lib/api/calendar-client";
import type { components } from "@/lib/api/generated/calendar";
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
type CreateEventRequest = components["schemas"]["CreateEventRequest"];
type UpdateEventRequest = components["schemas"]["UpdateEventRequest"];

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

/** Why an imported event can't be edited here; undefined for Life OS events. */
export function readOnlyReason(event: CalendarEvent) {
  const from =
    event.source.kind === "ics"
      ? (sourceName(event) ?? "an ICS feed")
      : event.source.kind === "google"
        ? (sourceName(event) ?? "Google Calendar")
        : undefined;
  return from
    ? `Imported from ${from}, so it can only be changed there. Its reminders can still be adjusted here.`
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
 * later. Imported, cancelled, and recurring events can't be dragged.
 */
export function toEventInput(event: CalendarEvent): EventInput | undefined {
  const imported = isImported(event);
  const cancelled = isCancelled(event);
  const common: EventInput = {
    id: occurrenceKey(event),
    title: event.title,
    editable: !imported && !cancelled && !event.recurrence,
    classNames: [
      "lifeos-event",
      ...(imported ? ["lifeos-event-imported"] : []),
      ...(cancelled ? ["lifeos-event-cancelled"] : []),
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

/**
 * The PATCH body for an event dragged or resized in FullCalendar. All-day
 * ends come back exclusive and are stored inclusive; a timed event dropped
 * from the all-day row has no end yet and keeps its length, or gets an hour.
 */
export function rescheduleRequest(
  change: { allDay: boolean; start: Date; end: Date | null },
  original: CalendarEvent,
): UpdateEventRequest {
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

/** The reminders as lines; null follows the defaults, [] means none. */
export function remindersSummary(reminders: Reminder[] | null | undefined) {
  if (reminders == null) return ["Default reminders"];
  if (reminders.length === 0) return ["No reminders"];
  return reminders.map(reminderLabel);
}

export function recurrenceLabel(
  recurrence: NonNullable<CalendarEvent["recurrence"]>,
  timeZone: string,
) {
  const units = {
    daily: "day",
    weekly: "week",
    monthly: "month",
    yearly: "year",
  };
  const every =
    recurrence.interval > 1
      ? `Repeats every ${plural(recurrence.interval, units[recurrence.frequency])}`
      : `Repeats ${recurrence.frequency}`;
  return recurrence.until
    ? `${every} until ${dateTimeFormat(timeZone, { day: "numeric", month: "short", year: "numeric" }).format(new Date(recurrence.until))}`
    : every;
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

export type ReminderMode = "default" | "none" | "custom";

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
  reminderMode: ReminderMode;
  reminders: ReminderDraft[];
};

export type EventField = "title" | "start" | "end" | "timeZone" | "reminders";
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

function splitInput(value: string) {
  const [date = "", time = ""] = value.split("T");
  return { date, time: time.slice(0, 5) };
}

/** The zone a timed event's times are edited in: its own, else the owner's. */
export function editingZone(timeZone: string, ownerZone: string) {
  return timeZone && isValidTimeZone(timeZone) ? timeZone : ownerZone;
}

function reminderFields(reminders: Reminder[] | null | undefined) {
  if (reminders == null) {
    return { reminderMode: "default" as const, reminders: [] };
  }
  if (reminders.length === 0) {
    return { reminderMode: "none" as const, reminders: [] };
  }
  return {
    reminderMode: "custom" as const,
    reminders: reminders.map(toReminderDraft),
  };
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
    ...reminderFields(event.reminders),
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
    reminderMode: "default" as const,
    reminders: [],
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

/** An event's fields read from the form; null clears an optional one. */
export type EventFields = {
  title: string;
  description: string | null;
  location: string | null;
  reminders: Reminder[] | null;
} & (
  | { allDay: true; startDate: string; endDate: string }
  | { allDay: false; startAt: string; endAt: string; timeZone: string | null }
);

const TIME_INPUT = /^\d{2}:\d{2}$/;

/**
 * Reads the form. Only what the form itself can tell is checked here (a
 * title, the dates and times filled in, complete reminders); the Calendar
 * API validates the rest, such as an end before the start, and its message
 * is shown at the field it concerns (see `fieldErrorsFromApi`).
 */
export function readDraft(
  draft: EventDraft,
  ownerZone: string,
): { fields: EventFields } | { errors: FieldErrors } {
  const errors: FieldErrors = {};
  const title = draft.title.trim();
  if (!title) errors.title = "Add a title.";

  let reminders: Reminder[] | null = null;
  if (draft.reminderMode === "none") reminders = [];
  if (draft.reminderMode === "custom" && draft.reminders.length) {
    const read = draft.reminders.map(fromReminderDraft);
    const missing = read.findIndex((reminder) => !reminder);
    if (missing >= 0) {
      errors.reminders = `Complete reminder ${missing + 1}: a whole number and, for a day reminder, a time.`;
    } else {
      reminders = read as Reminder[];
    }
  }

  const common = {
    title,
    description: draft.notes.trim() || null,
    location: draft.location.trim() || null,
    reminders,
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

/** The create body: empty optional fields and default reminders left out. */
export function createRequest(fields: EventFields): CreateEventRequest {
  const body: CreateEventRequest = {
    title: fields.title,
    allDay: fields.allDay,
    ...(fields.description ? { description: fields.description } : {}),
    ...(fields.location ? { location: fields.location } : {}),
    ...(fields.reminders ? { reminders: fields.reminders } : {}),
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
 * The PATCH body: every field the form edits, null where it was cleared.
 * Switching between timed and all-day sends the new kind's start and end;
 * the API clears the other kind's fields.
 */
export function updateRequest(fields: EventFields): UpdateEventRequest {
  const body: UpdateEventRequest = {
    title: fields.title,
    description: fields.description,
    location: fields.location,
    reminders: fields.reminders,
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

const API_ERRORS: [RegExp, EventField, string?][] = [
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
    if (pattern.test(message)) return { [field]: text ?? sentence(message) };
  }
  return { form: sentence(message) };
}

function sentence(message: string) {
  const text = message.charAt(0).toUpperCase() + message.slice(1);
  return /[.!?]$/.test(text) ? text : `${text}.`;
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
