"use client";

import "./calendar-workspace.css";

import type {
  CalendarOptions,
  DateSelectArg,
  DatesSetArg,
  EventApi,
  EventClickArg,
  EventSourceFunc,
} from "@fullcalendar/core";
import enGbLocale from "@fullcalendar/core/locales/en-gb";
import dayGridPlugin from "@fullcalendar/daygrid";
import interactionPlugin from "@fullcalendar/interaction";
import listPlugin from "@fullcalendar/list";
import FullCalendar from "@fullcalendar/react";
import timeGridPlugin from "@fullcalendar/timegrid";
import { Bell, ChevronLeft, ChevronRight, Globe, Plus } from "lucide-react";
import {
  memo,
  type RefObject,
  useCallback,
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";

import {
  CalendarEventDialog,
  type EventDialogState,
} from "@/components/calendar-event-dialog";
import { GoogleCalendarControl } from "@/components/calendar-google";
import { ReminderDefaultsDialog } from "@/components/calendar-reminder-defaults";
import {
  editQuestion,
  ScopeDialog,
  type ScopeQuestion,
} from "@/components/calendar-scope-dialog";
import { CalendarSearch } from "@/components/calendar-search";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { calendarClient } from "@/lib/api/calendar-client";
import {
  apiErrorMessage,
  CALENDAR_VIEWS,
  type CalendarEvent,
  type CalendarView,
  type EventFields,
  eventStartDay,
  fetchEventsInRange,
  FULLCALENDAR_VIEW,
  isImported,
  localDay,
  movedFields,
  newDraft,
  occurrenceEdit,
  rescheduleRequest,
  saveOccurrence,
  type Scope,
  toEventInput,
  VIEW_LABEL,
  viewOf,
} from "@/lib/calendar-workspace";
import { browserTimeZone } from "@/lib/time-zone";

const PLUGINS = [dayGridPlugin, timeGridPlugin, listPlugin, interactionPlugin];

const TIME_FORMAT = {
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
} as const;

const VIEWS: CalendarOptions["views"] = {
  dayGridMonth: { dayHeaderFormat: { weekday: "short" } },
  timeGridWeek: { dayHeaderFormat: { weekday: "short", day: "numeric" } },
  timeGridDay: {
    dayHeaderFormat: { weekday: "long", day: "numeric", month: "long" },
  },
  // The agenda: 30 days from the date in focus, like a phone calendar's list.
  listAgenda: {
    type: "list",
    duration: { days: 30 },
    listDayFormat: { weekday: "long", day: "numeric", month: "long" },
    listDaySideFormat: false,
  },
};

/** Phones open the list; wider screens the month. */
const PHONE = "(max-width: 639px)";

// Week and day scroll inside a screen-high grid that opens at 08:00; month
// and list grow with their content. FullCalendar's height is a calendar-wide
// option, so it follows the view.
const TIME_GRID_HEIGHT = "max(30rem, calc(100dvh - 15rem))";
const SCROLL_TIME = "08:00:00";
const isTimeGrid = (view: string) => view.startsWith("timeGrid");

const noSubscription = () => () => {};

type Mover = { event: EventApi; revert: () => void };

/** An occurrence dragged or resized, waiting for the owner to pick a scope. */
type PendingMove = {
  revert: () => void;
  occurrence: CalendarEvent;
  fields: EventFields;
  question: ScopeQuestion;
};

/**
 * FullCalendar with stable options. It renders only in the browser (its
 * first render needs the screen width), so server and hydrated HTML agree.
 */
const FullCalendarView = memo(function FullCalendarView({
  calendarRef,
  initialDate,
  initialView,
  loadEvents,
  onDatesSet,
  onEventClick,
  onEventMoved,
  onLoading,
  onSelect,
}: {
  calendarRef: RefObject<FullCalendar | null>;
  initialDate?: string;
  initialView?: CalendarView;
  loadEvents: EventSourceFunc;
  onDatesSet: (arg: DatesSetArg) => void;
  onEventClick: (arg: EventClickArg) => void;
  onEventMoved: (arg: Mover) => void;
  onLoading: (loading: boolean) => void;
  onSelect: (arg: DateSelectArg) => void;
}) {
  const [view] = useState(
    () => initialView ?? (window.matchMedia(PHONE).matches ? "list" : "month"),
  );
  const [timeGrid, setTimeGrid] = useState(isTimeGrid(FULLCALENDAR_VIEW[view]));
  const datesSet = useCallback(
    (arg: DatesSetArg) => {
      setTimeGrid(isTimeGrid(arg.view.type));
      onDatesSet(arg);
    },
    [onDatesSet],
  );
  // A grid that just got its height starts at midnight; open it at 08:00.
  useEffect(() => {
    if (timeGrid) calendarRef.current?.getApi().scrollToTime(SCROLL_TIME);
  }, [calendarRef, timeGrid]);
  return (
    <FullCalendar
      allDayText="All day"
      dayMaxEvents={3}
      datesSet={datesSet}
      editable
      eventClick={onEventClick}
      eventDrop={onEventMoved}
      eventResize={onEventMoved}
      eventTimeFormat={TIME_FORMAT}
      events={loadEvents}
      fixedWeekCount={false}
      headerToolbar={false}
      height={timeGrid ? TIME_GRID_HEIGHT : "auto"}
      initialDate={initialDate}
      initialView={FULLCALENDAR_VIEW[view]}
      loading={onLoading}
      locale={enGbLocale}
      navLinkDayClick="timeGridDay"
      navLinks
      noEventsText="Nothing on the calendar for these 30 days."
      nowIndicator
      plugins={PLUGINS}
      ref={calendarRef}
      scrollTime={SCROLL_TIME}
      select={onSelect}
      selectMirror
      selectable
      slotLabelFormat={TIME_FORMAT}
      timeZone="local"
      views={VIEWS}
    />
  );
});

/**
 * The Calendar workspace: month, week, day, and a 30-day list (FullCalendar
 * Standard) over the Calendar API, with search, event details, create,
 * edit, and delete dialogs, and Reminder defaults. Times are shown in the
 * browser's timezone (FullCalendar's "local"); `timeZone` is the zone the
 * server rendered in, used until the page has hydrated. The view and date
 * in focus are kept in the URL (`?view=week&date=2026-10-14`) once the
 * owner moves, so a reload returns to them.
 */
export function CalendarWorkspace({
  initialDate,
  initialView,
  timeZone,
  today,
}: {
  initialDate?: string;
  initialView?: CalendarView;
  timeZone: string;
  /** The owner's today (YYYY-MM-DD), decided by the server render. */
  today: string;
}) {
  const hydrated = useSyncExternalStore(
    noSubscription,
    () => true,
    () => false,
  );
  // FullCalendar's "local" zone is the browser's, so labels use it too.
  const zone = useSyncExternalStore(
    noSubscription,
    () => browserTimeZone() ?? timeZone,
    () => timeZone,
  );
  const calendarRef = useRef<FullCalendar>(null);
  const zoneRef = useRef(zone);
  const cancelledRef = useRef(false);
  const settled = useRef(false);
  const [view, setView] = useState<CalendarView>();
  const [title, setTitle] = useState("");
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState(false);
  const [notice, setNotice] = useState<string>();
  const [showCancelled, setShowCancelled] = useState(false);
  const [dialog, setDialog] = useState<EventDialogState>(null);
  const [defaultsOpen, setDefaultsOpen] = useState(false);
  const [moving, setMoving] = useState<PendingMove | null>(null);

  useEffect(() => {
    zoneRef.current = zone;
  }, [zone]);

  const api = () => calendarRef.current?.getApi();

  const loadEvents = useCallback<EventSourceFunc>((info, success, failure) => {
    fetchEventsInRange({
      from: info.start,
      to: info.end,
      timeZone: zoneRef.current,
      includeCancelled: cancelledRef.current,
    })
      .then((items) => {
        setFailed(false);
        success(items.flatMap((event) => toEventInput(event) ?? []));
      })
      .catch((error: unknown) => {
        setFailed(true);
        failure(error instanceof Error ? error : new Error(String(error)));
      });
  }, []);

  const onDatesSet = useCallback((arg: DatesSetArg) => {
    const next = viewOf(arg.view.type);
    setView(next);
    setTitle(arg.view.title);
    // The first render keeps the URL as it was opened; moving records it.
    if (settled.current && next) {
      const date = localDay(arg.view.calendar.getDate());
      window.history.replaceState(
        null,
        "",
        `${window.location.pathname}?view=${next}&date=${date}`,
      );
    }
    settled.current = true;
  }, []);

  const onEventClick = useCallback((arg: EventClickArg) => {
    arg.jsEvent.preventDefault();
    const event = arg.event.extendedProps.event as CalendarEvent | undefined;
    if (event) setDialog({ mode: "details", event });
  }, []);

  const onSelect = useCallback((arg: DateSelectArg) => {
    arg.view.calendar.unselect();
    setDialog({
      mode: "create",
      draft: newDraft(zoneRef.current, new Date(), arg),
    });
  }, []);

  const onEventMoved = useCallback(async ({ event, revert }: Mover) => {
    const original = event.extendedProps.event as CalendarEvent | undefined;
    if (!original || !event.start) return revert();
    // An occurrence of a recurring event: ask which occurrences move.
    if (original.occurrence && !isImported(original)) {
      const fields = movedFields(
        { allDay: event.allDay, start: event.start, end: event.end },
        original,
      );
      const edit = occurrenceEdit(fields, original, false);
      if (!edit.changed) return revert();
      setMoving({
        revert,
        occurrence: original,
        fields,
        question: editQuestion(
          original,
          edit,
          zoneRef.current,
          localDay(new Date()),
        ),
      });
      return;
    }
    try {
      const { data, error } = await calendarClient.PATCH(
        "/v1/events/{eventId}",
        {
          params: { path: { eventId: original.id } },
          body: rescheduleRequest(
            { allDay: event.allDay, start: event.start, end: event.end },
            original,
          ),
        },
      );
      if (!data) {
        revert();
        setNotice(
          `Could not move “${original.title}”: ${apiErrorMessage(error) ?? "Calendar refused the change"}.`,
        );
        return;
      }
      event.setExtendedProp("event", data);
      setNotice(undefined);
    } catch {
      revert();
      setNotice("Calendar is unavailable right now; the event was not moved.");
    }
  }, []);

  // Every scope can change other occurrences too (All events moves them
  // all), so the calendar reloads; a refused move snaps back.
  const moveTo = async (scope: Scope, move: PendingMove) => {
    const { occurrence, fields, revert } = move;
    try {
      const { data, error } = await saveOccurrence(scope, fields, occurrence, {
        repeatChanged: false,
        ownerZone: zone,
      });
      if (!data) {
        revert();
        setNotice(
          `Could not move “${occurrence.title}”: ${apiErrorMessage(error) ?? "Calendar refused the change"}.`,
        );
        return;
      }
      setNotice(undefined);
      api()?.refetchEvents();
    } catch {
      revert();
      setNotice("Calendar is unavailable right now; the event was not moved.");
    }
  };

  /** Shows the event's day (and time) unless it is already in view. */
  const reveal = (event: CalendarEvent, always = false) => {
    const calendar = api();
    const day = eventStartDay(event, zone);
    if (!calendar || !day) return;
    const midnight = new Date(`${day}T00:00:00`);
    const { activeStart, activeEnd } = calendar.view;
    if (always || midnight < activeStart || midnight >= activeEnd) {
      calendar.gotoDate(day);
    }
    if (!event.allDay && event.startAt) {
      const start = new Date(event.startAt);
      calendar.scrollToTime(
        `${String(Math.max(0, start.getHours() - 1)).padStart(2, "0")}:00:00`,
      );
    }
  };

  const onSaved = (event: CalendarEvent, created: boolean) => {
    reveal(event);
    api()?.refetchEvents();
    setDialog(created ? null : { mode: "details", event });
  };

  const onDeleted = () => {
    setDialog(null);
    api()?.refetchEvents();
  };

  // This and following, or All events: several occurrences changed.
  const onSeriesChanged = onDeleted;

  const openFromSearch = (event: CalendarEvent) => {
    reveal(event, true);
    setDialog({ mode: "details", event });
  };

  const toggleCancelled = (checked: boolean) => {
    setShowCancelled(checked);
    cancelledRef.current = checked;
    api()?.refetchEvents();
  };

  // Events that use their defaults now remind differently.
  const onDefaultsSaved = () => {
    setDefaultsOpen(false);
    api()?.refetchEvents();
  };

  const createNow = () =>
    setDialog({ mode: "create", draft: newDraft(zone, new Date()) });

  const unit = view === "list" ? "30 days" : view;

  return (
    <div
      className="mx-auto max-w-6xl space-y-3"
      data-time-zone={zone}
      data-view={view}
      id="calendar"
    >
      <header className="flex flex-wrap items-center gap-2">
        <h1 className="mr-auto text-xl font-semibold tracking-tight">
          Calendar
        </h1>
        <CalendarSearch
          className="order-last w-full sm:order-none sm:w-64 lg:w-80"
          includeCancelled={showCancelled}
          onChoose={openFromSearch}
          timeZone={zone}
          today={today}
        />
        <Button onClick={createNow}>
          <Plus />
          New event
        </Button>
      </header>

      <div
        aria-label="Calendar navigation"
        className="flex flex-wrap items-center gap-x-3 gap-y-2"
        role="toolbar"
      >
        <div className="flex min-w-0 items-center gap-1">
          <Button
            disabled={!view}
            onClick={() => api()?.today()}
            size="sm"
            variant="outline"
          >
            Today
          </Button>
          <Button
            aria-label={view ? `Previous ${unit}` : "Previous"}
            disabled={!view}
            onClick={() => api()?.prev()}
            size="icon-sm"
            variant="ghost"
          >
            <ChevronLeft />
          </Button>
          <Button
            aria-label={view ? `Next ${unit}` : "Next"}
            disabled={!view}
            onClick={() => api()?.next()}
            size="icon-sm"
            variant="ghost"
          >
            <ChevronRight />
          </Button>
          <h2
            aria-live="polite"
            className="ml-1 truncate text-sm font-medium"
            data-testid="calendar-title"
          >
            {title}
          </h2>
        </div>
        <div className="ml-auto flex flex-wrap items-center gap-x-3 gap-y-2">
          <Button
            onClick={() => setDefaultsOpen(true)}
            size="sm"
            variant="outline"
          >
            <Bell />
            Reminder defaults
          </Button>
          <GoogleCalendarControl timeZone={zone} />
          <span
            className="text-muted-foreground hidden items-center gap-1 text-xs lg:flex"
            title="Times are shown in this time zone"
          >
            <Globe aria-hidden="true" className="size-3.5" />
            {zone}
          </span>
          <Label className="text-muted-foreground gap-1.5 text-xs font-normal">
            <Checkbox
              checked={showCancelled}
              onCheckedChange={toggleCancelled}
            />
            Show cancelled
          </Label>
          <ToggleGroup
            aria-label="View"
            disabled={!view}
            onValueChange={(chosen) => {
              // Pressing the current view again would leave none pressed.
              const next = CALENDAR_VIEWS.find(
                (option) => option === chosen[0],
              );
              if (next) api()?.changeView(FULLCALENDAR_VIEW[next]);
            }}
            size="sm"
            spacing={0}
            value={view ? [view] : []}
            variant="outline"
          >
            {CALENDAR_VIEWS.map((option) => (
              <ToggleGroupItem key={option} value={option}>
                {VIEW_LABEL[option]}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
        </div>
      </div>

      {failed ? (
        <div
          className="bg-destructive-subtle border-destructive/30 text-destructive flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2 text-sm"
          role="alert"
        >
          Calendar is unavailable right now.
          <Button
            onClick={() => api()?.refetchEvents()}
            size="xs"
            variant="outline"
          >
            Try again
          </Button>
        </div>
      ) : null}
      {notice ? (
        <p
          className="bg-warning-subtle border-warning/30 text-warning rounded-lg border px-3 py-2 text-sm"
          role="alert"
        >
          {notice}
        </p>
      ) : null}

      <section
        aria-busy={loading || !hydrated}
        aria-label="Calendar events"
        className="lifeos-calendar bg-card overflow-clip rounded-xl border"
      >
        {hydrated ? (
          <FullCalendarView
            calendarRef={calendarRef}
            initialDate={initialDate}
            initialView={initialView}
            loadEvents={loadEvents}
            onDatesSet={onDatesSet}
            onEventClick={onEventClick}
            onEventMoved={onEventMoved}
            onLoading={setLoading}
            onSelect={onSelect}
          />
        ) : (
          <p className="text-muted-foreground min-h-96 p-4 text-sm">
            Loading your calendar…
          </p>
        )}
      </section>

      <CalendarEventDialog
        onDeleted={onDeleted}
        onSaved={onSaved}
        onSeriesChanged={onSeriesChanged}
        onStateChange={setDialog}
        state={dialog}
        timeZone={zone}
        today={today}
      />
      <ReminderDefaultsDialog
        onOpenChange={setDefaultsOpen}
        onSaved={onDefaultsSaved}
        open={defaultsOpen}
      />
      <ScopeDialog
        onCancel={() => {
          moving?.revert();
          setMoving(null);
        }}
        onChoose={(scope) => {
          if (moving) void moveTo(scope, moving);
          setMoving(null);
        }}
        question={moving?.question ?? null}
      />
    </div>
  );
}
