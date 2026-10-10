import { calendarClient } from "@/lib/api/calendar-client";
import type { components } from "@/lib/api/generated/calendar";
import {
  addDays,
  dayKey,
  DEFAULT_TIME_ZONE,
  startOfDay,
  userTimeZone,
} from "@/lib/time-zone";

export type CalendarEvent = components["schemas"]["CalendarEvent"];

export type CalendarSummaryState =
  | {
      kind: "ready";
      today: CalendarEvent[];
      upcoming: CalendarEvent[];
      /** The owner's timezone the events are shown in. */
      timeZone: string;
    }
  | { kind: "loading" }
  | { kind: "empty" }
  | { kind: "unavailable" };

/** Today and the next seven days, as days in the owner's timezone. */
export const CALENDAR_DAYS = 8;

/** The owner's day of `now` and the instants that bound the agenda. */
export function agendaWindow(now: Date, timeZone: string) {
  const today = dayKey(now, timeZone);
  return {
    today,
    from: startOfDay(today, timeZone),
    tomorrow: startOfDay(addDays(today, 1), timeZone),
    to: startOfDay(addDays(today, CALENDAR_DAYS), timeZone),
  };
}

export async function getCalendarSummary(): Promise<CalendarSummaryState> {
  const now = new Date();
  const timeZone = userTimeZone();
  const { from, to } = agendaWindow(now, timeZone);
  const { data, error } = await calendarClient.GET("/v1/events", {
    params: {
      query: { from: from.toISOString(), to: to.toISOString(), limit: 30 },
    },
  });

  if (error || !data) {
    return { kind: "unavailable" };
  }
  return toCalendarSummary(data.items, now, timeZone);
}

/**
 * Splits events into the owner's today and the days after. All-day events
 * are compared as dates; timed events as instants against local midnight.
 */
export function toCalendarSummary(
  items: CalendarEvent[],
  now: Date,
  timeZone: string = DEFAULT_TIME_ZONE,
): CalendarSummaryState {
  const agenda = agendaWindow(now, timeZone);
  // Calendar matches all-day events by the UTC date of `from`, which can be
  // the owner's yesterday; anything over before today is dropped.
  const current = items.filter((event) => !endsBeforeToday(event, agenda));
  if (current.length === 0) {
    return { kind: "empty" };
  }
  // What is not over yet and starts before tomorrow is today's.
  const isToday = (event: CalendarEvent) =>
    event.allDay
      ? (event.startDate ?? agenda.today) <= agenda.today
      : !event.startAt || Date.parse(event.startAt) < agenda.tomorrow.getTime();
  return {
    kind: "ready",
    today: current.filter(isToday),
    upcoming: current.filter((event) => !isToday(event)),
    timeZone,
  };
}

type Agenda = ReturnType<typeof agendaWindow>;

function endsBeforeToday(event: CalendarEvent, agenda: Agenda) {
  if (event.allDay) {
    return (event.endDate ?? event.startDate ?? agenda.today) < agenda.today;
  }
  const start = event.startAt ? Date.parse(event.startAt) : Infinity;
  const end = event.endAt ? Date.parse(event.endAt) : start;
  // An event that ended exactly at midnight belongs to yesterday.
  return end <= agenda.from.getTime() && start < agenda.from.getTime();
}
