"use client";

import { Check, CheckSquare2, LoaderCircle } from "lucide-react";
import { useState, useTransition } from "react";

import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { tasksClient } from "@/lib/api/tasks-client";
import type { Task, TasksSummaryState } from "@/lib/tasks-summary";

function dueLabel(dueAt: string | null | undefined) {
  if (!dueAt) {
    return "Due today";
  }

  const due = new Date(dueAt);
  const today = new Date();
  const isToday = due.toDateString() === today.toDateString();
  return isToday
    ? `Today, ${due.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}`
    : `Overdue · ${due.toLocaleDateString([], { month: "short", day: "numeric" })}`;
}

export function TasksSummary({ state }: { state: TasksSummaryState }) {
  const [tasks, setTasks] = useState<Task[]>(
    state.kind === "ready" ? state.tasks : [],
  );
  const [updatingTaskId, setUpdatingTaskId] = useState<string>();
  const [error, setError] = useState<string>();
  const [isPending, startTransition] = useTransition();

  if (state.kind === "loading") {
    return (
      <Card id="tasks" aria-busy="true">
        <CardHeader>
          <CardTitle>Today&apos;s tasks</CardTitle>
          <CardDescription>Loading tasks due today…</CardDescription>
        </CardHeader>
      </Card>
    );
  }

  if (state.kind === "unavailable") {
    return (
      <Card id="tasks">
        <CardHeader>
          <CardTitle>Today&apos;s tasks</CardTitle>
          <CardDescription>
            Tasks are temporarily unavailable. Your data has not been replaced
            with placeholders.
          </CardDescription>
        </CardHeader>
      </Card>
    );
  }

  if (state.kind === "empty") {
    return (
      <Card id="tasks">
        <CardHeader>
          <CardTitle>Today&apos;s tasks</CardTitle>
          <CardDescription>
            Nothing is due today. Take a breath or add your next action.
          </CardDescription>
        </CardHeader>
      </Card>
    );
  }

  function completeTask(taskId: string) {
    setError(undefined);
    setUpdatingTaskId(taskId);
    startTransition(async () => {
      const { data, error: requestError } = await tasksClient.POST(
        "/v1/tasks/{taskId}/complete",
        { params: { path: { taskId } } },
      );

      if (requestError || !data) {
        setError("Could not complete that task. Please try again.");
      } else {
        setTasks((current) =>
          current.map((task) => (task.id === taskId ? data : task)),
        );
      }
      setUpdatingTaskId(undefined);
    });
  }

  return (
    <Card id="tasks">
      <CardHeader>
        <div className="flex items-center gap-2">
          <CheckSquare2 aria-hidden="true" className="size-4" />
          <CardTitle>Today&apos;s tasks</CardTitle>
        </div>
        <CardDescription>
          Due today and anything still waiting from before.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <ul className="divide-y rounded-lg border">
          {tasks.map((task) => {
            const updating = updatingTaskId === task.id;
            return (
              <li
                className="flex items-center justify-between gap-4 p-3 sm:p-4"
                key={task.id}
              >
                <div className="min-w-0">
                  <p
                    className={
                      task.completed
                        ? "text-muted-foreground line-through"
                        : "font-medium"
                    }
                  >
                    {task.title}
                  </p>
                  <p className="text-muted-foreground mt-1 text-xs">
                    {task.completed ? "Completed" : dueLabel(task.dueAt)}
                  </p>
                </div>
                {task.completed ? (
                  <span className="text-muted-foreground flex shrink-0 items-center gap-1 text-sm">
                    <Check aria-hidden="true" className="size-4" /> Done
                  </span>
                ) : (
                  <Button
                    aria-label={`Complete ${task.title}`}
                    disabled={isPending}
                    onClick={() => completeTask(task.id)}
                    size="sm"
                    variant="outline"
                  >
                    {updating ? (
                      <LoaderCircle
                        aria-hidden="true"
                        className="animate-spin"
                      />
                    ) : (
                      <Check aria-hidden="true" />
                    )}
                    Complete
                  </Button>
                )}
              </li>
            );
          })}
        </ul>
        {error ? (
          <p className="text-destructive mt-3 text-sm">{error}</p>
        ) : null}
      </CardContent>
    </Card>
  );
}
