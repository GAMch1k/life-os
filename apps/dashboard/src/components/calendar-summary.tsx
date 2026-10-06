import { CalendarDays } from "lucide-react";

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import type {
  CalendarEvent,
  CalendarSummaryState,
} from "@/lib/calendar-summary";

function allDayLabel(event: CalendarEvent) {
  if (!event.startDate) {
    return "All day";
  }
  const start = new Date(`${event.startDate}T00:00:00Z`);
  const date = new Intl.DateTimeFormat(undefined, {
    weekday: "short",
    month: "short",
    day: "numeric",
    timeZone: "UTC",
  }).format(start);
  return event.endDate && event.endDate !== event.startDate
    ? `${date} · All day`
    : `All day · ${date}`;
}

function timedLabel(event: CalendarEvent) {
  if (!event.startAt) {
    return "Time not set";
  }
  const start = new Date(event.startAt);
  const day = new Intl.DateTimeFormat(undefined, {
    weekday: "short",
    month: "short",
    day: "numeric",
  }).format(start);
  const time = new Intl.DateTimeFormat(undefined, {
    hour: "numeric",
    minute: "2-digit",
  }).format(start);
  return `${day} · ${time}`;
}

function EventList({ events }: { events: CalendarEvent[] }) {
  return (
    <ul className="divide-y rounded-lg border">
      {events.map((event) => (
        <li className="p-3 sm:p-4" key={event.id}>
          <p className="font-medium">{event.title}</p>
          <p className="text-muted-foreground mt-1 text-xs">
            {event.allDay ? allDayLabel(event) : timedLabel(event)}
          </p>
        </li>
      ))}
    </ul>
  );
}

export function CalendarSummary({ state }: { state: CalendarSummaryState }) {
  if (state.kind === "loading") {
    return (
      <Card id="calendar" aria-busy="true">
        <CardHeader>
          <CardTitle>Calendar</CardTitle>
          <CardDescription>Loading your upcoming calendar…</CardDescription>
        </CardHeader>
      </Card>
    );
  }

  if (state.kind === "unavailable") {
    return (
      <Card id="calendar">
        <CardHeader>
          <CardTitle>Calendar</CardTitle>
          <CardDescription>
            Calendar is temporarily unavailable. Your events have not been
            replaced with placeholders.
          </CardDescription>
        </CardHeader>
      </Card>
    );
  }

  if (state.kind === "empty") {
    return (
      <Card id="calendar">
        <CardHeader>
          <CardTitle>Calendar</CardTitle>
          <CardDescription>No events in the next week.</CardDescription>
        </CardHeader>
      </Card>
    );
  }

  return (
    <Card id="calendar">
      <CardHeader>
        <div className="flex items-center gap-2">
          <CalendarDays aria-hidden="true" className="size-4" />
          <CardTitle>Calendar</CardTitle>
        </div>
        <CardDescription>Your day and the next seven days.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <section>
          <h3 className="mb-2 text-sm font-medium">Today</h3>
          {state.today.length ? (
            <EventList events={state.today} />
          ) : (
            <p className="text-muted-foreground text-sm">
              Nothing else on the calendar today.
            </p>
          )}
        </section>
        <section>
          <h3 className="mb-2 text-sm font-medium">Upcoming</h3>
          {state.upcoming.length ? (
            <EventList events={state.upcoming} />
          ) : (
            <p className="text-muted-foreground text-sm">
              No more events in the next week.
            </p>
          )}
        </section>
      </CardContent>
    </Card>
  );
}
