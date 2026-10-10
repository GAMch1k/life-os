"use client";

import Link from "next/link";
import { Inbox, LoaderCircle, Send } from "lucide-react";
import {
  type FormEvent,
  type KeyboardEvent,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";

import { InboxStateBadge } from "@/components/inbox-summary";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Textarea } from "@/components/ui/textarea";
import { VoiceCapture } from "@/components/voice-capture";
import { inboxClient } from "@/lib/api/inbox-client";
import {
  formatInboxTime,
  type InboxBacklog,
  type InboxItem,
  inboxItemStatus,
  isPendingState,
  retryReasonLabel,
} from "@/lib/inbox-summary";
import { cn } from "@/lib/utils";

/** Captures from this visit that stay listed under the composer. */
const SESSION_CAPTURES = 3;
const POLL_MS = 3_000;
/** Stop following a capture that is still pending after this long. */
const FOLLOW_MS = 15 * 60_000;
const CONFIRMATION_MS = 2_500;

type SessionCapture = { item: InboxItem; since: number };

/** Whether a capture from this visit is still worth polling for its outcome. */
function following({ item, since }: SessionCapture, now: number) {
  return (
    isPendingState(item.state) &&
    inboxItemStatus(item, new Date(now)).kind !== "retry" &&
    now - since < FOLLOW_MS
  );
}

/**
 * The one capture surface: a chat-like composer for text, the Voice recorder
 * beside the Capture button, and the outcome of what was just captured.
 * Enter captures, Shift+Enter starts a new line. Pass `backlog` to show the
 * pending/failed Inbox counts under it (Home).
 */
