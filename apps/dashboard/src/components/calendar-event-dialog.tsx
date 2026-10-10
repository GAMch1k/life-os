"use client";

import {
  Bell,
  Clock,
  MapPin,
  NotebookText,
  Plus,
  Repeat,
  X,
} from "lucide-react";
import {
  type FormEvent,
  type ReactNode,
  useEffect,
  useId,
  useState,
  useTransition,
} from "react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { calendarClient } from "@/lib/api/calendar-client";
import {
  apiErrorMessage,
  type CalendarEvent,
  createRequest,
  defaultRemindersNote,
  draftFromEvent,
  editingZone,
  type EventDraft,
  eventWhenLabel,
  fetchOwnerDefaults,
  type FieldErrors,
  fieldErrorsFromApi,
  formDefaults,
  isCancelled,
  isImported,
  MAX_REMINDERS,
  momentLabel,
  moveStart,
  readDraft,
  readOnlyReason,
  recurrenceLabel,
  reminderChanges,
  type ReminderDefaults,
  type ReminderDraft,
  reminderLines,
  sourceLabel,
  updateRequest,
} from "@/lib/calendar-workspace";
import { cn } from "@/lib/utils";

/** What the event dialog shows; null when it is closed. */
export type EventDialogState =
  | { mode: "details"; event: CalendarEvent }
  | { mode: "edit"; event: CalendarEvent; remindersOnly: boolean }
  | { mode: "create"; draft: EventDraft }
  | null;

type Shared = {
  /** The owner's timezone, which times are shown and edited in. */
  timeZone: string;
  /** The owner's today (YYYY-MM-DD). */
  today: string;
};

export const UNAVAILABLE = "Calendar is unavailable right now. Try again.";

