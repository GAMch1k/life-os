"use client";

import {
  type FormEvent,
  useEffect,
  useId,
  useState,
  useTransition,
} from "react";

import {
  LABEL,
  NEW_LOCAL_TIME,
  NEW_RELATIVE,
  ReminderList,
  UNAVAILABLE,
} from "@/components/calendar-event-dialog";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { calendarClient } from "@/lib/api/calendar-client";
import {
  apiErrorMessage,
  type CalendarSource,
  fetchReminderDefaults,
  readReminders,
  type Reminder,
  type ReminderDefaults,
  type ReminderDraft,
  sameReminders,
  sentence,
  settingsRequest,
  toReminderDraft,
} from "@/lib/calendar-workspace";
import { cn } from "@/lib/utils";

type Saved = { owner: ReminderDefaults; sources: CalendarSource[] };

type Drafts = {
  timed: ReminderDraft[];
  allDay: ReminderDraft[];
  /** Each source's list, by source id. */
  sources: Record<string, ReminderDraft[]>;
};

/** Errors by section ("timed", "allDay", or a source id), or the form's. */
type Errors = Record<string, string>;

/**
 * Reminder defaults: the owner's default reminders for the events they
 * create in Life OS (one list for timed and one for all-day events, from
 * Calendar settings) and each imported calendar's, such as ZAW's evening
 * before. Every event that uses its default reminders gets them as well as
 * its own. The lists are read each time the dialog opens.
 */
export function ReminderDefaultsDialog({
  onOpenChange,
  onSaved,
  open,
}: {
  onOpenChange: (open: boolean) => void;
  /** After every change was saved. */
  onSaved: () => void;
  open: boolean;
}) {
  return (
    <Dialog onOpenChange={onOpenChange} open={open}>
      <DialogContent className="sm:max-w-lg">
        <DefaultsForm onCancel={() => onOpenChange(false)} onSaved={onSaved} />
      </DialogContent>
    </Dialog>
  );
}

function draftsOf({ owner, sources }: Saved): Drafts {
  return {
    timed: owner.timed.map(toReminderDraft),
    allDay: owner.allDay.map(toReminderDraft),
    sources: Object.fromEntries(
      sources.map((source) => [
        source.id,
        (source.defaultReminders ?? []).map(toReminderDraft),
      ]),
    ),
  };
}

function failure(error: unknown, status: number) {
  const message = apiErrorMessage(error);
  if (status === 404) return "This calendar no longer exists.";
  return message ? sentence(message) : "Could not save these reminders.";
}

