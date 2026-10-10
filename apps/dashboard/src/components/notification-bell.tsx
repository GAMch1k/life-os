"use client";

import { ArrowRight, Bell } from "lucide-react";
import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverTitle,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  announceNotificationsChanged,
  bellState,
  fetchNotifications,
  fetchUnreadCount,
  fullTime,
  IMPORTANCE_DOTS,
  importanceLabel,
  isUnread,
  listQuery,
  markAllBefore,
  markAllRead,
  markAllReadLocally,
  markRead,
  MIN_REFRESH_GAP_MS,
  NOTIFICATIONS_CHANGED,
  POPOVER_LIMIT,
  preview,
  relativeTime,
  replaceNotification,
  safeLink,
  sourceLabel,
  UNREAD_REFRESH_MS,
  type Notification,
  type UnreadCount,
} from "@/lib/notifications";
import { cn } from "@/lib/utils";

/** The popover's list: what it read and when, or why it has nothing. */
type Latest =
  | { kind: "loading" }
  | { kind: "unavailable" }
  | { kind: "ready"; items: Notification[]; readAt: Date };

/**
 * The bell in the top bar. A dot (never a number) while anything is unread,
 * tinted when something important is; the count is in its accessible name.
 * It reads the unread count on mount, when the window regains focus or the
 * tab becomes visible, after any read action (here or on /notifications),
 * and every minute while the tab is visible. When Notifications cannot be
 * reached it shows no dot and stays quiet. Opening it lists the latest few
 * notifications; `timeZone` is the zone their times are shown in.
 */
export function NotificationBell({ timeZone }: { timeZone: string }) {
  const [count, setCount] = useState<UnreadCount | null>(null);
  const [open, setOpen] = useState(false);
  const [latest, setLatest] = useState<Latest>({ kind: "loading" });
  const openRef = useRef(false);
  const lastRead = useRef(0);
  // Only the newest request of each kind may set state.
  const countRequest = useRef(0);
  const listRequest = useRef(0);

  const refreshCount = useCallback(async () => {
    const request = ++countRequest.current;
    lastRead.current = Date.now();
    const next = await fetchUnreadCount();
    if (request === countRequest.current) setCount(next);
  }, []);

  const refreshList = useCallback(async () => {
    const request = ++listRequest.current;
    const list = await fetchNotifications(
      listQuery({}, undefined, POPOVER_LIMIT),
    );
    if (request !== listRequest.current) return;
    setLatest(
      list
        ? { kind: "ready", items: list.items, readAt: new Date() }
        : { kind: "unavailable" },
    );
  }, []);

  const refresh = useCallback(async () => {
    await Promise.all([
      refreshCount(),
      openRef.current ? refreshList() : undefined,
    ]);
  }, [refreshCount, refreshList]);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void refresh();
    }, UNREAD_REFRESH_MS);
    function onFocus() {
      if (Date.now() - lastRead.current >= MIN_REFRESH_GAP_MS) void refresh();
    }
    function onVisibilityChange() {
      if (document.visibilityState === "visible") onFocus();
    }
    function onChanged() {
      void refresh();
    }
    window.addEventListener("focus", onFocus);
    document.addEventListener("visibilitychange", onVisibilityChange);
    window.addEventListener(NOTIFICATIONS_CHANGED, onChanged);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("focus", onFocus);
      document.removeEventListener("visibilitychange", onVisibilityChange);
      window.removeEventListener(NOTIFICATIONS_CHANGED, onChanged);
    };
  }, [refresh]);

  function onOpenChange(next: boolean) {
    openRef.current = next;
    setOpen(next);
    if (next) {
      // Keep the last list on screen while it is read again.
      if (latest.kind === "unavailable") setLatest({ kind: "loading" });
      void refreshList();
      void refreshCount();
    }
  }

  async function read(item: Notification) {
    if (!isUnread(item)) return;
    const updated = await markRead(item.id);
    if (!updated) return;
    setLatest((current) =>
      current.kind === "ready"
        ? { ...current, items: replaceNotification(current.items, updated) }
        : current,
    );
    announceNotificationsChanged("bell");
  }

  async function readAll() {
    const items = latest.kind === "ready" ? latest.items : [];
    const before = markAllBefore(items);
    if (!before) return;
    if ((await markAllRead(before)) === null) return;
    const readAt = new Date().toISOString();
    setLatest((current) =>
      current.kind === "ready"
        ? {
            ...current,
            items: markAllReadLocally(current.items, before, readAt),
          }
        : current,
    );
    announceNotificationsChanged("bell");
  }

  const state = bellState(count);
  const hasUnread =
    (count?.count ?? 0) > 0 ||
    (latest.kind === "ready" && latest.items.some(isUnread));

  return (
    <Popover onOpenChange={onOpenChange} open={open}>
      <PopoverTrigger
        aria-label={state.label}
        data-dot={state.dot}
        render={
          <Button
            className="text-muted-foreground hover:text-foreground relative"
            size="icon-sm"
            title="Notifications"
            variant="ghost"
          />
        }
      >
        <Bell aria-hidden="true" className="size-4" />
        {state.dot === "none" ? null : (
          <span
            aria-hidden="true"
            className={cn(
              "ring-background absolute top-1 right-1 size-2 rounded-full ring-2",
              state.dot === "high" ? "bg-destructive" : "bg-primary",
            )}
          />
        )}
      </PopoverTrigger>
      <PopoverContent
        align="end"
        className="w-[min(22rem,calc(100vw-1.5rem))] gap-0 p-0"
      >
        <div className="flex min-h-11 items-center gap-2 border-b px-3 py-1.5">
          <PopoverTitle className="text-sm font-semibold">
            Notifications
          </PopoverTitle>
          {hasUnread && latest.kind === "ready" ? (
            <Button
              className="ml-auto"
              onClick={() => void readAll()}
              size="xs"
              variant="ghost"
            >
              Mark all read
            </Button>
          ) : null}
        </div>
        {latest.kind === "loading" ? (
          <p className="text-muted-foreground px-3 py-6 text-center">
            Loading…
          </p>
        ) : latest.kind === "unavailable" ? (
          <p className="text-muted-foreground px-3 py-6 text-center">
            Notifications are unavailable right now.
          </p>
        ) : latest.items.length === 0 ? (
          <p className="text-muted-foreground px-3 py-6 text-center">
            No notifications yet.
          </p>
        ) : (
          <ul
            aria-label="Latest notifications"
            className="max-h-[min(26rem,60dvh)] divide-y overflow-y-auto overscroll-contain"
          >
            {latest.items.map((item) => (
              <li key={item.id}>
                <LatestItem
                  item={item}
                  now={latest.readAt}
                  onClose={() => onOpenChange(false)}
                  onRead={(entry) => void read(entry)}
                  timeZone={timeZone}
                />
              </li>
            ))}
          </ul>
        )}
        <div className="flex border-t px-3 py-2">
          {/* Styled like Home's "Review in Inbox" link. */}
          <Link
            className="text-muted-foreground hover:text-foreground ml-auto inline-flex items-center gap-1 text-xs"
            href="/notifications"
            onClick={() => onOpenChange(false)}
          >
            All notifications{" "}
            <ArrowRight aria-hidden="true" className="size-3" />
          </Link>
        </div>
      </PopoverContent>
    </Popover>
  );
}

