"use client";

import {
  Bell,
  CalendarSync,
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

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import {
  deleteQuestion,
  editQuestion,
  ScopeDialog,
  type ScopeQuestion,
} from "@/components/calendar-scope-dialog";
import { DatePicker, TimeInput } from "@/components/date-picker";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Textarea } from "@/components/ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { calendarClient } from "@/lib/api/calendar-client";
import { googleCalendarClient } from "@/lib/api/google-calendar-client";
import {
  customDays,
  customFrom,
  type Frequency,
  readRepeat,
  recurrenceLabel,
  type RepeatDraft,
  repeatOptions,
  repeatsOnNote,
  unitLabel,
  WEEK,
  WEEKDAY_NAME,
  weekdayOf,
  sameRepeat,
} from "@/lib/calendar-recurrence";
import {
  apiErrorMessage,
  type CalendarEvent,
  createRequest,
  defaultPriorityNote,
  defaultRemindersNote,
  deleteOccurrence,
  draftFromEvent,
  editingZone,
  type EventDraft,
  type EventFields,
  eventRecurrenceLabel,
  eventWhenLabel,
  fetchOwnerDefaults,
  type FieldErrors,
  fieldErrorsFromApi,
  formDefaults,
  googleSyncLabel,
  isCancelled,
  isEditable,
  isImported,
  MAX_REMINDERS,
  momentLabel,
  moveStart,
  occurrenceEdit,
  occurrenceKey,
  PRIORITIES,
  type Priority,
  PRIORITY_LABEL,
  priorityBadge,
  readDraft,
  readOnlyReason,
  reminderAndPriorityChanges,
  type ReminderDefaults,
  type ReminderDraft,
  reminderLines,
  restoreOccurrence,
  saveOccurrence,
  type Scope,
  seriesZone,
  sourceBadge,
  sourceLabel,
  updateBody,
  updateRequest,
  type WriteResult,
} from "@/lib/calendar-workspace";
import type { EventExport } from "@/lib/google-calendar";
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
  onSeriesChanged,
  ...shared
}: Shared & {
  state: EventDialogState;
  onStateChange: (state: EventDialogState) => void;
  /**
   * After a create or update, with the event as Calendar stored it (an
   * occurrence changed as This event: the occurrence).
   */
  onSaved: (event: CalendarEvent, created: boolean) => void;
  onDeleted: (id: string) => void;
  /** After a recurring event changed to This and following or All events. */
  onSeriesChanged: () => void;
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
            key={`details-${occurrenceKey(current.event)}-${current.event.updatedAt}`}
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
            key={`edit-${occurrenceKey(current.event)}`}
            onCancel={() =>
              onStateChange({ mode: "details", event: current.event })
            }
            onSaved={(event) => onSaved(event, false)}
            onSeriesChanged={onSeriesChanged}
            remindersOnly={current.remindersOnly}
            {...shared}
          />
        ) : current?.mode === "create" ? (
          <EventForm
            initial={current.draft}
            key="create"
            onCancel={() => onStateChange(null)}
            onSaved={(event) => onSaved(event, true)}
            onSeriesChanged={onSeriesChanged}
            {...shared}
          />
        ) : null}
      </DialogContent>
    </Dialog>
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
  const [asking, setAsking] = useState<ScopeQuestion | null>(null);
  const [error, setError] = useState<string>();
  const [pending, startTransition] = useTransition();
  const reasonId = useId();
  // Feed events, Google calendars you can only read, and occurrences of
  // Google rules Calendar cannot hold: only reminders and priority change.
  const readOnly = !isEditable(event);
  const cancelled = isCancelled(event);
  const source = sourceBadge(event);
  const exported = useEventExport(event);
  const google = googleSyncLabel(event, exported);
  const priority = priorityBadge(event);
  const lines = reminderLines(event);
  // One occurrence of a recurring event (a Life OS or Google series), as
  // listing and search return them.
  const occurrence = event.occurrence && !readOnly ? event.occurrence : null;
  const repeats = eventRecurrenceLabel(event, timeZone);
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

  // This event skips the occurrence; the others end or delete the series.
  const removeOccurrence = (scope: Scope) =>
    run(async () => {
      const { error: failure, status } = await deleteOccurrence(scope, event);
      if (failure && status !== 404) {
        setError(apiErrorMessage(failure) ?? "Could not delete this event.");
        return;
      }
      onDeleted(event.id);
    });

  // A skipped occurrence comes back alone; any other event as a whole.
  const restore = () =>
    run(async () => {
      const { data, error: failure } = occurrence
        ? await restoreOccurrence(event)
        : await calendarClient.PATCH("/v1/events/{eventId}", {
            params: { path: { eventId: event.id } },
            body: { status: "active" },
          });
      if (failure || !data) {
        setError(apiErrorMessage(failure) ?? "Could not restore this event.");
        return;
      }
      onSaved(data);
    });

  // An occurrence opens in the form with its own values; saving asks which
  // occurrences the change applies to.
  const edit = (remindersOnly: boolean) => onEdit(event, remindersOnly);

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
        {cancelled || source || repeats || priority ? (
          <div className="flex flex-wrap items-center gap-1.5">
            {cancelled ? <Badge variant="destructive">Cancelled</Badge> : null}
            {priority ? (
              <Badge
                className={
                  event.priority === "high"
                    ? "border-warning/30 bg-warning-subtle text-warning"
                    : "text-muted-foreground"
                }
                data-testid="event-priority"
                variant="outline"
              >
                {priority}
              </Badge>
            ) : null}
            {source ? (
              <Badge
                className="border-info/30 bg-info-subtle text-info"
                variant="outline"
              >
                {source}
              </Badge>
            ) : null}
            {repeats ? (
              // A long rule ("Weekly on Monday and Wednesday, until …")
              // wraps on a phone instead of being cut off.
              <Badge className="h-auto whitespace-normal" variant="secondary">
                <Repeat aria-hidden="true" />
                <span data-testid="event-recurrence">{repeats}</span>
              </Badge>
            ) : null}
            {occurrence?.modified && !cancelled ? (
              <span className="text-muted-foreground text-xs">
                Changed for this event
              </span>
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
        {google ? (
          <DetailRow icon={CalendarSync} label="Google Calendar">
            <p data-testid="event-google">{google.place}</p>
            <p
              className={cn(
                "text-xs break-words",
                google.failing ? "text-destructive" : "text-muted-foreground",
              )}
              data-testid="event-google-sync"
            >
              {google.state}
            </p>
          </DetailRow>
        ) : null}
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
      {readOnly ? (
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
              {google
                ? "Delete this event permanently, also in Google Calendar?"
                : "Delete this event permanently?"}
            </p>
          </>
        ) : (
          <>
            <Button
              aria-describedby={readOnly ? reasonId : undefined}
              className="sm:mr-auto"
              disabled={pending || readOnly}
              onClick={() =>
                occurrence
                  ? setAsking(deleteQuestion(event, timeZone, today))
                  : setConfirming(true)
              }
              variant="destructive"
            >
              Delete
            </Button>
            {cancelled && !readOnly ? (
              <Button disabled={pending} onClick={restore} variant="outline">
                {occurrence ? "Restore this event" : "Restore"}
              </Button>
            ) : null}
            {readOnly && !cancelled ? (
              <Button
                disabled={pending}
                onClick={() => edit(true)}
                variant="outline"
              >
                Edit reminders and priority
              </Button>
            ) : null}
            <Button
              aria-describedby={readOnly ? reasonId : undefined}
              disabled={pending || readOnly}
              onClick={() => edit(false)}
            >
              Edit
            </Button>
          </>
        )}
      </DialogFooter>
      <ScopeDialog
        onCancel={() => setAsking(null)}
        onChoose={(scope) => {
          setAsking(null);
          removeOccurrence(scope);
        }}
        question={asking}
      />
    </>
  );
}

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

function EventForm({
  event,
  initial,
  onCancel,
  onSaved,
  onSeriesChanged,
  remindersOnly = false,
  timeZone,
  today,
}: Shared & {
  /** The event being edited (or one occurrence of it); a new event when absent. */
  event?: CalendarEvent;
  initial: EventDraft;
  onCancel: () => void;
  onSaved: (event: CalendarEvent) => void;
  onSeriesChanged: () => void;
  /** Read-only events: only their reminders and priority can change. */
  remindersOnly?: boolean;
}) {
  const [draft, setDraft] = useState(initial);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [pending, startTransition] = useTransition();
  // An occurrence's changes wait here while the owner picks their scope.
  const [asking, setAsking] = useState<{
    fields: EventFields;
    question: ScopeQuestion;
  } | null>(null);
  const id = useId();
  // One occurrence of an editable recurring event (Life OS or Google).
  const occurrence = event?.occurrence && isEditable(event) ? event : undefined;
  const repeatChanged = !sameRepeat(draft.repeat, initial.repeat);
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

  /** Saves, then reports Calendar's answer: its 400 message at the field. */
  const save = (
    write: () => Promise<WriteResult>,
    done: (event: CalendarEvent) => void,
  ) =>
    startTransition(async () => {
      try {
        const { data, error, status } = await write();
        if (data) return done(data);
        const message = apiErrorMessage(error);
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

  const submit = (formEvent: FormEvent<HTMLFormElement>) => {
    formEvent.preventDefault();
    const read = readDraft(draft, timeZone);
    if ("errors" in read) {
      setErrors(read.errors);
      return;
    }
    setErrors({});
    // An occurrence asks first which occurrences change; unchanged, there is
    // nothing to save.
    if (occurrence && !remindersOnly) {
      const edit = occurrenceEdit(read.fields, occurrence, repeatChanged);
      if (!edit.changed) return onCancel();
      setAsking({
        fields: read.fields,
        question: editQuestion(occurrence, edit, timeZone, today),
      });
      return;
    }
    // An imported event sends only its reminder and priority changes; with
    // none there is nothing to save (Calendar refuses an empty PATCH).
    const body = event
      ? remindersOnly
        ? reminderAndPriorityChanges(read.fields, event)
        : updateRequest(read.fields, event)
      : undefined;
    if (event && body && Object.keys(body).length === 0) return onCancel();
    save(async () => {
      const result =
        event && body
          ? await calendarClient.PATCH("/v1/events/{eventId}", {
              params: { path: { eventId: event.id } },
              body: updateBody(body),
            })
          : await calendarClient.POST("/v1/events", {
              body: createRequest(read.fields),
            });
      return {
        data: result.data,
        error: result.error,
        status: result.response.status,
      };
    }, onSaved);
  };

  // This event shows the occurrence again; the wider scopes change several
  // occurrences, so the calendar reloads.
  const saveTo = (scope: Scope, fields: EventFields) =>
    occurrence &&
    save(
      () =>
        saveOccurrence(scope, fields, occurrence, {
          repeatChanged,
          ownerZone: timeZone,
        }),
      scope === "this" ? onSaved : onSeriesChanged,
    );

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
              ? "Edit reminders and priority"
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
          <Field data-invalid={Boolean(errors.title)}>
            <FieldLabel htmlFor={`${id}-title`}>Title</FieldLabel>
            <Input
              autoFocus={!event}
              id={`${id}-title`}
              onChange={(change) => update({ title: change.target.value })}
              value={draft.title}
              {...invalid("title")}
            />
            <FieldError id={`${id}-title-error`}>{errors.title}</FieldError>
          </Field>

          <Label className="w-fit font-normal">
            <Checkbox
              checked={draft.allDay}
              onCheckedChange={(allDay) => update({ allDay })}
            />
            All day
          </Label>

          <div className="grid gap-3">
            <FieldSet className="min-w-0 gap-1.5">
              <FieldLegend variant="label">Start</FieldLegend>
              <div className="flex flex-wrap gap-2">
                <DatePicker
                  className="flex-1 basis-36"
                  label="Start date"
                  onChange={(startDate) =>
                    setDraft((current) =>
                      moveStart(current, {
                        startDate,
                        startTime: current.startTime,
                      }),
                    )
                  }
                  required
                  today={today}
                  value={draft.startDate}
                  {...invalid("start")}
                />
                {draft.allDay ? null : (
                  <TimeInput
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
                    value={draft.startTime}
                  />
                )}
              </div>
              <FieldError id={`${id}-start-error`}>{errors.start}</FieldError>
            </FieldSet>
            <FieldSet className="min-w-0 gap-1.5">
              <FieldLegend variant="label">
                {draft.allDay ? "Last day" : "End"}
              </FieldLegend>
              <div className="flex flex-wrap gap-2">
                <DatePicker
                  className="flex-1 basis-36"
                  label="End date"
                  // It cannot end before it starts.
                  min={draft.startDate}
                  onChange={(endDate) => update({ endDate })}
                  required
                  today={today}
                  value={draft.endDate}
                  {...invalid("end")}
                />
                {draft.allDay ? null : (
                  <TimeInput
                    aria-label="End time"
                    className="w-30 shrink-0"
                    onChange={(change) =>
                      update({ endTime: change.target.value })
                    }
                    value={draft.endTime}
                    {...invalid("end")}
                  />
                )}
              </div>
              <FieldError id={`${id}-end-error`}>{errors.end}</FieldError>
            </FieldSet>
          </div>

          {draft.allDay ? null : (
            <Field data-invalid={Boolean(errors.timeZone)}>
              <FieldLabel htmlFor={`${id}-zone`}>Time zone</FieldLabel>
              <NativeSelect
                className="w-full"
                id={`${id}-zone`}
                onChange={(change) => update({ timeZone: change.target.value })}
                value={draft.timeZone}
                {...invalid("timeZone")}
              >
                <NativeSelectOption value="">
                  Your time zone ({timeZone})
                </NativeSelectOption>
                {zoneOptions
                  .filter(
                    (zone) => zone !== timeZone || zone === draft.timeZone,
                  )
                  .map((zone) => (
                    <NativeSelectOption key={zone} value={zone}>
                      {zone.replaceAll("_", " ")}
                    </NativeSelectOption>
                  ))}
              </NativeSelect>
              {draft.timeZone ? (
                <FieldDescription className="text-xs">
                  Start and end are in {editedZone}.
                </FieldDescription>
              ) : null}
              <FieldError id={`${id}-timeZone-error`}>
                {errors.timeZone}
              </FieldError>
            </Field>
          )}

          <RepeatControl
            error={errors.repeat}
            id={`${id}-repeat`}
            onChange={(repeat) => update({ repeat })}
            repeat={draft.repeat}
            startDate={draft.startDate}
            timeZone={seriesZone(draft, timeZone)}
          />

          <Field>
            <FieldLabel htmlFor={`${id}-location`}>Location</FieldLabel>
            <Input
              id={`${id}-location`}
              onChange={(change) => update({ location: change.target.value })}
              value={draft.location}
            />
          </Field>

          <Field>
            <FieldLabel htmlFor={`${id}-notes`}>Notes</FieldLabel>
            <Textarea
              id={`${id}-notes`}
              onChange={(change) => update({ notes: change.target.value })}
              value={draft.notes}
            />
          </Field>
        </>
      )}

      <PriorityField
        id={`${id}-priority`}
        note={
          event && isImported(event) ? defaultPriorityNote(event) : undefined
        }
        onChange={(priority) => update({ priority })}
        priority={draft.priority}
      />

      <FieldSet className="min-w-0 gap-3">
        <FieldLegend className="mb-2" variant="label">
          Reminders
        </FieldLegend>
        <div className="grid gap-1">
          <Label className="w-fit font-normal">
            <Checkbox
              aria-describedby={`${id}-defaults`}
              checked={draft.useDefaultReminders}
              onCheckedChange={(useDefaultReminders) =>
                update({ useDefaultReminders })
              }
            />
            Use default reminders
          </Label>
          <FieldDescription
            className="pl-6 text-xs"
            data-testid="default-reminders"
            id={`${id}-defaults`}
          >
            {defaultRemindersNote(formDefaults(event, draft.allDay, owner), {
              allDay: draft.allDay,
              source: event ? sourceLabel(event) : undefined,
            })}
          </FieldDescription>
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
      </FieldSet>

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
      <ScopeDialog
        onCancel={() => setAsking(null)}
        onChoose={(scope) => {
          if (asking) saveTo(scope, asking.fields);
          setAsking(null);
        }}
        question={asking?.question ?? null}
      />
    </form>
  );
}

/**
 * Priority: how important the event is, which is how important its reminder
 * notifications are. `note` says what an imported event's source sets.
 */
function PriorityField({
  id,
  note,
  onChange,
  priority,
}: {
  id: string;
  note?: string;
  onChange: (priority: Priority) => void;
  priority: Priority;
}) {
  const noteId = `${id}-note`;
  return (
    <Field>
      <FieldLabel htmlFor={id}>Priority</FieldLabel>
      <NativeSelect
        aria-describedby={noteId}
        className="w-full"
        id={id}
        onChange={(change) => onChange(change.target.value as Priority)}
        value={priority}
      >
        {PRIORITIES.map((option) => (
          <NativeSelectOption key={option} value={option}>
            {PRIORITY_LABEL[option]}
          </NativeSelectOption>
        ))}
      </NativeSelect>
      <FieldDescription className="text-xs" id={noteId}>
        {note ?? "How important its reminders are."}
      </FieldDescription>
    </Field>
  );
}

const FREQUENCIES: Frequency[] = ["daily", "weekly", "monthly", "yearly"];

/**
 * Repeat, like Google Calendar's: Does not repeat, Daily, Weekly on the
 * start's weekday, Every weekday, Monthly on its day, Annually on its date,
 * or Custom (every N days, weeks, months, or years; weekly on chosen days;
 * ending never, on a day, or after N occurrences). The labels follow the
 * form's start date. `timeZone` is the zone the series repeats in.
 */
function RepeatControl({
  error,
  id,
  onChange,
  repeat,
  startDate,
  timeZone,
}: {
  error?: string;
  id: string;
  onChange: (repeat: RepeatDraft) => void;
  repeat: RepeatDraft;
  startDate: string;
  timeZone: string;
}) {
  const set = (patch: Partial<RepeatDraft>) =>
    onChange({ ...repeat, ...patch });
  const errorId = `${id}-error`;
  const described = error
    ? { "aria-invalid": true, "aria-describedby": errorId }
    : {};
  const start = weekdayOf(startDate);
  const read =
    repeat.preset === "custom" ? readRepeat(repeat, startDate, timeZone) : null;
  const days = customDays(repeat, startDate);
  const on =
    repeat.preset === "custom"
      ? repeatsOnNote(repeat.frequency, startDate)
      : undefined;

  return (
    // No data-invalid: it would turn the whole Custom panel red.
    <Field>
      <FieldLabel htmlFor={id}>Repeat</FieldLabel>
      <NativeSelect
        className="w-full"
        id={id}
        onChange={(change) => {
          const preset = change.target.value as RepeatDraft["preset"];
          onChange(
            preset === "custom" ? customFrom(repeat) : { ...repeat, preset },
          );
        }}
        value={repeat.preset}
        {...described}
      >
        {repeatOptions(startDate).map((option) => (
          <NativeSelectOption
            disabled={option.disabled && option.value !== repeat.preset}
            key={option.value}
            value={option.value}
          >
            {option.label}
          </NativeSelectOption>
        ))}
      </NativeSelect>

      {repeat.preset === "custom" ? (
        <div className="grid gap-3 rounded-lg border p-3">
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span>Every</span>
            <Input
              aria-label="Repeat every"
              className="w-20"
              inputMode="numeric"
              min={1}
              onChange={(change) => set({ interval: change.target.value })}
              type="number"
              value={repeat.interval}
            />
            <NativeSelect
              aria-label="Repeat unit"
              onChange={(change) =>
                set({ frequency: change.target.value as Frequency })
              }
              value={repeat.frequency}
            >
              {FREQUENCIES.map((frequency) => (
                <NativeSelectOption key={frequency} value={frequency}>
                  {unitLabel(frequency, repeat.interval)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </div>

          {repeat.frequency === "weekly" ? (
            <div className="grid gap-1.5">
              <p
                className="text-muted-foreground text-xs font-medium"
                id={`${id}-days`}
              >
                Repeat on
              </p>
              <ToggleGroup
                aria-labelledby={`${id}-days`}
                className="flex-wrap"
                multiple
                onValueChange={(chosen) =>
                  // The start's weekday always repeats; it is kept only if it
                  // was chosen before, so it still follows the start date.
                  set({
                    days: WEEK.filter(
                      (day) =>
                        chosen.includes(day) &&
                        (day !== start || repeat.days.includes(day)),
                    ),
                  })
                }
                size="sm"
                spacing={1}
                value={days}
                variant="outline"
              >
                {WEEK.map((day) => (
                  <ToggleGroupItem
                    aria-label={WEEKDAY_NAME[day]}
                    className="aria-pressed:bg-primary aria-pressed:text-primary-foreground w-11"
                    // The day it starts on always repeats.
                    disabled={day === start}
                    key={day}
                    value={day}
                  >
                    {WEEKDAY_NAME[day].slice(0, 3)}
                  </ToggleGroupItem>
                ))}
              </ToggleGroup>
              {start ? (
                <p className="text-muted-foreground text-xs">
                  Always on {WEEKDAY_NAME[start]}, the day it starts.
                </p>
              ) : null}
            </div>
          ) : on ? (
            <p className="text-muted-foreground text-xs">{on}</p>
          ) : null}

          <FieldSet className="min-w-0 gap-2">
            <FieldLegend
              className="text-muted-foreground text-xs"
              id={`${id}-ends`}
              variant="label"
            >
              Ends
            </FieldLegend>
            <RadioGroup
              aria-labelledby={`${id}-ends`}
              onValueChange={(ends: RepeatDraft["ends"]) => set({ ends })}
              value={repeat.ends}
            >
              <Label className="w-fit font-normal">
                <RadioGroupItem value="never" />
                Never
              </Label>
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <Label className="w-16 font-normal">
                  <RadioGroupItem value="on" />
                  On
                </Label>
                <DatePicker
                  className="flex-1 basis-36"
                  label="Ends on date"
                  // The last day cannot come before the first.
                  min={startDate}
                  onChange={(until) => set({ ends: "on", until })}
                  value={repeat.until}
                />
              </div>
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <Label className="w-16 font-normal">
                  <RadioGroupItem value="after" />
                  After
                </Label>
                <Input
                  aria-label="Number of occurrences"
                  className="w-20"
                  inputMode="numeric"
                  max={1000}
                  min={1}
                  onChange={(change) =>
                    set({ ends: "after", count: change.target.value })
                  }
                  type="number"
                  value={repeat.count}
                />
                <span className="text-muted-foreground">occurrences</span>
              </div>
            </RadioGroup>
          </FieldSet>

          {read && "recurrence" in read && read.recurrence ? (
            <p className="text-sm" data-testid="repeat-summary">
              {recurrenceLabel(read.recurrence, startDate, timeZone)}
            </p>
          ) : null}
        </div>
      ) : null}
      <FieldError id={errorId}>{error}</FieldError>
    </Field>
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
                <NativeSelect
                  aria-describedby={error ? errorId : undefined}
                  aria-label={`${row} type`}
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
                  <NativeSelectOption value="relative">
                    Before the start
                  </NativeSelectOption>
                  <NativeSelectOption value="localTime">
                    Days before, at a time
                  </NativeSelectOption>
                </NativeSelect>
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
                    <NativeSelect
                      aria-label={`${row} unit`}
                      onChange={(change) =>
                        set(index, {
                          ...reminder,
                          unit: change.target.value as typeof reminder.unit,
                        })
                      }
                      value={reminder.unit}
                    >
                      <NativeSelectOption value="minutes">
                        minutes
                      </NativeSelectOption>
                      <NativeSelectOption value="hours">
                        hours
                      </NativeSelectOption>
                      <NativeSelectOption value="days">days</NativeSelectOption>
                    </NativeSelect>
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
                    <TimeInput
                      aria-label={`${row} time`}
                      className="w-32"
                      onChange={(change) =>
                        set(index, { ...reminder, time: change.target.value })
                      }
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
      <FieldError id={errorId}>{error}</FieldError>
    </div>
  );
}

/**
 * Where a Life OS event is in Google, from the Google Calendar integration
 * (its export to the Life OS calendar); undefined while unknown, for other
 * events, or when the integration cannot be read.
 */
function useEventExport(event: CalendarEvent): EventExport | undefined {
  const [found, setFound] = useState<{ key: string; value?: EventExport }>();
  const key = `${event.id}:${event.updatedAt}`;
  const manual = event.source.kind === "manual";
  useEffect(() => {
    if (!manual) return;
    let current = true;
    googleCalendarClient
      .GET("/v1/exports", { params: { query: { eventId: [event.id] } } })
      .then(({ data }) => {
        if (current) setFound({ key, value: data?.items[0] });
      })
      .catch(() => {
        if (current) setFound({ key });
      });
    return () => {
      current = false;
    };
  }, [event.id, key, manual]);
  return manual && found?.key === key ? found.value : undefined;
}
