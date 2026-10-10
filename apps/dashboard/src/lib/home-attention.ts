import { calendarClient } from "@/lib/api/calendar-client";
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

export type HomeAttention = {
  humanReview: AttentionList<InboxItem> | null;
  urgent: { overdue: Task[]; today: Task[]; more: boolean } | null;
  unscheduled: AttentionList<Task> | null;
  nextEvent: { kind: "event"; event: CalendarEvent } | { kind: "none" } | null;
};

export const HOME_LIST_LIMIT = 100;
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

/** `null` in the result means the owning service could not be read. */
export function toHomeAttention(
  results: {
    review: ListResult<InboxItem>;
    urgent: ListResult<Task>;
    open: ListResult<Task>;
    events: ListResult<CalendarEvent>;
  },
  now: Date,
  timeZone: string = DEFAULT_TIME_ZONE,
): HomeAttention {
  const { review, urgent, open, events } = results;
  const { start } = ownerToday(now, timeZone);
  const isOverdue = (task: Task) =>
    task.dueAt !== undefined &&
    task.dueAt !== null &&
    new Date(task.dueAt) < start;
  const unscheduled = open.data?.items.filter((task) => !task.dueAt);
  const nextEvent = events.data?.items[0];

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
        ? { kind: "event", event: nextEvent }
        : { kind: "none" }
      : null,
  };
}

/** A service that cannot be reached at all reads the same as an HTTP error. */
export function unavailableOnFailure<T>(request: Promise<ListResult<T>>) {
  return request.catch((): ListResult<T> => ({}));
}

export async function getHomeAttention(): Promise<HomeAttention> {
  const now = new Date();
  const timeZone = await userTimeZone();
  const eventWindowEnd = new Date(now);
  eventWindowEnd.setUTCDate(eventWindowEnd.getUTCDate() + 30);
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
    unavailableOnFailure(
      calendarClient.GET("/v1/events", {
        params: {
          query: {
            from: now.toISOString(),
            to: eventWindowEnd.toISOString(),
            limit: 1,
          },
        },
      }),
    ),
  ]);
  return toHomeAttention({ review, urgent, open, events }, now, timeZone);
}
