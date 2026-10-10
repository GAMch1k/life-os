"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { Inbox } from "lucide-react";
import { useCallback, useEffect, useState } from "react";

import { CaptureComposer } from "@/components/capture-composer";
import { InboxStateBadge } from "@/components/inbox-summary";
import { Card, CardContent } from "@/components/ui/card";
import { inboxClient } from "@/lib/api/inbox-client";
import { onCapture } from "@/lib/capture-session";
import {
  formatInboxTime,
  type InboxBacklog,
  type InboxItem,
  inboxItemStatus,
  isPendingState,
  leftProcessing,
  retryReasonLabel,
} from "@/lib/inbox-summary";

/** Captures from this visit that stay listed under the composer. */
const SESSION_CAPTURES = 3;
const POLL_MS = 3_000;
/** Stop following a capture that is still pending after this long. */
const FOLLOW_MS = 15 * 60_000;

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
 * The one capture surface: the composer (text, and the Voice recorder beside
 * the Capture button) and the outcome of what was just captured on this page.
 * Pass `backlog` to show the pending/failed Inbox counts under it (Home).
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
  const router = useRouter();
  const [captures, setCaptures] = useState<SessionCapture[]>([]);
  const [backlog, setBacklog] = useState(initialBacklog);
  const [now, setNow] = useState(() => new Date(renderedAt));
  const showBacklog = initialBacklog !== undefined;

  const refreshBacklog = useCallback(async () => {
    if (!showBacklog) return;
    try {
      const { data } = await inboxClient.GET("/v1/backlog");
      if (data) setBacklog(data);
    } catch {
      // Keep the last known counts; the next change refreshes them again.
    }
  }, [showBacklog]);

  // Every text capture made on this page is listed and followed here: this
  // composer's, and a Human Review edit's.
  useEffect(
    () =>
      onCapture((item) => {
        setNow(new Date());
        setCaptures((current) =>
          [{ item, since: Date.now() }, ...current].slice(0, SESSION_CAPTURES),
        );
        void refreshBacklog();
      }),
    [refreshBacklog],
  );

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
      // A capture that settled changes what this page loaded on the server:
      // Home's Human Review card and Task summaries, the /inbox sections. Load
      // them again in place, keeping this card's state (BUG-29).
      if (watched.some(({ item }) => leftProcessing(item, byId.get(item.id)))) {
        router.refresh();
      }
    }, POLL_MS);
    return () => window.clearTimeout(timer);
  }, [captures, refreshBacklog, router]);

  return (
    <Card aria-label="Capture" id="capture" role="region" size="sm">
      <CardContent className="space-y-2">
        <CaptureComposer
          focus={focusOnDesktop ? "desktop" : undefined}
          id="inbox-capture"
          label="Capture a thought"
          placeholder="Capture a thought…"
        />
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
