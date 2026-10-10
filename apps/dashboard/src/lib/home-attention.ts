import { calendarClient } from "@/lib/api/calendar-client";
import type { components as CalendarComponents } from "@/lib/api/generated/calendar";
import { inboxClient } from "@/lib/api/inbox-client";
import { tasksClient } from "@/lib/api/tasks-client";
import type { CalendarEvent } from "@/lib/calendar-summary";
import type { InboxItem } from "@/lib/inbox-summary";
import type { Task } from "@/lib/tasks-summary";
import {
  addDays,
  dayKey,
  DEFAULT_TIME_ZONE,
  startOfDay,
} from "@/lib/time-zone";
import { userTimeZone } from "@/lib/user-time-zone";

// Home is a read-only projection over the owning services' list APIs. It only
// decides what to surface; every change happens in the dedicated workspaces.

type ListResult<T> = {
  data?: { items: T[]; nextCursor?: string | null };
};

/** A capped slice of a list, with `more` set when the service had more rows. */
export type AttentionList<T> = { items: T[]; more: boolean };

export type CalendarSource = CalendarComponents["schemas"]["CalendarSource"];

export type NextEvent = {
  kind: "event";
  /** The event, or the occurrence of a recurring event, that comes next. */
  event: CalendarEvent;
  /** Where an imported event comes from, such as "ZAW"; unset for yours. */
  sourceName?: string;
  /** The Calendar at the event's first day. */
  href: string;
};

export type HomeAttention = {
  humanReview: AttentionList<InboxItem> | null;
  urgent: { overdue: Task[]; today: Task[]; more: boolean } | null;
  unscheduled: AttentionList<Task> | null;
  nextEvent: NextEvent | { kind: "none" } | null;
};

export const HOME_LIST_LIMIT = 100;
/** How far ahead Home looks for the next event. */
export const NEXT_EVENT_DAYS = 30;
/** Events read per page, and at most this many pages, to find the next one. */
export const NEXT_EVENT_PAGE = 20;
export const NEXT_EVENT_PAGES = 3;
export const UNSCHEDULED_PREVIEW = 3;
/** Human Review previews fetched; the true count comes from the backlog. */
export const REVIEW_LIMIT = 10;

/**
 * The owner's current day: its first instant and its last millisecond, in
 * the owner's timezone, so "overdue" and "due today" match the Tasks page
 * rather than UTC midnight.
 */
export function ownerToday(now: Date, timeZone: string) {
  const today = dayKey(now, timeZone);
  return {
    start: startOfDay(today, timeZone),
    end: new Date(startOfDay(addDays(today, 1), timeZone).getTime() - 1),
  };
}

/**
 * When an event starts, as epoch milliseconds: its start instant, or the
 * beginning of an all-day event's first day in the owner's timezone.
 */
export function eventStart(event: CalendarEvent, timeZone: string) {
  if (event.allDay) {
    return event.startDate
      ? startOfDay(event.startDate, timeZone).getTime()
      : undefined;
  }
  return event.startAt ? Date.parse(event.startAt) : undefined;
}

/**
 * The next relevant event among `items` (listed by start, recurring events
 * as their occurrences): the first active one that has not started yet. An
 * event under way, including an all-day event of today, is not "next".
 */
export function nextUpcomingEvent(
  items: CalendarEvent[],
  now: Date,
  timeZone: string,
) {
  return items.find((event) => {
    if (event.status === "cancelled") return false;
    const start = eventStart(event, timeZone);
    return start !== undefined && start >= now.getTime();
  });
}

/** The name of the calendar an imported event comes from; unset for yours. */
export function eventSourceName(
  event: CalendarEvent,
  sources: CalendarSource[] | undefined,
) {
  if (event.source.kind === "manual") return undefined;
  if (event.source.kind === "external") {
    // An integration's source: its name, else its provider's label.
    return (
      event.source.name?.trim() || event.source.provider?.trim() || undefined
    );
  }
  const source = sources?.find((entry) => entry.id === event.source.sourceId);
  if (source) return source.name;
  return event.source.kind === "google" ? "Google Calendar" : "ICS feed";
}

/** The Calendar at the owner's day an event starts on. */
export function calendarDayHref(event: CalendarEvent, timeZone: string) {
  const day = event.allDay
    ? event.startDate
    : event.startAt
      ? dayKey(event.startAt, timeZone)
      : undefined;
  return day ? `/calendar?date=${day}` : "/calendar";
}

