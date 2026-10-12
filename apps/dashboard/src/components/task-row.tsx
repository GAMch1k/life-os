"use client";

import { useState, useTransition } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { DatePicker, TimeInput } from "@/components/date-picker";
import { Button } from "@/components/ui/button";
import { FieldLegend, FieldSet } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { tasksClient } from "@/lib/api/tasks-client";
import type { Task } from "@/lib/tasks-summary";
import { dueLabel, fromDueFields, toDueFields } from "@/lib/tasks-workspace";
import { cn } from "@/lib/utils";

type RowProps = {
  today: string;
  timeZone: string;
  onChange: (task: Task) => void;
  /** The Task was deleted (or was already gone): it leaves every list. */
  onDeleted: (id: string) => void;
  /** The Task the page's link names (`?task=<id>`): it is highlighted. */
  focusId?: string;
};

/**
 * A bordered list of Task rows. It is a size container: in a wide list the
 * title, due date, and actions sit in fixed columns so dates line up whatever
 * the title length; in a narrow one (a phone, a schedule day) the title takes
 * the first line and the date and actions wrap below it.
 */
export function TaskList({ tasks, ...row }: RowProps & { tasks: Task[] }) {
  return (
    <ul className="bg-card @container rounded-lg border px-3">
      {tasks.map((task) => (
        <TaskRow key={task.id} task={task} {...row} />
      ))}
    </ul>
  );
}