/**
 * One notification in the popover. Clicking it marks it read and, when it
 * links to a Life OS page, goes there.
 */
function LatestItem({
  item,
  now,
  onClose,
  onRead,
  timeZone,
}: {
  item: Notification;
  now: Date;
  onClose: () => void;
  onRead: (item: Notification) => void;
  timeZone: string;
}) {
  const unread = isUnread(item);
  const link = safeLink(item.link);
  const className = cn(
    "hover:bg-muted/60 focus-visible:bg-muted flex w-full items-start gap-2.5 px-3 py-2.5 text-left transition-colors outline-none",
    unread && "bg-muted/30",
  );
  const content = (
    <>
      <span
        aria-hidden="true"
        className={cn(
          "mt-1.5 size-2 shrink-0 rounded-full",
          IMPORTANCE_DOTS[item.importance],
        )}
      />
      <span className="min-w-0 flex-1 space-y-0.5">
        <span className="flex items-baseline gap-2">
          <span
            className={cn(
              "min-w-0 flex-1 truncate",
              unread
                ? "text-foreground font-semibold"
                : "text-muted-foreground",
            )}
          >
            {item.title}
          </span>
          <time
            className="text-muted-foreground shrink-0 text-xs"
            dateTime={item.createdAt}
            title={fullTime(item.createdAt, timeZone)}
          >
            {relativeTime(item.createdAt, timeZone, now)}
          </time>
        </span>
        {item.body ? (
          <span className="text-muted-foreground line-clamp-2 block text-xs">
            {preview(item.body)}
          </span>
        ) : null}
        <span className="text-muted-foreground block text-xs">
          {sourceLabel(item.source)} · {importanceLabel(item.importance)}
          {unread ? <span className="sr-only"> · Unread</span> : null}
        </span>
      </span>
    </>
  );
  return link ? (
    <Link
      className={className}
      href={link}
      onClick={() => {
        onRead(item);
        onClose();
      }}
    >
      {content}
    </Link>
  ) : (
    <button className={className} onClick={() => onRead(item)} type="button">
      {content}
    </button>
  );
}
