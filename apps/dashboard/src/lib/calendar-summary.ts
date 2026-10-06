import { calendarClient } from "@/lib/api/calendar-client";
import type { components } from "@/lib/api/generated/calendar";

export type CalendarEvent = components["schemas"]["CalendarEvent"];

export type CalendarSummaryState =
  | { kind: "ready"; today: CalendarEvent[]; upcoming: CalendarEvent[] }
  | { kind: "loading" }
  | { kind: "empty" }
  | { kind: "unavailable" };

function startOfToday() {
  const date = new Date();
  date.setUTCHours(0, 0, 0, 0);
  return date;
}

export async function getCalendarSummary(): Promise<CalendarSummaryState> {
  const from = startOfToday();
  const to = new Date(from);
  to.setUTCDate(to.getUTCDate() + 8);
  const { data, error } = await calendarClient.GET("/v1/events", {
    params: {
      query: { from: from.toISOString(), to: to.toISOString(), limit: 30 },
    },
  });

  if (error || !data) {
    return { kind: "unavailable" };
  }
  if (data.items.length === 0) {
    return { kind: "empty" };
  }

  const todayEnd = new Date(from);
  todayEnd.setUTCDate(todayEnd.getUTCDate() + 1);
  const today = data.items.filter((event) =>
    eventIsToday(event, from, todayEnd),
  );
  return {
    kind: "ready",
    today,
    upcoming: data.items.filter(
      (event) => !eventIsToday(event, from, todayEnd),
    ),
  };
}

function eventIsToday(event: CalendarEvent, from: Date, to: Date) {
  if (event.allDay) {
    const start = event.startDate
      ? new Date(`${event.startDate}T00:00:00Z`)
      : from;
    const end = event.endDate ? new Date(`${event.endDate}T23:59:59Z`) : start;
    return start < to && end >= from;
  }

  const start = event.startAt ? new Date(event.startAt) : from;
  const end = event.endAt ? new Date(event.endAt) : start;
  return start < to && end >= from;
}
