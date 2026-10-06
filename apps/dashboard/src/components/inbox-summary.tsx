"use client";

import { Inbox, LoaderCircle, Mic, Send } from "lucide-react";
import { type FormEvent, useState, useTransition } from "react";

import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Textarea } from "@/components/ui/textarea";
import { inboxClient } from "@/lib/api/inbox-client";
import type { InboxItem, InboxSummaryState } from "@/lib/inbox-summary";

export function InboxSummary({ state }: { state: InboxSummaryState }) {
  const [items, setItems] = useState<InboxItem[]>(
    state.kind === "ready" ? state.items : [],
  );
  const [content, setContent] = useState("");
  const [error, setError] = useState<string>();
  const [isPending, startTransition] = useTransition();

  if (state.kind === "loading") {
    return (
      <Card id="inbox" aria-busy="true">
        <CardHeader>
          <CardTitle>Inbox</CardTitle>
          <CardDescription>Loading unprocessed captures…</CardDescription>
        </CardHeader>
      </Card>
    );
  }

  if (state.kind === "unavailable") {
    return (
      <Card id="inbox">
        <CardHeader>
          <CardTitle>Inbox</CardTitle>
          <CardDescription>
            Inbox is temporarily unavailable. Your captures have not been
            replaced with placeholders.
          </CardDescription>
        </CardHeader>
      </Card>
    );
  }

  function capture(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const capturedContent = content.trim();
    if (!capturedContent) {
      setError("Write something to capture first.");
      return;
    }

    setError(undefined);
    startTransition(async () => {
      const { data, error: requestError } = await inboxClient.POST(
        "/v1/inbox-items",
        { body: { content: capturedContent, source: "manual" } },
      );
      if (requestError || !data) {
        setError("Could not save that capture. Please try again.");
        return;
      }
      setItems((current) => [data, ...current]);
      setContent("");
    });
  }

  return (
    <Card id="inbox">
      <CardHeader>
        <div className="flex items-center gap-2">
          <Inbox aria-hidden="true" className="size-4" />
          <CardTitle>Inbox</CardTitle>
        </div>
        <CardDescription>
          {items.length === 0
            ? "Capture a thought before it becomes a task or a plan."
            : `${items.length} unprocessed ${items.length === 1 ? "capture" : "captures"}.`}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <form className="space-y-2" onSubmit={capture}>
          <label className="sr-only" htmlFor="inbox-capture">
            Capture a thought
          </label>
          <Textarea
            id="inbox-capture"
            onChange={(event) => setContent(event.target.value)}
            placeholder="What do you need to remember?"
            value={content}
          />
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
              <Mic aria-hidden="true" className="size-3.5" /> Voice capture will
              appear here.
            </p>
            <Button disabled={isPending} size="sm" type="submit">
              {isPending ? (
                <LoaderCircle aria-hidden="true" className="animate-spin" />
              ) : (
                <Send aria-hidden="true" />
              )}
              Capture
            </Button>
          </div>
          {error ? <p className="text-destructive text-sm">{error}</p> : null}
        </form>

        {items.length ? (
          <ul className="divide-y rounded-lg border">
            {items.map((item) => (
              <li className="p-3 text-sm sm:p-4" key={item.id}>
                {item.content}
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-muted-foreground text-sm">
            Your unprocessed captures will show up here.
          </p>
        )}
      </CardContent>
    </Card>
  );
}