export function CalendarEventDialog({
  state,
  onStateChange,
  onSaved,
  onDeleted,
  ...shared
}: Shared & {
  state: EventDialogState;
  onStateChange: (state: EventDialogState) => void;
  /** After a create or update, with the event as Calendar stored it. */
  onSaved: (event: CalendarEvent, created: boolean) => void;
  onDeleted: (id: string) => void;
}) {
  // The last state stays rendered while the dialog animates closed.
  const [shown, setShown] = useState(state);
  if (state && state !== shown) setShown(state);
  const current = state ?? shown;

  return (
    <Dialog
      open={state !== null}
      onOpenChange={(open) => {
        if (!open) onStateChange(null);
      }}
    >
      <DialogContent
        className={current?.mode === "details" ? undefined : "sm:max-w-lg"}
      >
        {current?.mode === "details" ? (
          <EventDetails
            event={current.event}
            key={`details-${current.event.id}-${current.event.updatedAt}`}
            onDeleted={onDeleted}
            onEdit={(event, remindersOnly) =>
              onStateChange({ mode: "edit", event, remindersOnly })
            }
            onSaved={(event) => onSaved(event, false)}
            {...shared}
          />
        ) : current?.mode === "edit" ? (
          <EventForm
            event={current.event}
            initial={draftFromEvent(current.event, shared.timeZone)}
            key={`edit-${current.event.id}`}
            onCancel={() =>
              onStateChange({ mode: "details", event: current.event })
            }
            onSaved={(event) => onSaved(event, false)}
            remindersOnly={current.remindersOnly}
            {...shared}
          />
        ) : current?.mode === "create" ? (
          <EventForm
            initial={current.draft}
            key="create"
            onCancel={() => onStateChange(null)}
            onSaved={(event) => onSaved(event, true)}
            {...shared}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function Badge({
  children,
  tone,
}: {
  children: ReactNode;
  tone: "destructive" | "info" | "muted";
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-md border px-1.5 py-0.5 text-xs font-medium",
        tone === "destructive" &&
          "bg-destructive-subtle border-destructive/30 text-destructive",
        tone === "info" && "bg-info-subtle border-info/30 text-info",
        tone === "muted" && "bg-muted text-muted-foreground",
      )}
    >
      {children}
    </span>
  );
}

function DetailRow({
  children,
  icon: Icon,
  label,
}: {
  children: ReactNode;
  icon: typeof Clock;
  label: string;
}) {
  return (
    <div className="grid grid-cols-[1rem_minmax(0,1fr)] gap-x-3">
      <Icon
        aria-hidden="true"
        className="text-muted-foreground mt-0.5 size-4"
      />
      <dt className="sr-only">{label}</dt>
      <dd className="min-w-0 space-y-0.5">{children}</dd>
    </div>
  );
}

function EventDetails({
  event,
  onDeleted,
  onEdit,
  onSaved,
  timeZone,
  today,
}: Shared & {
  event: CalendarEvent;
  onDeleted: (id: string) => void;
  onEdit: (event: CalendarEvent, remindersOnly: boolean) => void;
  onSaved: (event: CalendarEvent) => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState<string>();
  const [pending, startTransition] = useTransition();
  const reasonId = useId();
  const imported = isImported(event);
  const cancelled = isCancelled(event);
  const source = sourceLabel(event);
  const lines = reminderLines(event);
  const hint =
    !event.allDay && event.timeZone && event.timeZone !== timeZone
      ? event.timeZone
      : undefined;

  const run = (action: () => Promise<void>) =>
    startTransition(async () => {
      setError(undefined);
      try {
        await action();
      } catch {
        setError(UNAVAILABLE);
      }
    });

  const remove = () =>
    run(async () => {
      const { error: failure, response } = await calendarClient.DELETE(
        "/v1/events/{eventId}",
        { params: { path: { eventId: event.id } } },
      );
      // Already gone is as good as deleted.
      if (failure && response.status !== 404) {
        setError(apiErrorMessage(failure) ?? "Could not delete this event.");
        return;
      }
      onDeleted(event.id);
    });

  const restore = () =>
    run(async () => {
      const { data, error: failure } = await calendarClient.PATCH(
        "/v1/events/{eventId}",
        { params: { path: { eventId: event.id } }, body: { status: "active" } },
      );
      if (failure || !data) {
        setError(apiErrorMessage(failure) ?? "Could not restore this event.");
        return;
      }
      onSaved(data);
    });

  // A recurring event is edited as the stored series, not the occurrence
  // that was clicked.
  const edit = (remindersOnly: boolean) =>
    run(async () => {
      if (!event.recurrence) return onEdit(event, remindersOnly);
      const { data } = await calendarClient.GET("/v1/events/{eventId}", {
        params: { path: { eventId: event.id } },
      });
      onEdit(data ?? event, remindersOnly);
    });

  return (
    <>
      <DialogHeader>
        <DialogTitle
          className={cn(
            "break-words",
            cancelled && "text-muted-foreground line-through",
          )}
        >
          {event.title}
        </DialogTitle>
        {cancelled || source || event.recurrence ? (
          <div className="flex flex-wrap gap-1.5">
            {cancelled ? <Badge tone="destructive">Cancelled</Badge> : null}
            {source ? <Badge tone="info">Imported · {source}</Badge> : null}
            {event.recurrence ? (
              <Badge tone="muted">
                <Repeat aria-hidden="true" className="mr-1 size-3" />
                {recurrenceLabel(event.recurrence, timeZone)}
              </Badge>
            ) : null}
          </div>
        ) : null}
      </DialogHeader>

      <dl className="grid gap-3">
        <DetailRow icon={Clock} label="When">
          <p data-testid="event-when">
            {eventWhenLabel(event, timeZone, today)}
          </p>
          {event.allDay ? null : (
            <p className="text-muted-foreground text-xs">
              Times in {timeZone}
              {hint
                ? ` · scheduled in ${hint}: ${eventWhenLabel(event, hint, today)}`
                : ""}
            </p>
          )}
        </DetailRow>
        {event.location ? (
          <DetailRow icon={MapPin} label="Location">
            <p className="break-words">{event.location}</p>
          </DetailRow>
        ) : null}
        {event.description ? (
          <DetailRow icon={NotebookText} label="Notes">
            <p className="break-words whitespace-pre-wrap">
              {event.description}
            </p>
          </DetailRow>
        ) : null}
        <DetailRow icon={Bell} label="Reminders">
          <ul className="space-y-0.5" data-testid="event-reminders">
            {lines.length ? (
              lines.map((line, index) => (
                <li key={index}>
                  {line.label}
                  {line.origin ? (
                    <span className="text-muted-foreground">
                      {" "}
                      · {line.origin}
                    </span>
                  ) : null}
                </li>
              ))
            ) : (
              <li>No reminders</li>
            )}
          </ul>
        </DetailRow>
      </dl>

      {cancelled && event.removedFromSourceAt ? (
        <p className="text-muted-foreground text-xs">
          Removed from its source on{" "}
          {momentLabel(event.removedFromSourceAt, timeZone)}.
        </p>
      ) : cancelled && event.cancelledAt ? (
        <p className="text-muted-foreground text-xs">
          Cancelled on {momentLabel(event.cancelledAt, timeZone)}.
        </p>
      ) : null}
      {imported ? (
        <p className="text-muted-foreground text-xs" id={reasonId}>
          {readOnlyReason(event)}
        </p>
      ) : null}
      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}

      <DialogFooter>
        {confirming ? (
          <>
            <Button
              disabled={pending}
              onClick={() => setConfirming(false)}
              variant="outline"
            >
              Keep it
            </Button>
            <Button disabled={pending} onClick={remove} variant="destructive">
              {pending ? "Deleting…" : "Delete event"}
            </Button>
            {/* Above the buttons on a phone, beside them on wider screens. */}
            <p className="text-sm sm:order-first sm:mr-auto sm:self-center">
              Delete this event permanently?
            </p>
          </>
        ) : (
          <>
            <Button
              aria-describedby={imported ? reasonId : undefined}
              className="sm:mr-auto"
              disabled={pending || imported}
              onClick={() => setConfirming(true)}
              variant="destructive"
            >
              Delete
            </Button>
            {cancelled && !imported ? (
              <Button disabled={pending} onClick={restore} variant="outline">
                Restore
              </Button>
            ) : null}
            {imported && !cancelled ? (
              <Button
                disabled={pending}
                onClick={() => edit(true)}
                variant="outline"
              >
                Edit reminders
              </Button>
            ) : null}
            <Button
              aria-describedby={imported ? reasonId : undefined}
              disabled={pending || imported}
              onClick={() => edit(false)}
            >
              Edit
            </Button>
          </>
        )}
      </DialogFooter>
    </>
  );
}

const SELECT =
  "border-input focus-visible:border-ring focus-visible:ring-ring/50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:bg-input/30 dark:aria-invalid:ring-destructive/40 h-8 min-w-0 rounded-lg border bg-transparent px-2 text-base transition-colors outline-none focus-visible:ring-3 aria-invalid:ring-3 md:text-sm";

export const LABEL = "text-sm font-medium";

let zoneList: string[] | undefined;

/** Every IANA zone the browser knows, for the time zone picker. */
function timeZones() {
  if (!zoneList) {
    let zones: string[] = [];
    try {
      zones = Intl.supportedValuesOf("timeZone");
    } catch {
      // Older browsers: only the owner's zone and the event's own are offered.
    }
    zoneList = zones.includes("UTC") ? zones : [...zones, "UTC"];
  }
  return zoneList;
}

export function FieldError({ id, message }: { id: string; message?: string }) {
  return message ? (
    <p className="text-destructive text-xs" id={id}>
      {message}
    </p>
  ) : null;
}

function EventForm({
  event,
  initial,
  onCancel,
  onSaved,
  remindersOnly = false,
  timeZone,
}: Shared & {
  /** The event being edited; a new event when absent. */
  event?: CalendarEvent;
  initial: EventDraft;
  onCancel: () => void;
  onSaved: (event: CalendarEvent) => void;
  /** Imported events: only their reminders can change. */
  remindersOnly?: boolean;
}) {
  const [draft, setDraft] = useState(initial);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [pending, startTransition] = useTransition();
  const id = useId();
  // A Life OS event's defaults are the owner's (Reminder defaults), read
  // when the form opens; an imported event's are its source's.
  const ownDefaults = !event || !isImported(event);
  const [owner, setOwner] = useState<
    ReminderDefaults | "loading" | "unavailable"
  >("loading");
  useEffect(() => {
    if (!ownDefaults) return;
    let current = true;
    fetchOwnerDefaults()
      .then((defaults) => {
        if (current) setOwner(defaults);
      })
      .catch(() => {
        if (current) setOwner("unavailable");
      });
    return () => {
      current = false;
    };
  }, [ownDefaults]);
  const update = (patch: Partial<EventDraft>) =>
    setDraft((current) => ({ ...current, ...patch }));
  const invalid = (field: keyof FieldErrors) =>
    errors[field]
      ? { "aria-invalid": true, "aria-describedby": `${id}-${field}-error` }
      : {};

  const submit = (formEvent: FormEvent<HTMLFormElement>) => {
    formEvent.preventDefault();
    const read = readDraft(draft, timeZone);
    if ("errors" in read) {
      setErrors(read.errors);
      return;
    }
    setErrors({});
    // An imported event sends only its reminder changes; with none there is
    // nothing to save (Calendar refuses an empty PATCH).
    const body = event
      ? remindersOnly
        ? reminderChanges(read.fields, event)
        : updateRequest(read.fields, event)
      : undefined;
    if (event && body && Object.keys(body).length === 0) return onCancel();
    startTransition(async () => {
      try {
        const result =
          event && body
            ? await calendarClient.PATCH("/v1/events/{eventId}", {
                params: { path: { eventId: event.id } },
                body,
              })
            : await calendarClient.POST("/v1/events", {
                body: createRequest(read.fields),
              });
        if (result.data) return onSaved(result.data);
        const message = apiErrorMessage(result.error);
        const status = result.response.status;
        setErrors(
          status === 400 && message
            ? fieldErrorsFromApi(message)
            : status === 404
              ? { form: "This event no longer exists." }
              : { form: message ?? "Could not save this event." },
        );
      } catch {
        setErrors({ form: UNAVAILABLE });
      }
    });
  };

  const zones = timeZones();
  const zoneOptions = zones.includes(draft.timeZone)
    ? zones
    : [draft.timeZone, ...zones].filter(Boolean);
  const editedZone = editingZone(draft.timeZone, timeZone);

  return (
    <form className="grid gap-4" noValidate onSubmit={submit}>
      <DialogHeader>
        <DialogTitle>
          {!event
            ? "New event"
            : remindersOnly
              ? "Edit reminders"
              : "Edit event"}
        </DialogTitle>
        {remindersOnly && event ? (
          <DialogDescription className="break-words">
            {event.title}
          </DialogDescription>
        ) : null}
      </DialogHeader>

      {remindersOnly ? null : (
        <>
          <div className="grid gap-1.5">
            <label className={LABEL} htmlFor={`${id}-title`}>
              Title
            </label>
            <Input
              autoFocus={!event}
              id={`${id}-title`}
              onChange={(change) => update({ title: change.target.value })}
              value={draft.title}
              {...invalid("title")}
            />
            <FieldError id={`${id}-title-error`} message={errors.title} />
          </div>

          <label className="flex items-center gap-2 text-sm">
            <input
              checked={draft.allDay}
              className="accent-primary size-4"
              onChange={(change) => update({ allDay: change.target.checked })}
              type="checkbox"
            />
            All day
          </label>

          <div className="grid gap-3">
            <fieldset className="grid min-w-0 gap-1.5">
              <legend className={cn(LABEL, "mb-1.5")}>Start</legend>
              <div className="flex flex-wrap gap-2">
                <Input
                  aria-label="Start date"
                  className="min-w-0 flex-1 basis-36"
                  onChange={(change) =>
                    setDraft((current) =>
                      moveStart(current, {
                        startDate: change.target.value,
                        startTime: current.startTime,
                      }),
                    )
                  }
                  type="date"
                  value={draft.startDate}
                  {...invalid("start")}
                />
                {draft.allDay ? null : (
                  <Input
                    aria-label="Start time"
                    className="w-30 shrink-0"
                    onChange={(change) =>
                      setDraft((current) =>
                        moveStart(current, {
                          startDate: current.startDate,
                          startTime: change.target.value,
                        }),
                      )
                    }
                    type="time"
                    value={draft.startTime}
                  />
                )}
              </div>
              <FieldError id={`${id}-start-error`} message={errors.start} />
            </fieldset>
            <fieldset className="grid min-w-0 gap-1.5">
              <legend className={cn(LABEL, "mb-1.5")}>
                {draft.allDay ? "Last day" : "End"}
              </legend>
              <div className="flex flex-wrap gap-2">
                <Input
                  aria-label="End date"
                  className="min-w-0 flex-1 basis-36"
                  onChange={(change) =>
                    update({ endDate: change.target.value })
                  }
                  type="date"
                  value={draft.endDate}
                  {...invalid("end")}
                />
                {draft.allDay ? null : (
                  <Input
                    aria-label="End time"
                    className="w-30 shrink-0"
                    onChange={(change) =>
                      update({ endTime: change.target.value })
                    }
                    type="time"
                    value={draft.endTime}
                    {...invalid("end")}
                  />
                )}
              </div>
              <FieldError id={`${id}-end-error`} message={errors.end} />
            </fieldset>
          </div>

          {draft.allDay ? null : (
            <div className="grid gap-1.5">
              <label className={LABEL} htmlFor={`${id}-zone`}>
                Time zone
              </label>
              <select
                className={cn(SELECT, "w-full")}
                id={`${id}-zone`}
                onChange={(change) => update({ timeZone: change.target.value })}
                value={draft.timeZone}
                {...invalid("timeZone")}
              >
                <option value="">Your time zone ({timeZone})</option>
                {zoneOptions
                  .filter(
                    (zone) => zone !== timeZone || zone === draft.timeZone,
                  )
                  .map((zone) => (
                    <option key={zone} value={zone}>
                      {zone.replaceAll("_", " ")}
                    </option>
                  ))}
              </select>
              {draft.timeZone ? (
                <p className="text-muted-foreground text-xs">
                  Start and end are in {editedZone}.
                </p>
              ) : null}
              <FieldError
                id={`${id}-timeZone-error`}
                message={errors.timeZone}
              />
            </div>
          )}

          <div className="grid gap-1.5">
            <label className={LABEL} htmlFor={`${id}-location`}>
              Location
            </label>
            <Input
              id={`${id}-location`}
              onChange={(change) => update({ location: change.target.value })}
              value={draft.location}
            />
          </div>

          <div className="grid gap-1.5">
            <label className={LABEL} htmlFor={`${id}-notes`}>
              Notes
            </label>
            <Textarea
              id={`${id}-notes`}
              onChange={(change) => update({ notes: change.target.value })}
              value={draft.notes}
            />
          </div>
        </>
      )}

      <fieldset className="grid min-w-0 gap-3">
        <legend className={cn(LABEL, "mb-2")}>Reminders</legend>
        <div className="grid gap-1">
          <label className="flex items-center gap-2 text-sm">
            <input
              aria-describedby={`${id}-defaults`}
              checked={draft.useDefaultReminders}
              className="accent-primary size-4"
              onChange={(change) =>
                update({ useDefaultReminders: change.target.checked })
              }
              type="checkbox"
            />
            Use default reminders
          </label>
          <p
            className="text-muted-foreground pl-6 text-xs"
            data-testid="default-reminders"
            id={`${id}-defaults`}
          >
            {defaultRemindersNote(formDefaults(event, draft.allDay, owner), {
              allDay: draft.allDay,
              source: event ? sourceLabel(event) : undefined,
            })}
          </p>
        </div>
        <div
          aria-labelledby={`${id}-own`}
          className="grid min-w-0 gap-2"
          role="group"
        >
          <p
            className="text-muted-foreground text-xs font-medium"
            id={`${id}-own`}
          >
            Your reminders
          </p>
          <ReminderList
            error={errors.reminders}
            errorId={`${id}-reminders-error`}
            fresh={draft.allDay ? NEW_LOCAL_TIME : NEW_RELATIVE}
            onChange={(reminders) => update({ reminders })}
            reminders={draft.reminders}
          />
        </div>
      </fieldset>

      {errors.form ? (
        <p className="text-destructive text-sm" role="alert">
          {errors.form}
        </p>
      ) : null}

      <DialogFooter>
        <Button
          disabled={pending}
          onClick={onCancel}
          type="button"
          variant="outline"
        >
          Cancel
        </Button>
        <Button disabled={pending} type="submit">
          {pending ? "Saving…" : "Save"}
        </Button>
      </DialogFooter>
    </form>
  );
}

export const NEW_RELATIVE: ReminderDraft = {
  kind: "relative",
  amount: "30",
  unit: "minutes",
};
// The evening before, as for a waste pickup or an all-day event.
export const NEW_LOCAL_TIME: ReminderDraft = {
  kind: "localTime",
  daysBefore: "1",
  time: "19:00",
};

/**
 * A list of reminders, each row minutes, hours, or days before the start,
 * or N days before at a time, with Add reminder (up to ten). Every row is
 * labelled `${label} N`, such as "Reminder 1 amount".
 */
export function ReminderList({
  empty,
  error,
  errorId,
  fresh = NEW_RELATIVE,
  label = "Reminder",
  onChange,
  reminders,
}: {
  /** Shown while the list is empty. */
  empty?: string;
  error?: string;
  errorId: string;
  /** What Add reminder adds. */
  fresh?: ReminderDraft;
  label?: string;
  onChange: (reminders: ReminderDraft[]) => void;
  reminders: ReminderDraft[];
}) {
  const set = (index: number, reminder: ReminderDraft) =>
    onChange(
      reminders.map((current, at) => (at === index ? reminder : current)),
    );
  return (
    <div className="grid min-w-0 gap-2">
      {reminders.length ? (
        <ul className="grid gap-2">
          {reminders.map((reminder, index) => {
            const row = `${label} ${index + 1}`;
            // "Remove reminder 1", but "Remove ZAW default 1".
            const remove = /^[A-Z][a-z]/.test(row)
              ? row.charAt(0).toLowerCase() + row.slice(1)
              : row;
            return (
              <li
                className="flex flex-wrap items-center gap-2 text-sm"
                key={index}
              >
                <select
                  aria-describedby={error ? errorId : undefined}
                  aria-label={`${row} type`}
                  className={SELECT}
                  onChange={(change) =>
                    set(
                      index,
                      change.target.value === "localTime"
                        ? NEW_LOCAL_TIME
                        : NEW_RELATIVE,
                    )
                  }
                  value={reminder.kind}
                >
                  <option value="relative">Before the start</option>
                  <option value="localTime">Days before, at a time</option>
                </select>
                {reminder.kind === "relative" ? (
                  <>
                    <Input
                      aria-label={`${row} amount`}
                      className="w-20"
                      inputMode="numeric"
                      min={0}
                      onChange={(change) =>
                        set(index, {
                          ...reminder,
                          amount: change.target.value,
                        })
                      }
                      type="number"
                      value={reminder.amount}
                    />
                    <select
                      aria-label={`${row} unit`}
                      className={SELECT}
                      onChange={(change) =>
                        set(index, {
                          ...reminder,
                          unit: change.target.value as typeof reminder.unit,
                        })
                      }
                      value={reminder.unit}
                    >
                      <option value="minutes">minutes</option>
                      <option value="hours">hours</option>
                      <option value="days">days</option>
                    </select>
                    <span className="text-muted-foreground">before</span>
                  </>
                ) : (
                  <>
                    <Input
                      aria-label={`${row} days before`}
                      className="w-16"
                      inputMode="numeric"
                      min={0}
                      onChange={(change) =>
                        set(index, {
                          ...reminder,
                          daysBefore: change.target.value,
                        })
                      }
                      type="number"
                      value={reminder.daysBefore}
                    />
                    <span className="text-muted-foreground">
                      days before at
                    </span>
                    <Input
                      aria-label={`${row} time`}
                      className="w-32"
                      onChange={(change) =>
                        set(index, { ...reminder, time: change.target.value })
                      }
                      type="time"
                      value={reminder.time}
                    />
                  </>
                )}
                <Button
                  aria-label={`Remove ${remove}`}
                  onClick={() =>
                    onChange(reminders.filter((_, at) => at !== index))
                  }
                  size="icon-sm"
                  type="button"
                  variant="ghost"
                >
                  <X />
                </Button>
              </li>
            );
          })}
        </ul>
      ) : empty ? (
        <p className="text-muted-foreground text-xs">{empty}</p>
      ) : null}
      {reminders.length < MAX_REMINDERS ? (
        <Button
          className="justify-self-start"
          onClick={() => onChange([...reminders, fresh])}
          size="sm"
          type="button"
          variant="outline"
        >
          <Plus />
          Add reminder
        </Button>
      ) : null}
      <FieldError id={errorId} message={error} />
    </div>
  );
}
