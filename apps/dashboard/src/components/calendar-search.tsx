"use client";

import { Autocomplete } from "@base-ui/react/autocomplete";
import { Search, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import {
  type CalendarEvent,
  eventWhenLabel,
  isCancelled,
  SEARCH_LIMIT,
  searchEvents,
  sourceLabel,
  splitSearchResults,
} from "@/lib/calendar-workspace";
import { cn } from "@/lib/utils";

/** Waits this long after the last keystroke before searching. */
const SEARCH_DELAY_MS = 250;

type Results = {
  query: string;
  groups: { value: string; items: CalendarEvent[] }[];
  count: number;
  more: boolean;
};

/**
 * Searches every event, past and future, by the words in its title, notes,
 * and location (Calendar `GET /v1/events/search`), like Google Calendar's
 * search box. Results are a dated list, upcoming first and then past ones;
 * arrow keys move through them and Enter (or a tap) opens one.
 */
export function CalendarSearch({
  className,
  includeCancelled,
  onChoose,
  timeZone,
  today,
}: {
  className?: string;
  includeCancelled: boolean;
  onChoose: (event: CalendarEvent) => void;
  /** The zone result times are shown in. */
  timeZone: string;
  /** The owner's today (YYYY-MM-DD): dates in other years show their year. */
  today: string;
}) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<Results | null>(null);
  const [state, setState] = useState<"idle" | "searching" | "failed">("idle");
  const request = useRef<AbortController | null>(null);

  // Searches once typing pauses, and again when the cancelled toggle or the
  // zone changes; a newer search aborts the one in flight.
  useEffect(() => {
    const words = query.trim();
    if (!words) return;
    const timer = window.setTimeout(() => {
      request.current?.abort();
      const controller = new AbortController();
      request.current = controller;
      setState("searching");
      searchEvents(words, {
        timeZone,
        includeCancelled,
        signal: controller.signal,
      })
        .then(({ items, more }) => {
          if (controller.signal.aborted) return;
          const { upcoming, past } = splitSearchResults(
            items,
            new Date(),
            timeZone,
          );
          setResults({
            query: words,
            groups: [
              { value: "Upcoming", items: upcoming },
              { value: "Past", items: past },
            ].filter((group) => group.items.length),
            count: items.length,
            more,
          });
          setState("idle");
        })
        .catch(() => {
          if (!controller.signal.aborted) setState("failed");
        });
    }, SEARCH_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [query, includeCancelled, timeZone]);

  useEffect(() => () => request.current?.abort(), []);

  const change = (value: string) => {
    setQuery(value);
    if (!value.trim()) {
      request.current?.abort();
      setResults(null);
      setState("idle");
    }
  };

  const shown = query.trim() ? results : null;
  const status =
    state === "failed"
      ? "Search is unavailable right now."
      : state === "searching" && !shown
        ? "Searching…"
        : shown && shown.count === 0
          ? `No events match “${shown.query}”.`
          : shown
            ? `${shown.count} ${shown.count === 1 ? "event" : "events"}${shown.more ? `, the ${SEARCH_LIMIT} most relevant` : ""}`
            : null;

  return (
    <div className={cn("relative", className)} role="search">
      <Autocomplete.Root
        filter={null}
        items={shown?.groups ?? []}
        itemToStringValue={(event: CalendarEvent) => event.title}
        openOnInputClick
        value={query}
        onValueChange={(value, details) => {
          // Choosing a result opens it; the query stays in the box.
          if (details.reason !== "item-press") change(value);
        }}
      >
        <Search
          aria-hidden="true"
          className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2"
        />
        <Autocomplete.Input
          aria-label="Search events"
          className="border-input placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 dark:bg-input/30 h-8 w-full min-w-0 rounded-lg border bg-transparent py-1 pr-8 pl-8 text-base transition-colors outline-none focus-visible:ring-3 md:text-sm"
          placeholder="Search events"
        />
        {query ? (
          <Autocomplete.Clear
            aria-label="Clear search"
            className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 absolute top-1/2 right-1 flex size-6 -translate-y-1/2 items-center justify-center rounded-md outline-none focus-visible:ring-3"
          >
            <X className="size-3.5" />
          </Autocomplete.Clear>
        ) : null}
        <Autocomplete.Portal hidden={!status}>
          <Autocomplete.Positioner
            align="end"
            className="z-50 outline-none"
            sideOffset={4}
          >
            <Autocomplete.Popup
              aria-busy={state === "searching" || undefined}
              className="bg-popover text-popover-foreground ring-foreground/10 w-[max(var(--anchor-width),20rem)] max-w-[calc(100vw-1.5rem)] overflow-hidden rounded-lg shadow-md ring-1"
              data-testid="calendar-search-results"
            >
              <div className="max-h-[min(var(--available-height),26rem)] scroll-py-1 overflow-y-auto overscroll-contain p-1">
                <Autocomplete.Status>
                  {status ? (
                    <p className="text-muted-foreground px-2 py-1.5 text-xs">
                      {status}
                    </p>
                  ) : null}
                </Autocomplete.Status>
                <Autocomplete.List>
                  {(group: Results["groups"][number]) => (
                    <Autocomplete.Group
                      className="pb-1 last:pb-0"
                      items={group.items}
                      key={group.value}
                    >
                      <Autocomplete.GroupLabel className="text-muted-foreground px-2 pt-2 pb-1 text-xs font-medium">
                        {group.value}
                      </Autocomplete.GroupLabel>
                      <Autocomplete.Collection>
                        {(event: CalendarEvent) => (
                          <SearchResult
                            event={event}
                            key={`${event.id}-${event.startAt ?? event.startDate}`}
                            label={eventWhenLabel(event, timeZone, today)}
                            onChoose={onChoose}
                          />
                        )}
                      </Autocomplete.Collection>
                    </Autocomplete.Group>
                  )}
                </Autocomplete.List>
              </div>
            </Autocomplete.Popup>
          </Autocomplete.Positioner>
        </Autocomplete.Portal>
      </Autocomplete.Root>
    </div>
  );
}

function SearchResult({
  event,
  label,
  onChoose,
}: {
  event: CalendarEvent;
  label: string;
  onChoose: (event: CalendarEvent) => void;
}) {
  const cancelled = isCancelled(event);
  const source = sourceLabel(event);
  return (
    <Autocomplete.Item
      className="data-highlighted:bg-accent data-highlighted:text-accent-foreground grid cursor-default gap-0.5 rounded-md px-2 py-1.5 outline-none select-none"
      onClick={() => onChoose(event)}
      value={event}
    >
      <span
        className={cn(
          "truncate font-medium",
          cancelled && "text-muted-foreground line-through",
        )}
      >
        {event.title}
      </span>
      <span className="text-muted-foreground flex min-w-0 flex-wrap gap-x-2 text-xs">
        <time dateTime={event.startAt ?? event.startDate ?? undefined}>
          {label}
        </time>
        {event.location ? (
          <span className="truncate">{event.location}</span>
        ) : null}
        {cancelled ? <span>Cancelled</span> : null}
        {source ? <span>{source}</span> : null}
      </span>
    </Autocomplete.Item>
  );
}