function DefaultsForm({
  onCancel,
  onSaved,
}: {
  onCancel: () => void;
  onSaved: () => void;
}) {
  const [saved, setSaved] = useState<Saved>();
  const [drafts, setDrafts] = useState<Drafts>();
  const [failed, setFailed] = useState(false);
  const [errors, setErrors] = useState<Errors>({});
  const [pending, startTransition] = useTransition();
  const id = useId();

  useEffect(() => {
    let current = true;
    fetchReminderDefaults()
      .then((loaded) => {
        if (!current) return;
        setSaved(loaded);
        setDrafts(draftsOf(loaded));
      })
      .catch(() => {
        if (current) setFailed(true);
      });
    return () => {
      current = false;
    };
  }, []);

  const submit = (formEvent: FormEvent<HTMLFormElement>) => {
    formEvent.preventDefault();
    if (!saved || !drafts) return;
    const sections: [string, ReminderDraft[]][] = [
      ["timed", drafts.timed],
      ["allDay", drafts.allDay],
      ...saved.sources.map((source): [string, ReminderDraft[]] => [
        source.id,
        drafts.sources[source.id] ?? [],
      ]),
    ];
    const read: Record<string, Reminder[]> = {};
    const problems: Errors = {};
    for (const [key, list] of sections) {
      const result = readReminders(list);
      if ("error" in result) problems[key] = result.error;
      else read[key] = result.reminders;
    }
    if (Object.keys(problems).length) return setErrors(problems);
    setErrors({});

    // Only what changed is saved, the owner's lists first, then each source.
    // What was saved stays saved when a later request fails, so trying again
    // sends only the rest.
    startTransition(async () => {
      try {
        let current = saved;
        const body = settingsRequest(current.owner, {
          timed: read.timed,
          allDay: read.allDay,
        });
        if (body) {
          const result = await calendarClient.PATCH("/v1/settings", { body });
          if (!result.data) {
            const changed = Object.keys(body.defaultReminders ?? {});
            return setErrors({
              [changed.length === 1 ? changed[0] : "form"]: failure(
                result.error,
                result.response.status,
              ),
            });
          }
          current = { ...current, owner: result.data.defaultReminders };
          setSaved(current);
        }
        for (const source of current.sources) {
          const next = read[source.id];
          if (!next || sameReminders(source.defaultReminders, next)) continue;
          const result = await calendarClient.PATCH("/v1/sources/{sourceId}", {
            params: { path: { sourceId: source.id } },
            body: { defaultReminders: next },
          });
          if (!result.data) {
            return setErrors({
              [source.id]: failure(result.error, result.response.status),
            });
          }
          const updated = result.data;
          current = {
            ...current,
            sources: current.sources.map((item) =>
              item.id === updated.id ? updated : item,
            ),
          };
          setSaved(current);
        }
        onSaved();
      } catch {
        setErrors({ form: UNAVAILABLE });
      }
    });
  };

  return (
    <form className="grid gap-4" noValidate onSubmit={submit}>
      <DialogHeader>
        <DialogTitle>Reminder defaults</DialogTitle>
        <DialogDescription>
          Every event that uses default reminders gets these as well as its own.
          A change applies to all of them.
        </DialogDescription>
      </DialogHeader>

      {failed ? (
        <p className="text-destructive text-sm" role="alert">
          {UNAVAILABLE}
        </p>
      ) : !saved || !drafts ? (
        <p className="text-muted-foreground text-sm">
          Loading reminder defaults…
        </p>
      ) : (
        <>
          <DefaultsSection
            error={errors.timed}
            errorId={`${id}-timed-error`}
            fresh={NEW_RELATIVE}
            label="Timed default"
            note="Events you create in Life OS with a start time."
            onChange={(timed) => setDrafts({ ...drafts, timed })}
            reminders={drafts.timed}
            title="Your timed events"
          />
          <DefaultsSection
            error={errors.allDay}
            errorId={`${id}-allDay-error`}
            fresh={NEW_LOCAL_TIME}
            label="All-day default"
            note="Events you create in Life OS that last all day."
            onChange={(allDay) => setDrafts({ ...drafts, allDay })}
            reminders={drafts.allDay}
            title="Your all-day events"
          />
          {saved.sources.map((source, index) => (
            <DefaultsSection
              error={errors[source.id]}
              errorId={`${id}-source-${index}-error`}
              fresh={NEW_RELATIVE}
              key={source.id}
              label={`${source.name} default`}
              note={`Events imported from ${source.name}.`}
              onChange={(list) =>
                setDrafts({
                  ...drafts,
                  sources: { ...drafts.sources, [source.id]: list },
                })
              }
              reminders={drafts.sources[source.id] ?? []}
              title={source.name}
            />
          ))}
        </>
      )}

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
        <Button disabled={pending || !drafts} type="submit">
          {pending ? "Saving…" : "Save"}
        </Button>
      </DialogFooter>
    </form>
  );
}

function DefaultsSection({
  error,
  errorId,
  fresh,
  label,
  note,
  onChange,
  reminders,
  title,
}: {
  error?: string;
  errorId: string;
  fresh: ReminderDraft;
  label: string;
  note: string;
  onChange: (reminders: ReminderDraft[]) => void;
  reminders: ReminderDraft[];
  title: string;
}) {
  return (
    <fieldset className="grid min-w-0 gap-2">
      <legend className={cn(LABEL, "mb-1")}>{title}</legend>
      <p className="text-muted-foreground text-xs">{note}</p>
      <ReminderList
        empty="No default reminders."
        error={error}
        errorId={errorId}
        fresh={fresh}
        label={label}
        onChange={onChange}
        reminders={reminders}
      />
    </fieldset>
  );
}
