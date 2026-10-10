import type {
  CalendarEvent,
  CalendarSummaryState,
} from "@/lib/calendar-summary";
import { occurrenceKey } from "@/lib/calendar-workspace";
import { dateTimeFormat, dayKey } from "@/lib/time-zone";

const DAY = { weekday: "short", day: "numeric", month: "short" } as const;
const TIME = { hour: "2-digit", minute: "2-digit" } as const;

function allDayLabel(event: CalendarEvent) {
  if (!event.startDate) {
    return "All day";
  }
  // A date is not an instant: it reads the same in every timezone.
  const day = (date: string) =>
    dateTimeFormat("UTC", DAY).format(new Date(`${date}T00:00:00Z`));
  return event.endDate && event.endDate !== event.startDate
    ? `${day(event.startDate)} – ${day(event.endDate)} · All day`
    : `${day(event.startDate)} · All day`;
}

function timedLabel(event: CalendarEvent, timeZone: string) {
  if (!event.startAt) {
    return "Time not set";
  }
  const start = new Date(event.startAt);
  const label = `${dateTimeFormat(timeZone, DAY).format(start)} · ${dateTimeFormat(timeZone, TIME).format(start)}`;
  const end = event.endAt ? new Date(event.endAt) : null;
  return end && end > start && dayKey(end, timeZone) === dayKey(start, timeZone)
    ? `${label}–${dateTimeFormat(timeZone, TIME).format(end)}`
    : label;
}

/** When an event happens, in the Dashboard's timezone. */
export function eventTimeLabel(event: CalendarEvent, timeZone: string) {
  return event.allDay ? allDayLabel(event) : timedLabel(event, timeZone);
}

function EventList({
  events,
  timeZone,
}: {
  events: CalendarEvent[];
  timeZone: string;
}) {
  return (
    <ul className="bg-card divide-y rounded-lg border">
      {events.map((event) => (
        <li className="min-w-0 px-3 py-2.5" key={occurrenceKey(event)}>
          <p className="font-medium break-words">{event.title}</p>
          <time
            className="text-muted-foreground mt-0.5 block text-xs"
            dateTime={event.startAt ?? event.startDate ?? undefined}
          >
            {eventTimeLabel(event, timeZone)}
          </time>
        </li>
      ))}
    </ul>
  );
}

function Agenda({ state }: { state: CalendarSummaryState }) {
  if (state.kind === "loading") {
    return (
      <p aria-busy="true" className="text-muted-foreground text-sm">
        Loading your calendar…
      </p>
    );
  }
  if (state.kind === "unavailable") {
    return (
      <p className="text-muted-foreground text-sm">
        Calendar is unavailable right now.
      </p>
    );
  }
  if (state.kind === "empty") {
    return (
      <p className="text-muted-foreground text-sm">
        No events in the next week.
      </p>
    );
  }
  return (
    <>
      <section aria-labelledby="calendar-today" className="space-y-2">
        <h2 className="text-sm font-medium" id="calendar-today">
          Today
        </h2>
        {state.today.length ? (
          <EventList events={state.today} timeZone={state.timeZone} />
        ) : (
          <p className="text-muted-foreground text-sm">
            Nothing else on the calendar today.
          </p>
        )}
      </section>
      <section aria-labelledby="calendar-upcoming" className="space-y-2">
        <h2 className="text-sm font-medium" id="calendar-upcoming">
          Upcoming
        </h2>
        {state.upcoming.length ? (
          <EventList events={state.upcoming} timeZone={state.timeZone} />
        ) : (
          <p className="text-muted-foreground text-sm">
            No more events in the next week.
          </p>
        )}
      </section>
    </>
  );
}

export function CalendarSummary({ state }: { state: CalendarSummaryState }) {
  return (
    <div
      className="mx-auto max-w-5xl space-y-5"
      data-time-zone={state.kind === "ready" ? state.timeZone : undefined}
      id="calendar"
    >
      <h1 className="text-xl font-semibold tracking-tight">Calendar</h1>
      <Agenda state={state} />
    </div>
  );
}