function TaskRow({
  task,
  today,
  timeZone,
  onChange,
  onDeleted,
  focusId,
}: RowProps & { task: Task }) {
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [deleteError, setDeleteError] = useState<string>();
  const [title, setTitle] = useState(task.title);
  const [notes, setNotes] = useState(task.notes ?? "");
  const [due, setDue] = useState(() => toDueFields(task.dueAt, timeZone));
  const [error, setError] = useState<string>();
  const [pending, startTransition] = useTransition();
  const patch = (body: Record<string, unknown>) =>
    startTransition(async () => {
      setError(undefined);
      const { data, error: requestError } = await tasksClient.PATCH(
        "/v1/tasks/{taskId}",
        { params: { path: { taskId: task.id } }, body },
      );
      if (requestError || !data) return setError("Could not save this Task.");
      onChange(data);
      setTitle(data.title);
      setNotes(data.notes ?? "");
      setDue(toDueFields(data.dueAt, timeZone));
      setEditing(false);
    });
  const lifecycle = (
    path: "/v1/tasks/{taskId}/complete" | "/v1/tasks/{taskId}/reopen",
  ) =>
    startTransition(async () => {
      setError(undefined);
      const { data, error: requestError } = await tasksClient.POST(path, {
        params: { path: { taskId: task.id } },
      });
      if (requestError || !data) return setError("Could not update this Task.");
      onChange(data);
    });
  const remove = () =>
    startTransition(async () => {
      setDeleteError(undefined);
      try {
        const { response } = await tasksClient.DELETE("/v1/tasks/{taskId}", {
          params: { path: { taskId: task.id } },
        });
        // Already gone is as good as deleted.
        if (!response.ok && response.status !== 404)
          return setDeleteError("Could not delete this Task.");
      } catch {
        return setDeleteError("Could not delete this Task.");
      }
      setConfirming(false);
      onDeleted(task.id);
    });
  const dueAt = fromDueFields(due.date, due.time, timeZone);
  const shownDue = dueLabel(task.dueAt, today, timeZone);
  // The linked Task is highlighted as Activity highlights the entry its link
  // opens; its divider gives way to the highlight.
  const focused = task.id === focusId;
  return (
    <li
      aria-current={focused ? "true" : undefined}
      className={cn(
        "border-b py-2.5 last:border-0",
        focused && "bg-secondary -mx-2 rounded-lg border-transparent px-2",
      )}
      id={`task-${task.id}`}
    >
      {editing ? (
        <div className="space-y-2">
          <Input
            aria-label="Task title"
            value={title}
            onChange={(event) => setTitle(event.target.value)}
          />
          <Textarea
            aria-label="Task notes"
            value={notes}
            onChange={(event) => setNotes(event.target.value)}
          />
          {/* No time is due when the day starts; no date leaves it as it is
              (Clear date removes it). */}
          <FieldSet className="gap-1">
            <FieldLegend
              className="text-muted-foreground mb-1 text-xs font-normal"
              variant="label"
            >
              Due date
            </FieldLegend>
            <div className="flex flex-wrap gap-2">
              <DatePicker
                className="w-44"
                label="Due date"
                onChange={(date) => setDue((current) => ({ ...current, date }))}
                today={today}
                value={due.date}
              />
              <TimeInput
                aria-label="Due time"
                className="w-28"
                disabled={!due.date}
                onChange={(event) =>
                  setDue((current) => ({
                    ...current,
                    time: event.target.value,
                  }))
                }
                value={due.time}
              />
            </div>
          </FieldSet>
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              disabled={pending || !title.trim()}
              onClick={() =>
                patch({
                  title: title.trim(),
                  notes,
                  ...(dueAt ? { dueAt } : {}),
                })
              }
            >
              Save
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={pending}
              onClick={() => patch({ clearDueAt: true })}
            >
              Clear date
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={pending}
              onClick={() => setEditing(false)}
            >
              Cancel
            </Button>
            <Button
              aria-label={`Delete ${task.title}`}
              className="ml-auto"
              size="sm"
              variant="destructive"
              disabled={pending}
              onClick={() => {
                setDeleteError(undefined);
                setConfirming(true);
              }}
            >
              Delete
            </Button>
          </div>
          {/* Deleting cannot be undone, so it is confirmed first. A recurring
              Task is one Task: the whole series goes. */}
          <Dialog
            open={confirming}
            onOpenChange={(open) => {
              if (!pending) setConfirming(open);
            }}
          >
            <DialogContent>
              <DialogHeader>
                <DialogTitle className="wrap-anywhere">
                  Delete “{task.title}”?
                </DialogTitle>
                <DialogDescription>
                  This can&apos;t be undone.
                </DialogDescription>
              </DialogHeader>
              {deleteError ? (
                <p className="text-destructive text-sm" role="alert">
                  {deleteError}
                </p>
              ) : null}
              <DialogFooter>
                <Button
                  disabled={pending}
                  onClick={() => setConfirming(false)}
                  variant="outline"
                >
                  Keep it
                </Button>
                <Button
                  disabled={pending}
                  onClick={remove}
                  variant="destructive"
                >
                  {pending ? "Deleting…" : "Delete Task"}
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        </div>
      ) : (
        <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 @xl:grid-cols-[minmax(0,1fr)_10rem_9rem]">
          <div className="col-span-2 min-w-0 @xl:col-span-1">
            <p
              className={cn(
                "font-medium wrap-anywhere @xl:truncate",
                task.completed && "text-muted-foreground line-through",
              )}
              title={task.title}
            >
              {task.title}
            </p>
            {task.notes ? (
              <p className="text-muted-foreground mt-0.5 truncate text-sm">
                {task.notes}
              </p>
            ) : null}
          </div>
          {task.dueAt ? (
            <time
              className="text-muted-foreground truncate text-xs tabular-nums"
              data-slot="task-due"
              dateTime={task.dueAt}
            >
              {shownDue}
            </time>
          ) : (
            <span
              className="text-muted-foreground truncate text-xs"
              data-slot="task-due"
            >
              {shownDue}
            </span>
          )}
          <div className="flex justify-end gap-1">
            <Button
              aria-label={`Edit ${task.title}`}
              size="sm"
              variant="ghost"
              disabled={pending}
              onClick={() => setEditing(true)}
            >
              Edit
            </Button>
            <Button
              aria-label={`${task.completed ? "Reopen" : "Complete"} ${task.title}`}
              size="sm"
              variant="outline"
              disabled={pending}
              onClick={() =>
                lifecycle(
                  task.completed
                    ? "/v1/tasks/{taskId}/reopen"
                    : "/v1/tasks/{taskId}/complete",
                )
              }
            >
              {task.completed ? "Reopen" : "Complete"}
            </Button>
          </div>
        </div>
      )}
      {error ? <p className="text-destructive mt-2 text-sm">{error}</p> : null}
    </li>
  );
}