/** `null` in the result means the owning service could not be read. */
export function toHomeAttention(
  results: {
    review: ListResult<InboxItem>;
    urgent: ListResult<Task>;
    open: ListResult<Task>;
    events: ListResult<CalendarEvent>;
    /** Calendar sources, read when the next event is imported. */
    sources?: ListResult<CalendarSource>;
  },
  now: Date,
  timeZone: string = DEFAULT_TIME_ZONE,
): HomeAttention {
  const { review, urgent, open, events, sources } = results;
  const { start } = ownerToday(now, timeZone);
  const isOverdue = (task: Task) =>
    task.dueAt !== undefined &&
    task.dueAt !== null &&
    new Date(task.dueAt) < start;
  const unscheduled = open.data?.items.filter((task) => !task.dueAt);
  const nextEvent = events.data
    ? nextUpcomingEvent(events.data.items, now, timeZone)
    : undefined;

  return {
    // The Inbox list does not page, so a full page is the only sign of more.
    humanReview: review.data
      ? {
          items: review.data.items,
          more:
            Boolean(review.data.nextCursor) ||
            review.data.items.length >= REVIEW_LIMIT,
        }
      : null,
    urgent: urgent.data
      ? {
          overdue: urgent.data.items.filter(isOverdue),
          today: urgent.data.items.filter((task) => !isOverdue(task)),
          more: Boolean(urgent.data.nextCursor),
        }
      : null,
    unscheduled: unscheduled
      ? {
          items: unscheduled.slice(0, UNSCHEDULED_PREVIEW),
          more:
            unscheduled.length > UNSCHEDULED_PREVIEW ||
            Boolean(open.data?.nextCursor),
        }
      : null,
    nextEvent: events.data
      ? nextEvent
        ? {
            kind: "event",
            event: nextEvent,
            sourceName: eventSourceName(nextEvent, sources?.data?.items),
            href: calendarDayHref(nextEvent, timeZone),
          }
        : { kind: "none" }
      : null,
  };
}

/** A service that cannot be reached at all reads the same as an HTTP error. */
export function unavailableOnFailure<T>(request: Promise<ListResult<T>>) {
  return request.catch((): ListResult<T> => ({}));
}

/**
 * Events from now on, page by page, until one has not started yet: events
 * under way come first in the list, and are not what Home shows.
 */
async function upcomingEvents(
  now: Date,
  timeZone: string,
): Promise<ListResult<CalendarEvent>> {
  const to = new Date(now);
  to.setUTCDate(to.getUTCDate() + NEXT_EVENT_DAYS);
  const items: CalendarEvent[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < NEXT_EVENT_PAGES; page++) {
    const { data } = await calendarClient.GET("/v1/events", {
      params: {
        query: {
          from: now.toISOString(),
          to: to.toISOString(),
          // All-day events are matched as the owner's days in this zone.
          timeZone,
          limit: NEXT_EVENT_PAGE,
          cursor,
        },
      },
    });
    if (!data) return page === 0 ? {} : { data: { items } };
    items.push(...data.items);
    if (nextUpcomingEvent(data.items, now, timeZone) || !data.nextCursor) {
      break;
    }
    cursor = data.nextCursor;
  }
  return { data: { items } };
}

export async function getHomeAttention(): Promise<HomeAttention> {
  const now = new Date();
  const timeZone = await userTimeZone();
  const [review, urgent, open, events] = await Promise.all([
    unavailableOnFailure(
      inboxClient.GET("/v1/inbox-items", {
        params: { query: { state: "human_review", limit: REVIEW_LIMIT } },
      }),
    ),
    // Tasks are ordered by due date, so overdue work comes first.
    unavailableOnFailure(
      tasksClient.GET("/v1/tasks", {
        params: {
          query: {
            completed: false,
            dueBefore: ownerToday(now, timeZone).end.toISOString(),
            includeUndated: false,
            limit: HOME_LIST_LIMIT,
          },
        },
      }),
    ),
    // Only undated Tasks, so any number of dated ones cannot crowd them out.
    unavailableOnFailure(
      tasksClient.GET("/v1/tasks", {
        params: {
          query: { completed: false, undated: true, limit: HOME_LIST_LIMIT },
        },
      }),
    ),
    unavailableOnFailure(upcomingEvents(now, timeZone)),
  ]);
  // An imported event is labelled with its source's name, which an
  // integration's event carries itself.
  const next = events.data
    ? nextUpcomingEvent(events.data.items, now, timeZone)
    : undefined;
  const sources =
    next && next.source.kind !== "manual" && next.source.kind !== "external"
      ? await unavailableOnFailure(calendarClient.GET("/v1/sources"))
      : undefined;
  return toHomeAttention(
    { review, urgent, open, events, sources },
    now,
    timeZone,
  );
}
