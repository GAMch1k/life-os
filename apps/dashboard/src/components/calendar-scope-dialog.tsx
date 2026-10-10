"use client";

import { useState } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { Button } from "@/components/ui/button";
import {
  type CalendarEvent,
  deleteScopes,
  editScopes,
  eventWhenLabel,
  type Scope,
  SCOPE_LABEL,
} from "@/lib/calendar-workspace";

/** What the owner is asked about an occurrence of a recurring event. */
export type ScopeQuestion = {
  /** A save or a drag ("edit"), or a delete. */
  action: "edit" | "delete";
  scopes: Scope[];
  /** Which occurrence, such as "Gym · Wed 14 Oct · 18:00–19:00". */
  subject?: string;
  /** What the choices do, or why one is missing. */
  note?: string;
};

const TITLE = {
  edit: "Edit recurring event",
  delete: "Delete recurring event",
};

/** "Gym · Wed 14 Oct · 18:00–19:00": the occurrence asked about. */
function subjectOf(occurrence: CalendarEvent, timeZone: string, today: string) {
  return `${occurrence.title} · ${eventWhenLabel(occurrence, timeZone, today)}`;
}

/**
 * Saving or dragging an occurrence. A change of what belongs to the series
 * (`seriesOnly`: Repeat, All day, the time zone) can't apply to this event
 * alone, so This event is not offered and the note says why.
 */
export function editQuestion(
  occurrence: CalendarEvent,
  { seriesOnly }: { seriesOnly: boolean },
  timeZone: string,
  today: string,
): ScopeQuestion {
  return {
    action: "edit",
    scopes: editScopes({ seriesOnly }),
    subject: subjectOf(occurrence, timeZone, today),
    note: seriesOnly
      ? "Repeat, All day, and the time zone belong to the whole series, so they can't change for this event only."
      : undefined,
  };
}

/** Deleting an occurrence: This event skips it, the others delete for good. */
export function deleteQuestion(
  occurrence: CalendarEvent,
  timeZone: string,
  today: string,
): ScopeQuestion {
  const scopes = deleteScopes(occurrence);
  return {
    action: "delete",
    scopes,
    subject: subjectOf(occurrence, timeZone, today),
    note: scopes.includes("this")
      ? "This event is skipped and kept as history: Show cancelled shows it again and can restore it. The other choices delete permanently."
      : "This event is already skipped. The choices delete permanently.",
  };
}

/**
 * Asks which occurrences a change applies to, like Google Calendar: This
 * event, This and following events, or All events (`question.scopes`).
 * Choosing one closes it (`onChoose`); Cancel, Escape, or a click outside
 * leaves everything as it was (`onCancel`). Rendered inside the event dialog
 * it opens over it, as a nested dialog. Null closes it.
 */
export function ScopeDialog({
  question,
  onCancel,
  onChoose,
}: {
  question: ScopeQuestion | null;
  onCancel: () => void;
  onChoose: (scope: Scope) => void;
}) {
  // The last question stays rendered while the dialog animates closed.
  const [shown, setShown] = useState(question);
  if (question && question !== shown) setShown(question);
  const current = question ?? shown;

  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) onCancel();
      }}
      open={question !== null}
    >
      <DialogContent className="sm:max-w-sm" showCloseButton={false}>
        <DialogHeader className="pr-0">
          <DialogTitle>{current ? TITLE[current.action] : null}</DialogTitle>
          {current?.subject ? (
            <DialogDescription className="break-words">
              {current.subject}
            </DialogDescription>
          ) : null}
        </DialogHeader>
        <div className="grid gap-2">
          {current?.scopes.map((scope) => (
            <Button
              className="justify-start"
              key={scope}
              onClick={() => onChoose(scope)}
              variant={current.action === "delete" ? "destructive" : "outline"}
            >
              {SCOPE_LABEL[scope]}
            </Button>
          ))}
        </div>
        {current?.note ? (
          <p className="text-muted-foreground text-xs">{current.note}</p>
        ) : null}
        <DialogFooter>
          <Button onClick={onCancel} variant="outline">
            Cancel
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