export function CaptureCard({
  backlog: initialBacklog,
  focusOnDesktop = false,
  renderedAt,
  timeZone,
}: {
  backlog?: InboxBacklog | null;
  focusOnDesktop?: boolean;
  renderedAt: string;
  timeZone: string;
}) {
  const [content, setContent] = useState("");
  const [sending, setSending] = useState(false);
  const [notice, setNotice] = useState<{
    kind: "ok" | "error";
    text: string;
  }>();
  const [captures, setCaptures] = useState<SessionCapture[]>([]);
  const [backlog, setBacklog] = useState(initialBacklog);
  const [now, setNow] = useState(() => new Date(renderedAt));
  const field = useRef<HTMLTextAreaElement>(null);
  // Guards against a second submit before React re-renders with `sending`.
  const submitting = useRef(false);
  const showBacklog = initialBacklog !== undefined;

  useEffect(() => {
    // Desktop opens ready to type; phones keep the keyboard closed until tapped.
    if (focusOnDesktop && window.matchMedia("(pointer: fine)").matches) {
      field.current?.focus({ preventScroll: true });
    }
  }, [focusOnDesktop]);

  useEffect(() => {
    if (notice?.kind !== "ok") return;
    const timer = window.setTimeout(
      () => setNotice(undefined),
      CONFIRMATION_MS,
    );
    return () => window.clearTimeout(timer);
  }, [notice]);

  const refreshBacklog = useCallback(async () => {
    if (!showBacklog) return;
    try {
      const { data } = await inboxClient.GET("/v1/backlog");
      if (data) setBacklog(data);
    } catch {
      // Keep the last known counts; the next change refreshes them again.
    }
  }, [showBacklog]);

  useEffect(() => {
    const watched = captures.filter((capture) =>
      following(capture, Date.now()),
    );
    if (!watched.length) return;
    const timer = window.setTimeout(async () => {
      const fresh = await Promise.all(
        watched.map(({ item }) =>
          inboxClient
            .GET("/v1/inbox-items/{itemId}", {
              params: { path: { itemId: item.id } },
            })
            .then(
              ({ data }) => data,
              () => undefined,
            ),
        ),
      );
      const byId = new Map(
        fresh.flatMap((item) => (item ? [[item.id, item] as const] : [])),
      );
      setNow(new Date());
      setCaptures((current) =>
        current.map((capture) => ({
          ...capture,
          item: byId.get(capture.item.id) ?? capture.item,
        })),
      );
      if (
        watched.some(({ item }) => {
          const next = byId.get(item.id);
          return next !== undefined && next.state !== item.state;
        })
      ) {
        void refreshBacklog();
      }
    }, POLL_MS);
    return () => window.clearTimeout(timer);
  }, [captures, refreshBacklog]);

  async function submit(fromKeyboard: boolean) {
    if (submitting.current) return;
    const text = content.trim();
    if (!text) {
      if (!fromKeyboard) {
        setNotice({ kind: "error", text: "Write something to capture first." });
      }
      return;
    }
    submitting.current = true;
    setSending(true);
    setNotice(undefined);
    // Clear right away like a chat composer; anything typed meanwhile stays.
    setContent("");
    try {
      const { data, error } = await inboxClient.POST("/v1/inbox-items", {
        body: { content: text, source: "manual" },
      });
      if (error || !data) throw new Error("capture failed");
      setNow(new Date());
      setCaptures((current) =>
        [{ item: data, since: Date.now() }, ...current].slice(
          0,
          SESSION_CAPTURES,
        ),
      );
      setNotice({ kind: "ok", text: "Captured." });
      void refreshBacklog();
    } catch {
      setContent((current) => (current ? `${text}\n${current}` : text));
      setNotice({
        kind: "error",
        text: "Could not save that capture. Please try again.",
      });
    } finally {
      submitting.current = false;
      setSending(false);
      // Keep typing after a capture, also after clicking Capture (the button
      // was disabled meanwhile, which may have dropped focus to the body).
      const active = document.activeElement;
      if (
        !active ||
        active === document.body ||
        active === field.current ||
        (active instanceof HTMLButtonElement && active.type === "submit")
      ) {
        field.current?.focus({ preventScroll: true });
      }
    }
  }

  function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void submit(false);
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key !== "Enter" || event.shiftKey || event.altKey) return;
    // Enter also confirms IME composition (for example Japanese input).
    if (event.nativeEvent.isComposing || event.keyCode === 229) return;
    event.preventDefault();
    if (!event.repeat) void submit(true);
  }

  return (
    <Card aria-label="Capture" id="capture" role="region" size="sm">
      <CardContent className="space-y-2">
        <form className="space-y-2" onSubmit={onSubmit}>
          <label className="sr-only" htmlFor="inbox-capture">
            Capture a thought
          </label>
          <Textarea
            aria-describedby="inbox-capture-hint"
            className="max-h-64 min-h-16 resize-none sm:min-h-20"
            enterKeyHint="send"
            id="inbox-capture"
            onChange={(event) => setContent(event.target.value)}
            onKeyDown={onKeyDown}
            placeholder="Capture a thought…"
            ref={field}
            value={content}
          />
          <div className="flex items-start gap-2">
            <VoiceCapture />
            <span
              className="text-muted-foreground hidden shrink-0 self-center text-xs lg:inline"
              id="inbox-capture-hint"
            >
              Enter to capture · Shift+Enter for a new line
            </span>
            <Button disabled={sending} size="lg" type="submit">
              {sending ? (
                <LoaderCircle aria-hidden="true" className="animate-spin" />
              ) : (
                <Send aria-hidden="true" />
              )}
              Capture
            </Button>
          </div>
          <p
            aria-live="polite"
            className={cn(
              "text-xs",
              notice?.kind === "error"
                ? "text-destructive"
                : "text-muted-foreground",
              !notice && "sr-only",
            )}
            role="status"
          >
            {notice?.text}
          </p>
        </form>
        {captures.length ? (
          <ul aria-label="Just captured" className="divide-y border-t">
            {captures.map(({ item }) => {
              const status = inboxItemStatus(item, now);
              const badge = (
                <InboxStateBadge
                  now={now}
                  status={status}
                  timeZone={timeZone}
                />
              );
              return (
                <li
                  className="flex items-center gap-2 py-1.5 last:pb-0"
                  key={item.id}
                >
                  <span className="min-w-0 flex-1 truncate">
                    {item.content}
                  </span>
                  {status.kind === "review" ? (
                    <Link href="/inbox#human-review">{badge}</Link>
                  ) : status.kind === "failed" ? (
                    <Link href="/inbox#failed">{badge}</Link>
                  ) : (
                    badge
                  )}
                </li>
              );
            })}
          </ul>
        ) : null}
        {showBacklog ? (
          <BacklogLine backlog={backlog} now={now} timeZone={timeZone} />
        ) : null}
      </CardContent>
    </Card>
  );
}

/**
 * Automatic work still in Inbox: pending (raw or processing, with retries)
 * and failed. Human Review is counted once, in its own card.
 */
function BacklogLine({
  backlog,
  now,
  timeZone,
}: {
  backlog: InboxBacklog | null | undefined;
  now: Date;
  timeZone: string;
}) {
  if (backlog === null) {
    return (
      <p className="text-muted-foreground border-t pt-2 text-xs">
        Inbox counts are unavailable.
      </p>
    );
  }
  const failed = backlog?.failedCount ?? 0;
  if (!backlog || (!backlog.pendingCount && !failed)) return null;
  return (
    <div className="text-muted-foreground flex flex-wrap items-center gap-x-3 gap-y-1 border-t pt-2 text-xs">
      <Inbox aria-hidden="true" className="size-3.5" />
      {backlog.pendingCount ? (
        <Link className="hover:text-foreground" href="/inbox#pending">
          {backlog.pendingCount} pending
          {backlog.retryingCount ? `, ${backlog.retryingCount} retrying` : ""}
        </Link>
      ) : null}
      {backlog.pendingCount && backlog.nextRetryAt ? (
        <span>
          next retry {formatInboxTime(backlog.nextRetryAt, timeZone, now)}
          {backlog.nextRetryReason
            ? ` (${retryReasonLabel(backlog.nextRetryReason)})`
            : ""}
        </span>
      ) : null}
      {failed ? (
        <Link className="text-destructive hover:underline" href="/inbox#failed">
          {failed} failed
        </Link>
      ) : null}
    </div>
  );
}
