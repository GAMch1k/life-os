import { calendarClient } from "@/lib/api/calendar-client";
import { inboxClient } from "@/lib/api/inbox-client";
import { tasksClient } from "@/lib/api/tasks-client";
import type { components as Calendar } from "@/lib/api/generated/calendar";
import type { components as Inbox } from "@/lib/api/generated/inbox";
import type { components as Tasks } from "@/lib/api/generated/tasks";

export type HomeAttention = {
  humanReview: Inbox["schemas"]["InboxItem"][] | null;
  overdue: Tasks["schemas"]["Task"][] | null;
  today: Tasks["schemas"]["Task"][] | null;
  unscheduled: Tasks["schemas"]["Task"][] | null;
  nextEvent: Calendar["schemas"]["CalendarEvent"] | null | undefined;
};

export async function getHomeAttention(): Promise<HomeAttention> {
  const now = new Date(),
    start = new Date(now);
  start.setUTCHours(0, 0, 0, 0);
  const tomorrow = new Date(start);
  tomorrow.setUTCDate(tomorrow.getUTCDate() + 1);
  const eventWindowEnd = new Date(now);
  eventWindowEnd.setUTCDate(eventWindowEnd.getUTCDate() + 30);
  const [tasks, review, events] = await Promise.all([
    tasksClient.GET("/v1/tasks", {
      params: { query: { completed: false, limit: 100 } },
    }),
    inboxClient.GET("/v1/inbox-items", {
      params: { query: { state: "human_review", limit: 10 } },
    }),
    calendarClient.GET("/v1/events", {
      params: {
        query: {
          from: now.toISOString(),
          to: eventWindowEnd.toISOString(),
          limit: 1,
        },
      },
    }),
  ]);
  const open = tasks.data?.items,
    due = (item: Tasks["schemas"]["Task"]) =>
      item.dueAt ? new Date(item.dueAt) : undefined;
  return {
    humanReview: review.data?.items ?? null,
    overdue: open
      ? open.filter((item) => {
          const value = due(item);
          return value !== undefined && value < start;
        })
      : null,
    today: open
      ? open.filter((item) => {
          const value = due(item);
          return value !== undefined && value >= start && value < tomorrow;
        })
      : null,
    unscheduled: open ? open.filter((item) => !item.dueAt).slice(0, 3) : null,
    nextEvent: events.data?.items[0],
  };
}
